package filemanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/features/downloader"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
)

type RedownloadQueue interface {
	PrepareRedownload(context.Context, string, []resolver.ResolvedTrack, []downloader.RedownloadTarget, bool) (*downloader.Job, error)
	ConfirmRedownload(context.Context, string, func(*downloader.Job, func() error) error) error
}

type RedownloadOrganizer interface {
	ApplyRedownload(context.Context, organizer.RedownloadImport, func() error, func(string, string) error) (string, error)
}

type redownloadLibrary interface {
	albumRedownloadTracks(context.Context, string) ([]resolver.ResolvedTrack, []downloader.RedownloadTarget, error)
	redownloadPositionFiles(context.Context, string, db.TrackPosition) ([]string, error)
	verifyRedownloadAlbum(context.Context, string, int64, int64) error
}

func (s *Service) prepareRedownload(ctx context.Context, req RedownloadRequest) (*RedownloadResponse, error) {
	if strings.TrimSpace(req.Path) == "" || req.Path == "." {
		return nil, ErrInvalidPath
	}
	full, err := s.musicSandbox.Resolve(req.Path, false, sandbox.LiteralPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSandboxViolation, err)
	}
	path, err := s.musicSandbox.ToRelative(full)
	if err != nil {
		return nil, err
	}
	info, err := safefs.Stat(full)
	if err != nil {
		return nil, ErrFileNotFound
	}
	folder, kind := db.AlbumRoot(filepath.ToSlash(filepath.Dir(path))), "track"
	var tracks []resolver.ResolvedTrack
	var targets []downloader.RedownloadTarget
	if info.IsDir() {
		kind, folder = "album", db.AlbumRoot(path)
		repo, ok := s.repo.(redownloadLibrary)
		if !ok {
			return nil, errors.New("индекс релиза недоступен")
		}
		tracks, targets, err = repo.albumRedownloadTracks(ctx, folder)
		if err != nil {
			return nil, err
		}
	} else {
		if !strings.EqualFold(filepath.Ext(path), ".mp3") {
			return nil, ErrInvalidPath
		}
		tags, err := id3.ReadTags(full, path)
		if err != nil {
			return nil, err
		}
		trackNumber := tags.TrackNumber
		if !tags.HasTrackNumber {
			trackNumber = 0
		}
		position := db.ReleasePosition(db.LocalDiscNumber(filepath.Dir(path), tags.DiscNumber), trackNumber)
		track := releaseDownloadTrack(position, tags.Title, tags.Artist, tags.Album, tags.AlbumArtist, int(tags.Duration), tags.ISRC)
		track.AudioSourceURL = resolver.PublicTrackSource(tags.SourceURL)
		if req.TargetURL != "" {
			track.AudioSourceURL = resolver.PublicTrackSource(req.TargetURL)
			if track.AudioSourceURL == "" {
				return nil, ErrInvalidPath
			}
		}
		tracks = []resolver.ResolvedTrack{track}
		targets = []downloader.RedownloadTarget{{TrackID: track.TrackID, LibraryPath: path}}
	}
	for i := range targets {
		target := &targets[i]
		if target.LibraryPath == "" {
			continue
		}
		full, err := s.musicSandbox.Resolve(target.LibraryPath, false, sandbox.LiteralPath)
		if err != nil {
			return nil, err
		}
		target.OriginalHash, err = organizer.FileSHA256(full)
		if err != nil {
			return nil, err
		}
	}
	if len(tracks) == 0 {
		return nil, errors.New("нет дорожек для повторной загрузки")
	}
	run := kind == "track" && !req.ChooseSource && tracks[0].AudioSourceURL != ""
	job, err := s.redownloadQueue.PrepareRedownload(ctx, folder, tracks, targets, run)
	if err != nil {
		return nil, err
	}
	return &RedownloadResponse{Success: true, JobID: job.ID, Type: kind, SourceURL: job.URL, Message: job.CurrentStep}, nil
}

func releaseDownloadTrack(position db.TrackPosition, title, artist, album, albumArtist string, duration int, isrc string) resolver.ResolvedTrack {
	if albumArtist == "" {
		albumArtist = artist
	}
	t := resolver.ResolvedTrack{
		TrackID: fmt.Sprintf("redownload_%d_%d", position.DiscNumber, position.TrackNumber),
		Title:   title, Artist: artist, Album: album, AlbumArtist: albumArtist, Duration: duration,
		DiscNumber: position.DiscNumber, TrackNumber: position.TrackNumber, Status: "waiting_source",
	}
	if isrc != "" {
		t.ISRC = &isrc
	}
	return t
}

