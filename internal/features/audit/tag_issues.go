package audit

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
)

func (s *Service) GetTagIssues(ctx context.Context, filter string, limit int) (*TagIssuesResponse, error) {
	tracks, err := s.repo.GetTracksForTagAudit(ctx)
	if err != nil {
		return nil, err
	}

	var issues []TagIssue
	totalCount := 0

	for _, t := range tracks {
		item := analyzeTrackIssues(t.ID, t.FilePath, t.Title, t.Artist, t.Album, t.Year, t.TrackNumber, t.Duration, t.AlbumArtist)
		if item == nil {
			continue
		}

		if filter != "" && filter != "all" {
			matched := false
			if filter == "safe" || filter == "safe_autofix" {
				matched = (item.Severity == "safe_autofix")
			} else if filter == "review" || filter == "review_needed" {
				matched = (item.Severity == "review_needed")
			} else {
				for _, iss := range item.Issues {
					if iss == filter {
						matched = true
						break
					}
					if filter == "missing_tags" && (iss == "missing_year" || iss == "missing_album") {
						matched = true
						break
					}
				}
			}
			if !matched {
				continue
			}
		}

		totalCount++
		if limit <= 0 || len(issues) < limit {
			issues = append(issues, *item)
		}
	}

	return &TagIssuesResponse{
		Issues:     issues,
		TotalCount: totalCount,
	}, nil
}

func (s *Service) FixTagIssue(ctx context.Context, req FixTagIssueRequest) (*id3.TrackTags, error) {
	if s.musicSandbox == nil {
		return nil, errors.New("music sandbox not initialized")
	}

	fullPath, err := s.musicSandbox.Resolve(req.Path, false)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) {
			return nil, ErrSandboxViolation
		}
		return nil, fmt.Errorf("%w: %v", ErrFileNotFound, err)
	}

	tags, err := id3.ReadTags(fullPath, req.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read tags: %w", err)
	}

	if strings.TrimSpace(req.Title) != "" {
		tags.Title = strings.TrimSpace(req.Title)
	}
	if strings.TrimSpace(req.Artist) != "" {
		tags.Artist = strings.TrimSpace(req.Artist)
	}
	if strings.TrimSpace(req.Album) != "" {
		tags.Album = strings.TrimSpace(req.Album)
	}
	if req.Year > 0 {
		tags.Year = req.Year
	}

	if s.enricher != nil {
		currentArts := id3.ParseMultiValue(tags.Artist)
		enriched := s.enricher.EnrichTrackArtists(ctx, tags.Title, currentArts)
		if len(enriched) > len(currentArts) {
			tags.Artist = id3.FormatMultiValue(enriched)
		}
	}

	if err := s.writeLibraryTags(ctx, fullPath, *tags, nil); err != nil {
		return nil, fmt.Errorf("failed to write tags: %w", err)
	}

	return tags, nil
}

func (s *Service) FixSafeTagIssues(ctx context.Context) (*FixBatchResponse, error) {
	report, err := s.GetTagIssues(ctx, "safe_autofix", 0)
	if err != nil {
		return nil, err
	}

	fixedCount := 0
	var errs []string

	for _, item := range report.Issues {
		fullPath, err := s.musicSandbox.Resolve(item.FilePath, false)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", item.FilePath, err))
			continue
		}

		tags, err := id3.ReadTags(fullPath, item.FilePath)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", item.FilePath, err))
			continue
		}

		if item.Proposed.Title != "" {
			tags.Title = item.Proposed.Title
		}
		if item.Proposed.Artist != "" {
			tags.Artist = item.Proposed.Artist
		}
		if item.Proposed.AlbumArtist != "" {
			tags.AlbumArtist = item.Proposed.AlbumArtist
		}

		if err := s.writeLibraryTags(ctx, fullPath, *tags, nil); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", item.FilePath, err))
			continue
		}

		fixedCount++
	}

	return &FixBatchResponse{
		Success:    len(errs) == 0,
		FixedCount: fixedCount,
		TotalFound: len(report.Issues),
		Errors:     errs,
	}, nil
}

func (s *Service) FixSelectedTagIssues(ctx context.Context, items []FixTagIssueRequest) (*FixBatchResponse, error) {
	fixedCount := 0
	var errs []string

	for _, it := range items {
		if it.Path == "" {
			continue
		}
		_, err := s.FixTagIssue(ctx, it)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", it.Path, err))
		} else {
			fixedCount++
		}
	}

	return &FixBatchResponse{
		Success:    len(errs) == 0,
		FixedCount: fixedCount,
		TotalFound: len(items),
		Errors:     errs,
	}, nil
}

func (s *Service) FixAllTagIssues(ctx context.Context) (*FixBatchResponse, error) {
	report, err := s.GetTagIssues(ctx, "all", 0)
	if err != nil {
		return nil, err
	}

	fixedCount := 0
	var errs []string

	for _, item := range report.Issues {
		fullPath, err := s.musicSandbox.Resolve(item.FilePath, false)
		if err != nil {
			continue
		}

		tags, err := id3.ReadTags(fullPath, item.FilePath)
		if err != nil {
			continue
		}

		if item.Proposed.Title != "" {
			tags.Title = item.Proposed.Title
		}
		if item.Proposed.Artist != "" {
			tags.Artist = item.Proposed.Artist
		}
		if item.Proposed.AlbumArtist != "" {
			tags.AlbumArtist = item.Proposed.AlbumArtist
		}
		if item.Proposed.Album != "" {
			tags.Album = item.Proposed.Album
		}
		if item.Proposed.Year > 0 && tags.Year == 0 {
			tags.Year = item.Proposed.Year
		}

		if err := s.writeLibraryTags(ctx, fullPath, *tags, nil); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", item.FilePath, err))
			continue
		}

		fixedCount++
	}

	return &FixBatchResponse{
		Success:    len(errs) == 0,
		FixedCount: fixedCount,
		TotalFound: len(report.Issues),
		Errors:     errs,
	}, nil
}

func (s *Service) writeLibraryTags(ctx context.Context, fullPath string, tags id3.TrackTags, cover *id3.CoverInfo) error {
	write := func() error {
		if err := id3.WriteTags(fullPath, tags, cover); err != nil {
			return fmt.Errorf("failed to write tags: %w", err)
		}
		return nil
	}
	if s.trackIndex != nil {
		return s.trackIndex.ChangeTrack(ctx, fullPath, fullPath, false, write)
	}
	if err := write(); err != nil {
		return err
	}
	if s.scanner != nil {
		relative, _ := s.musicSandbox.ToRelative(fullPath)
		return s.scanner.IndexFile(fullPath, relative)
	}
	return nil
}
