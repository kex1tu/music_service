package audit

import (
	"context"
)

func (s *Service) GetSummary(ctx context.Context) (*AuditSummary, error) {
	summary, err := s.repo.GetAuditSummary(ctx)
	if err != nil {
		return nil, err
	}
	if summary == nil {
		summary = &AuditSummary{}
	}

	// Compute real-time tag issues count
	tracks, err := s.repo.GetTracksForTagAudit(ctx)
	if err != nil {
		return nil, err
	}
	for _, track := range tracks {
		if analyzeTrackIssues(track.ID, track.FilePath, track.Title, track.Artist, track.Album, track.Year,
			track.TrackNumber, track.Duration, track.AlbumArtist) != nil {
			summary.TagIssuesCount++
		}
	}

	return summary, nil
}

func (s *Service) Refresh(ctx context.Context) (*AuditSummary, error) {
	if s.scanner != nil {
		if _, err := s.scanner.ScanAll(ctx); err != nil {
			return nil, err
		}
	}
	return s.GetSummary(ctx)
}

func (s *Service) Search(ctx context.Context, query string, limit int) (*SearchLibraryResult, error) {
	return s.repo.SearchLibrary(ctx, query, limit)
}
