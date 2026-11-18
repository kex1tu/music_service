package audit

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
)

func (s *Service) GetInvalidPaths(ctx context.Context) ([]InvalidPathRecord, error) {
	return s.repo.GetInvalidPaths(ctx)
}

func (s *Service) FixPath(ctx context.Context, relPath string, overwrite bool) (*FixPathResponse, error) {
	if s.musicSandbox == nil {
		return nil, errors.New("music sandbox not initialized")
	}

	fullPath, err := s.musicSandbox.Resolve(relPath, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFileNotFound, err)
	}

	tags, err := id3.ReadTags(fullPath, relPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read tags: %w", err)
	}

	album := strings.TrimSpace(tags.Album)
	if album == "" || strings.EqualFold(album, "single") || strings.EqualFold(album, "singles") {
		if strings.TrimSpace(tags.Title) != "" {
			album = strings.TrimSpace(tags.Title)
		}
	}
	artist := strings.TrimSpace(tags.AlbumArtist)
	if artist == "" {
		artist = strings.TrimSpace(tags.Artist)
	}
	if artist == "" {
		artist = "Unknown Artist"
	}

	var targetRel string
	if s.organizer != nil {
		_, targetRel = s.organizer.ComputeTargetPath(artist, album, tags.TrackNumber, tags.Title, filepath.Base(fullPath))
	} else {
		targetRel = filepath.Join(artist, album, fmt.Sprintf("%02d. %s.mp3", tags.TrackNumber, tags.Title))
	}

	targetFull, err := s.musicSandbox.Resolve(targetRel, true)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to resolve target %s: %v", ErrSandboxViolation, targetRel, err)
	}

	if fullPath != targetFull {
		oldDir := filepath.Dir(fullPath)
		actualDest, err := organizer.MoveTrackWithCompanions(fullPath, targetFull, overwrite)
		if err != nil {
			return nil, fmt.Errorf("failed to move file: %w", err)
		}

		actualRel, _ := s.musicSandbox.ToRelative(actualDest)
		_ = s.repo.UpdateTrackPath(ctx, relPath, actualRel)
		_ = s.repo.DeleteAuditIssueByPath(ctx, relPath, "invalid_path")
		targetRel = actualRel

		organizer.CleanEmptyParents(oldDir, s.musicSandbox.Root())
	}

	return &FixPathResponse{
		Success:    true,
		SourcePath: relPath,
		TargetPath: targetRel,
		Message:    "Путь успешно исправлен на канонический",
	}, nil
}

func (s *Service) FixAllPaths(ctx context.Context) (*FixAllPathsResponse, error) {
	paths, err := s.repo.GetInvalidPaths(ctx)
	if err != nil {
		return nil, err
	}

	fixed := 0
	var errs []string
	oldDirs := make(map[string]bool)

	for _, p := range paths {
		fullPath, err := s.musicSandbox.Resolve(p.FilePath, false)
		if err != nil {
			continue
		}
		targetFull, err := s.musicSandbox.Resolve(p.SuggestedPath, true)
		if err != nil {
			continue
		}

		oldDir := filepath.Dir(fullPath)
		actualDest, err := organizer.MoveTrackWithCompanions(fullPath, targetFull, false)

		if err == nil {
			actualRel, _ := s.musicSandbox.ToRelative(actualDest)
			_ = s.repo.UpdateTrackPath(ctx, p.FilePath, actualRel)
			_ = s.repo.DeleteAuditIssueByPath(ctx, p.FilePath, "invalid_path")
			fixed++
			oldDirs[oldDir] = true
		} else {
			errs = append(errs, fmt.Sprintf("%s: %v", p.FilePath, err))
		}
	}

	root := s.musicSandbox.Root()
	for dir := range oldDirs {
		organizer.CleanEmptyParents(dir, root)
	}

	return &FixAllPathsResponse{
		Success:      len(errs) == 0,
		TotalInvalid: len(paths),
		FixedCount:   fixed,
		Errors:       errs,
	}, nil
}

func (s *Service) Reorganize(ctx context.Context) (*ReorganizeLibraryResponse, error) {
	paths, err := s.repo.GetInvalidPaths(ctx)
	if err != nil {
		return nil, err
	}

	moved := 0
	oldDirs := make(map[string]bool)

	for _, tr := range paths {
		if tr.SuggestedPath != tr.FilePath {
			srcFull, err := s.musicSandbox.Resolve(tr.FilePath, false)
			if err != nil {
				continue
			}
			dstFull, err := s.musicSandbox.Resolve(tr.SuggestedPath, true)
			if err != nil {
				continue
			}

			oldDir := filepath.Dir(srcFull)
			actualDest, err := organizer.MoveTrackWithCompanions(srcFull, dstFull, false)

			if err == nil {
				actualRel, _ := s.musicSandbox.ToRelative(actualDest)
				moved++
				_ = s.repo.UpdateTrackPath(ctx, tr.FilePath, actualRel)
				_ = s.repo.DeleteAuditIssueByPath(ctx, tr.FilePath, "invalid_path")
				oldDirs[oldDir] = true
			}
		}
	}

	root := s.musicSandbox.Root()
	for dir := range oldDirs {
		organizer.CleanEmptyParents(dir, root)
	}

	if s.scanner != nil {
		_, _ = s.scanner.ScanAll(ctx)
	}

	return &ReorganizeLibraryResponse{
		Success:     true,
		TotalTracks: len(paths),
		MovedTracks: moved,
		TotalAlbums: 1,
		Errors:      []string{},
	}, nil
}

func (s *Service) NormalizeNavidrome(ctx context.Context) (*NavidromeReport, error) {
	if s.organizer == nil {
		return nil, errors.New("organizer service is not initialized")
	}

	onTrack := func(relPath string) {
		if s.scanner != nil {
			if full, err := s.musicSandbox.Resolve(relPath, false); err == nil {
				_ = s.scanner.IndexFile(full, relPath)
			}
		}
	}

	rep, err := s.organizer.NormalizeNavidromeLibrary(ctx, onTrack)
	if err != nil {
		return nil, err
	}

	return &NavidromeReport{
		DirectoriesFixed: rep.DirectoriesFixed,
		FilesPermsFixed:  rep.FilesPermsFixed,
		TagsNormalized:   rep.TagsNormalized,
		SinglesFixed:     rep.SinglesFixed,
		TotalScanned:     rep.TotalScanned,
	}, nil
}
