package organizer

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/id3"
)

type RedownloadImport struct {
	StagedPath, AlbumPath      string
	OriginalPath, OriginalHash string
	ResultPath, ResultHash     string
}

// ApplyRedownload publishes a complete file at the exact confirmed destination.
// Replacements leave the original sidecars in place.
func (s *Service) ApplyRedownload(ctx context.Context, in RedownloadImport, verify func() error, receipt func(string, string) error) (string, error) {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()

	if in.ResultPath != "" && in.ResultHash != "" {
		full, err := s.musicSandbox.Resolve(in.ResultPath, false, sandbox.LiteralPath)
		if err != nil {
			return "", err
		}
		if hash, err := FileSHA256(full); err == nil && hash == in.ResultHash {
			if err := ApplyTrackChange(ctx, s.trackIndex, full, full, false, func() error { return nil }); err != nil {
				return "", err
			}
			s.discardRedownloadStage(in.StagedPath)
			return in.ResultPath, nil
		}
	}
	staged, err := s.tmpSandbox.Resolve(in.StagedPath, false, sandbox.LiteralPath)
	if err != nil {
		return "", err
	}
	tags, err := id3.ReadTags(staged, in.StagedPath)
	if err != nil {
		return "", err
	}
	destination := in.OriginalPath
	if destination == "" {
		destination, err = s.redownloadDestination(in.AlbumPath, *tags)
		if err != nil {
			return "", err
		}
	}
	full, err := s.musicSandbox.Resolve(destination, false, sandbox.LiteralPath)
	if err != nil {
		return "", err
	}
	err = ApplyTrackChange(ctx, s.trackIndex, staged, full, false, func() error {
		if err := verify(); err != nil {
			return err
		}
		if in.OriginalPath != "" {
			if hash, err := FileSHA256(full); err != nil || hash != in.OriginalHash {
				return errors.New("исходный файл изменился после постановки задачи")
			}
		}
		if err := safefs.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return err
		}
		tmp, err := safefs.CreateTemp(filepath.Dir(full), ".redownload-*")
		if err != nil {
			return err
		}
		scratch := tmp.Name()
		defer safefs.Remove(scratch)
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := copyFile(staged, scratch); err != nil {
			return err
		}
		if in.OriginalPath != "" {
			original, err := id3.ReadTags(full, destination)
			if err != nil {
				return err
			}
			original.SourceURL = tags.SourceURL
			if err := id3.ReplaceAudioTags(scratch, full, *original); err != nil {
				return err
			}
		}
		hash, err := FileSHA256(scratch)
		if err != nil {
			return err
		}
		if err := receipt(destination, hash); err != nil {
			return err
		}
		if in.OriginalPath != "" {
			if hash, err := FileSHA256(full); err != nil || hash != in.OriginalHash {
				return errors.New("исходный файл изменился во время замены")
			}
			return safefs.Rename(scratch, full)
		}
		if err := safefs.Link(scratch, full); err != nil {
			if errors.Is(err, os.ErrExist) {
				return errors.New("в папке релиза уже есть файл с таким именем")
			}
			return err
		}
		if err := moveCompanionFiles(staged, full); err != nil {
			return errors.Join(err, safefs.Remove(full))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	s.discardRedownloadStage(in.StagedPath)
	return destination, nil
}

func (s *Service) discardRedownloadStage(path string) {
	base := strings.TrimSuffix(path, filepath.Ext(path))
	for _, ext := range append([]string{filepath.Ext(path)}, companionExtensions...) {
		full, err := s.tmpSandbox.Resolve(base+ext, false, sandbox.LiteralPath)
		if err == nil {
			err = safefs.Remove(full)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("[Organizer] could not clean confirmed staging file %q: %v", base+ext, err)
		}
	}
}

func (s *Service) redownloadDestination(album string, tags id3.TrackTags) (string, error) {
	full, err := s.musicSandbox.Resolve(album, false, sandbox.LiteralPath)
	if err != nil {
		return "", err
	}
	dest := backfillDestination{root: album, discs: make(map[int]string), multiDisc: tags.DiscTotal > 1 || tags.DiscNumber > 1}
	entries, err := safefs.ReadDir(full)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	for _, entry := range entries {
		if n := db.DiscFolderNumber(entry.Name()); entry.IsDir() && n > 0 && dest.discs[n] == "" {
			dest.discs[n], dest.multiDisc = entry.Name(), true
		}
	}
	_, name := s.ComputeTargetPath(tags.AlbumArtist, tags.Album, tags.TrackNumber, tags.Title, "track.mp3")
	return dest.trackPath("", tags.DiscNumber, filepath.Base(name)), nil
}
