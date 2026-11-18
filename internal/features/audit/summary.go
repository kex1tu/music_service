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
	if err == nil {
		count := 0
		for _, t := range tracks {
			if analyzeTrackIssues(t.ID, t.FilePath, t.Title, t.Artist, t.Album, t.Year, t.TrackNumber, t.Duration, t.AlbumArtist) != nil {
				count++
			}
		}
		summary.TagIssuesCount = count
	}

	return summary, nil
}

func (s *Service) Refresh(ctx context.Context) (*AuditSummary, error) {
	if s.scanner != nil {
		_, _ = s.scanner.ScanAll(ctx)
	}
	return s.GetSummary(ctx)
}

func (s *Service) Search(ctx context.Context, query string, limit int) (*SearchLibraryResult, error) {
	return s.repo.SearchLibrary(ctx, query, limit)
}
