package downloader

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

func (s *Service) recoverLegacyStaging(job *Job) (bool, error) {
	if s.tmpSandbox == nil || !job.legacySelection || job.selectionCorrupt || job.Status == StatusCancelled {
		return false, nil
	}
	dir := filepath.Join(job.ID, "converted")
	full, err := s.tmpSandbox.Resolve(dir, false)
	if err != nil {
		return false, err
	}
	files, err := safefs.ReadDir(full)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	for _, file := range files {
		if !file.Type().IsRegular() || !strings.EqualFold(filepath.Ext(file.Name()), ".mp3") {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(dir, file.Name()))
		path, err := s.tmpSandbox.Resolve(rel, false, sandbox.LiteralPath)
		if err != nil {
			return false, err
		}
		tags, err := readLegacyStagedTags(path, rel)
		if err != nil {
			return false, err
		}
		if tags == nil || tags.Duration <= 0 {
			continue
		}
		track := resolver.ResolvedTrack{
			TrackID: fmt.Sprintf("legacy_%d", len(job.Tracks)+1), TrackNumber: tags.TrackNumber, DiscNumber: tags.DiscNumber,
			Title: tags.Title, Artist: tags.Artist, Album: tags.Album, AlbumArtist: tags.AlbumArtist,
			TrackTotal: &tags.TrackTotal, DiscTotal: &tags.DiscTotal, Duration: int(tags.Duration),
			ConvertedPath: &rel, Status: "ready", AudioSourceURL: resolver.PublicTrackSource(tags.SourceURL),
		}
		if tags.Year != 0 {
			track.Year = &tags.Year
		}
		if tags.Genre != "" {
			track.Genre = &tags.Genre
		}
		if tags.ISRC != "" {
			track.ISRC = &tags.ISRC
		}
		job.Tracks = append(job.Tracks, track)
	}

	if len(job.Tracks) == 0 {
		if job.Status == StatusWaitingSource || job.Status == StatusReady || job.Status == StatusPartialReady {
			job.SetError("Не удалось восстановить треклист старой задачи")
			return true, nil
		}
		return false, nil
	}
	slices.SortStableFunc(job.Tracks, func(a, b resolver.ResolvedTrack) int {
		if a.DiscNumber != b.DiscNumber {
			return a.DiscNumber - b.DiscNumber
		}
		return a.TrackNumber - b.TrackNumber
	})
	first := job.Tracks[0]
	meta := resolver.ReleaseMetadata{AlbumTitle: first.Album, AlbumArtist: first.AlbumArtist,
		DiscTotal: *first.DiscTotal, TotalTracks: *first.TrackTotal}
	if first.Year != nil {
		meta.Year = *first.Year
	}
	if first.Genre != nil {
		meta.Genre = *first.Genre
	}
	total := max(job.TotalTracks, len(job.Tracks))
	wasReady := job.Status == StatusReady
	job.SetMetadata(meta, job.Tracks)
	if wasReady && len(job.Tracks) == total {
		job.SetReady(len(job.Tracks), total)
	} else {
		job.SetPartialReady(len(job.Tracks), total)
	}
	job.CurrentStep = "Восстановлены готовые файлы старой задачи"
	job.legacySelection = false
	return true, nil
}

// A transcode interrupted before tagging can leave an MP3 in converted.
func readLegacyStagedTags(path, rel string) (*id3.TrackTags, error) {
	file, err := safefs.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var header [4]byte
	if _, err := file.ReadAt(header[:], 0); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	if header != [4]byte{'I', 'D', '3', 4} {
		return nil, nil
	}
	return id3.ReadTagsFromFile(file, rel)
}