func (r *SQLiteRepository) albumRedownloadTracks(ctx context.Context, folder string) ([]resolver.ResolvedTrack, []downloader.RedownloadTarget, error) {
	var albumID, catalogRevision int64
	var album, albumArtist string
	if err := r.db.QueryRowContext(ctx, `SELECT al.id, al.catalog_revision, al.title, COALESCE(NULLIF(al.album_artist, ''), a.name)
		FROM albums al JOIN artists a ON a.id = al.artist_id WHERE al.folder_path = ?`, folder).Scan(&albumID, &catalogRevision, &album, &albumArtist); err != nil {
		return nil, nil, err
	}
	catalog, err := db.ReleaseTracklist(ctx, r.db, albumID, false)
	if err != nil {
		return nil, nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT t.id, MAX(1, COALESCE(t.disc_number, 1)), COALESCE(t.track_number, 0), t.file_path,
		t.title, a.name, CAST(COALESCE(t.duration, 0) AS INTEGER), COALESCE(t.isrc, '')
		FROM tracks t JOIN artists a ON a.id = t.artist_id WHERE t.album_id = ?
		ORDER BY 2, 3, file_path`, albumID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	type slot struct {
		track resolver.ResolvedTrack
		paths []string
	}
	local := make(map[db.TrackPosition]slot)
	var order []db.TrackPosition
	var unnumbered []slot
	for rows.Next() {
		var trackID int64
		var position db.TrackPosition
		var title, artist, path, isrc string
		var duration int
		if err := rows.Scan(&trackID, &position.DiscNumber, &position.TrackNumber, &path, &title, &artist, &duration, &isrc); err != nil {
			return nil, nil, err
		}
		if position.TrackNumber <= 0 {
			track := releaseDownloadTrack(position, title, artist, album, albumArtist, duration, isrc)
			track.TrackID = fmt.Sprintf("redownload_local_%d", trackID)
			unnumbered = append(unnumbered, slot{track: track, paths: []string{path}})
			continue
		}
		value, ok := local[position]
		if !ok {
			order = append(order, position)
			value.track = releaseDownloadTrack(position, title, artist, album, albumArtist, duration, isrc)
		}
		value.paths = append(value.paths, path)
		local[position] = value
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var tracks []resolver.ResolvedTrack
	var targets []downloader.RedownloadTarget
	if len(catalog) > 0 {
		for _, t := range catalog {
			track := releaseDownloadTrack(t.TrackPosition, t.Title, t.Artist, album, albumArtist, t.Duration, t.ISRC)
			paths := local[t.TrackPosition].paths
			trackTarget := downloadTarget(track.TrackID, paths)
			if !t.Present && len(paths) > 0 {
				trackTarget.LibraryPath, trackTarget.Conflict = "", true
			}
			if len(paths) == 0 {
				trackTarget.AlbumID, trackTarget.CatalogRevision = albumID, catalogRevision
			}
			tracks, targets = append(tracks, track), append(targets, trackTarget)
		}
		return tracks, targets, nil
	}
	for _, position := range order {
		value := local[position]
		tracks, targets = append(tracks, value.track), append(targets, downloadTarget(value.track.TrackID, value.paths))
	}
	for _, value := range unnumbered {
		tracks, targets = append(tracks, value.track), append(targets, downloadTarget(value.track.TrackID, value.paths))
	}
	return tracks, targets, nil
}

func (r *SQLiteRepository) verifyRedownloadAlbum(ctx context.Context, root string, albumID, revision int64) error {
	var present int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM albums WHERE folder_path = ? AND id = ? AND catalog_revision = ?`,
		root, albumID, revision).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return db.ErrCatalogAlbumChanged
	}
	return err
}

func downloadTarget(trackID string, paths []string) downloader.RedownloadTarget {
	target := downloader.RedownloadTarget{TrackID: trackID, Conflict: len(paths) > 1}
	if len(paths) == 1 {
		target.LibraryPath = paths[0]
	}
	return target
}

func (r *SQLiteRepository) redownloadPositionFiles(ctx context.Context, root string, position db.TrackPosition) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT t.file_path FROM tracks t JOIN albums a ON a.id = t.album_id
		WHERE a.folder_path = ? AND MAX(1, COALESCE(t.disc_number, 1)) = ? AND t.track_number = ?`,
		root, position.DiscNumber, position.TrackNumber)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}
