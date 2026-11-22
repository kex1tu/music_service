package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
)

func deezerTrackID(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Hostname() != "deezer.com" && u.Hostname() != "www.deezer.com") ||
		path.Base(path.Dir(u.Path)) != "track" {
		return ""
	}
	id := path.Base(u.Path)
	if n, err := strconv.ParseInt(id, 10, 64); err != nil || n < 1 {
		return ""
	}
	return id
}

func (r BackfillRequest) selects(gap db.BackfillSelection) bool {
	if len(r.Positions) > 0 {
		for _, selected := range r.Positions {
			if selected.TrackPosition == gap.TrackPosition &&
				(selected.DeezerID == "" || selected.DeezerID == gap.DeezerID) {
				return true
			}
		}
		return false
	}
	if len(r.TrackURLs) > 0 {
		for _, rawURL := range r.TrackURLs {
			if gap.DeezerID != "" && gap.DeezerID == deezerTrackID(rawURL) ||
				gap.DeezerURL != "" && gap.DeezerURL == rawURL {
				return true
			}
		}
		return false
	}
	return len(r.TrackNumbers) == 0 || slices.Contains(r.TrackNumbers, gap.TrackNumber)
}

func (r BackfillRequest) selectionCount() int {
	for _, count := range []int{len(r.Positions), len(r.TrackURLs), len(r.TrackNumbers)} {
		if count != 0 {
			return count
		}
	}
	return 0
}

func (s *Service) freezeBackfillSelection(ctx context.Context, folder string, selection *BackfillRequest) error {
	seen := make(map[db.TrackPosition]bool)
	for i := range selection.Positions {
		position := &selection.Positions[i]
		if position.TrackNumber < 1 || position.DiscNumber < 1 || seen[position.TrackPosition] {
			return fmt.Errorf("invalid or duplicate backfill position %+v", position.TrackPosition)
		}
		seen[position.TrackPosition] = true
		if position.DeezerID == "" {
			position.DeezerID = deezerTrackID(position.DeezerURL)
		}
	}
	if s.repo == nil || folder == "" || len(selection.Positions) > 0 {
		return nil
	}
	missing, err := s.repo.GetMissingTracksForAlbum(ctx, folder)
	if err != nil {
		return err
	}
	var positions []db.BackfillSelection
	numbers := make(map[int]bool)
	for _, gap := range missing {
		if !selection.selects(gap.BackfillSelection) {
			continue
		}
		if len(selection.TrackURLs) == 0 && len(selection.TrackNumbers) > 0 && numbers[gap.TrackNumber] {
			return fmt.Errorf("track %d occurs on multiple discs; select a disc", gap.TrackNumber)
		}
		numbers[gap.TrackNumber] = true
		positions = append(positions, gap.BackfillSelection)
	}
	if len(positions) > 0 &&
		(len(selection.TrackURLs) == 0 || len(positions) == len(selection.TrackURLs)) &&
		(len(selection.TrackNumbers) == 0 || len(selection.TrackURLs) > 0 || len(positions) == len(selection.TrackNumbers)) {
		selection.Positions = positions
	}
	return nil
}

