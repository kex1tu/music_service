package organizer

import (
	"fmt"
	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
	"music-manager/internal/db"
)

type backfillDestination struct {
	root      string
	discs     map[int]string
	multiDisc bool
}

// MoveAlbumInto imports missing tracks into the selected local edition.
func (s *Service) MoveAlbumInto(target, fallbackAlbum, fallbackAlbumArtist string, tracks []TrackTags, coverBase64 string) (*OrganizerResult, error) {
	full, err := s.musicSandbox.Resolve(target, true)
	if err != nil {
		return nil, err
	}
	root, err := s.musicSandbox.ToRelative(full)
	if err != nil {
		return nil, err
	}
	if db.AlbumRoot(root) != root {
		return nil, fmt.Errorf("backfill target must be the root of the release")
	}
	entries, err := safefs.ReadDir(full)
	if err != nil {
		return nil, err
	}
	dest := &backfillDestination{root: root, discs: make(map[int]string)}
	for _, entry := range entries {
		if disc := db.DiscFolderNumber(entry.Name()); entry.IsDir() && disc > 0 && dest.discs[disc] == "" {
			dest.discs[disc] = entry.Name()
			dest.multiDisc = true
		}
	}
	for _, track := range tracks {
		if track.DiscNumber > 1 || track.DiscTotal > 1 {
			dest.multiDisc = true
		}
		if strings.HasPrefix(track.Path, "tmp_downloads/") || strings.HasPrefix(track.Path, "tmp/") {
			continue
		}
		fullPath, err := s.musicSandbox.Resolve(track.Path, false)
		if err != nil {
			return nil, err
		}
		rel, err := s.musicSandbox.ToRelative(fullPath)
		if err != nil || db.AlbumRoot(filepath.ToSlash(filepath.Dir(rel))) != root {
			return nil, fmt.Errorf("track %q is outside the selected release", track.Path)
		}
	}
	return s.moveAlbum(root, fallbackAlbum, fallbackAlbumArtist, tracks, coverBase64, dest)
}

func (d *backfillDestination) trackPath(existing string, disc int, filename string) string {
	if existing != "" && db.AlbumRoot(filepath.ToSlash(filepath.Dir(existing))) == d.root {
		return existing
	}
	if !d.multiDisc {
		return filepath.Join(d.root, filename)
	}
	disc = max(1, disc)
	folder := d.discs[disc]
	if folder == "" {
		folder = fmt.Sprintf("CD%d", disc)
	}
	return filepath.Join(d.root, folder, filename)
}
