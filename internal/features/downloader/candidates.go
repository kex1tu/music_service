package downloader

import (
	"context"
	"errors"

	"music-manager/internal/services/resolver"
)

// GetCandidates выполняет поиск кандидатов для ручной привязки.
func (s *Service) GetCandidates(ctx context.Context, artist, title string, duration int, query string, isrc ...string) ([]resolver.CandidateTrackItem, error) {
	code := ""
	if len(isrc) > 0 {
		code = isrc[0]
	}
	return s.GetCandidatesWithLimit(ctx, artist, title, duration, query, code, 30)
}

func (s *Service) GetCandidatesWithLimit(ctx context.Context, artist, title string, duration int, query, code string, limit int) ([]resolver.CandidateTrackItem, error) {
	if s.matcher == nil {
		return nil, errors.New("audio matcher is not available")
	}
	return s.matcher.SearchMultiCandidatesWithLimit(ctx, artist, title, duration, query, limit)
}