func (s *Service) resolveBackfill(ctx context.Context, job *Job) (*resolver.ReleaseMetadata, []resolver.ResolvedTrack, error) {
	var folder string
	var checked bool
	if job.TargetAlbumPath != nil {
		folder = *job.TargetAlbumPath
	}
	if s.repo != nil && folder != "" {
		var err error
		checked, err = s.repo.albumCatalogChecked(ctx, folder)
		if err != nil {
			return nil, nil, err
		}
	}
	pickedRelease := job.Source == "release_picker" && job.Backfill != nil && len(job.Tracks) > 0
	if pickedRelease && !checked {
		return &job.AlbumMetadata, job.Tracks, nil
	}

	var selection BackfillRequest
	if job.Backfill != nil {
		selection = *job.Backfill
	} else if len(job.backfillTrackNumbers) > 0 || len(job.backfillTrackURLs) > 0 {
		selection.TrackNumbers = job.backfillTrackNumbers
		selection.TrackURLs = job.backfillTrackURLs
	} else if deezerTrackID(job.URL) != "" {
		selection.TrackURLs = []string{job.URL}
	}

	manuallyBound := make(map[db.TrackPosition]resolver.ResolvedTrack)
	pickedTracks := make(map[db.TrackPosition]resolver.ResolvedTrack)
	for _, track := range job.Tracks {
		position := db.ReleasePosition(track.DiscNumber, track.TrackNumber)
		if pickedRelease {
			pickedTracks[position] = track
		}
		if track.VersionStatus == "manual" && track.AudioSourceURL != "" {
			manuallyBound[position] = track
		}
	}
	if !checked && len(manuallyBound) > 0 && len(manuallyBound) == selection.selectionCount() && len(manuallyBound) == len(job.Tracks) {
		return &job.AlbumMetadata, job.Tracks, nil
	}

	var tracks []resolver.ResolvedTrack
	knownGaps := make(map[db.TrackPosition]bool)
	selectedGaps := make(map[db.TrackPosition]bool)
	if s.repo != nil && folder != "" {
		missing, err := s.repo.GetMissingTracksForAlbum(ctx, folder)
		if err != nil {
			return nil, nil, err
		}
		numbers := make(map[int]bool)
		for _, gap := range missing {
			knownGaps[gap.TrackPosition] = true
			if !selection.selects(gap.BackfillSelection) {
				continue
			}
			if len(selection.Positions) == 0 && len(selection.TrackURLs) == 0 &&
				len(selection.TrackNumbers) > 0 && numbers[gap.TrackNumber] {
				return nil, nil, fmt.Errorf("track %d occurs on multiple discs; select a disc", gap.TrackNumber)
			}
			numbers[gap.TrackNumber] = true
			track := resolvedBackfillTrack(gap.CatalogTrack)
			if mapped, ok := pickedTracks[gap.TrackPosition]; ok && (gap.DeezerID == "" || mapped.TrackID == track.TrackID) {
				track.AudioSourceURL, track.VersionStatus, track.VersionPolicy = mapped.AudioSourceURL, mapped.VersionStatus, mapped.VersionPolicy
			} else if manual, ok := manuallyBound[gap.TrackPosition]; ok && manual.TrackID == track.TrackID {
				track.AudioSourceURL, track.VersionStatus, track.VersionPolicy = manual.AudioSourceURL, manual.VersionStatus, manual.VersionPolicy
			}
			tracks = append(tracks, track)
			selectedGaps[gap.TrackPosition] = true
		}
	}
	if checked {
		return &job.AlbumMetadata, tracks, nil
	}
	if count := selection.selectionCount(); len(tracks) > 0 && (count == 0 || len(tracks) == count) {
		return nil, tracks, nil
	}

	if s.resolver == nil {
		return nil, nil, errors.New("Не удалось разрешить выбранные позиции в каталоге")
	}
	albumURL := job.URL
	if selection.AlbumID != "" {
		albumURL = "https://www.deezer.com/album/" + selection.AlbumID
	}
	if (albumURL == "" || deezerTrackID(albumURL) != "") && job.AlbumTitle != nil && s.resolver.Deezer() != nil {
		artist := ""
		if job.ArtistName != nil {
			artist = *job.ArtistName
		}
		albums, err := s.resolver.Deezer().SearchAlbums(ctx, fmt.Sprintf(`artist:"%s" album:"%s"`, artist, *job.AlbumTitle))
		if err == nil && len(albums) > 0 {
			albumURL = fmt.Sprintf("https://www.deezer.com/album/%d", albums[0].ID)
		}
	}

	present := make(map[db.TrackPosition]bool)
	if s.repo != nil && folder != "" {
		var err error
		present, err = s.repo.GetAlbumTrackPositions(ctx, folder)
		if err != nil {
			return nil, nil, err
		}
	}
	if albumURL == "" || deezerTrackID(albumURL) != "" && len(selection.Positions) != 0 {
		return nil, nil, errors.New("Не удалось найти треклист для выбранных позиций")
	}
	meta, catalog, err := s.resolver.Resolve(ctx, albumURL, "deezer")
	if err != nil {
		return nil, nil, err
	}
	numbers := make(map[int]bool)
	for _, track := range catalog {
		position := db.ReleasePosition(track.DiscNumber, track.TrackNumber)
		id := strings.TrimPrefix(track.TrackID, "dz_")
		if selectedGaps[position] || !selection.selects(db.BackfillSelection{TrackPosition: position, DeezerID: id}) {
			continue
		}
		if present[position] && !knownGaps[position] {
			continue
		}
		if len(selection.Positions) == 0 && len(selection.TrackURLs) == 0 && len(selection.TrackNumbers) > 0 && numbers[track.TrackNumber] {
			return nil, nil, fmt.Errorf("track %d occurs on multiple discs; select a disc", track.TrackNumber)
		}
		numbers[track.TrackNumber] = true
		if manual, ok := manuallyBound[position]; ok && manual.TrackID == track.TrackID {
			track.AudioSourceURL, track.VersionStatus, track.VersionPolicy = manual.AudioSourceURL, manual.VersionStatus, manual.VersionPolicy
		}
		tracks = append(tracks, track)
	}
	return meta, tracks, nil
}

func resolvedBackfillTrack(track db.CatalogTrack) resolver.ResolvedTrack {
	id := fmt.Sprintf("backfill_%d_%d", track.DiscNumber, track.TrackNumber)
	if track.DeezerID != "" {
		id = "dz_" + track.DeezerID
	}
	resolved := resolver.ResolvedTrack{
		TrackID: id, TrackNumber: track.TrackNumber, DiscNumber: track.DiscNumber,
		Title: track.Title, Artist: track.Artist, Duration: track.Duration, Status: "pending",
	}
	if track.ISRC != "" {
		resolved.ISRC = &track.ISRC
	}
	return resolved
}
