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
	items, err := s.matcher.SearchMultiCandidatesWithLimit(ctx, artist, title, duration, query, limit)
	if s.repo != nil {
		if pin, e := s.repo.GetPinnedSource(ctx, resolver.BuildCacheKey(code, artist, title)); e == nil && pin != nil && resolver.IsAllowedURL(pin.PinnedURL) {
			found := false
			for i := range items {
				if resolver.SameAudioSource(items[i].URL, pin.PinnedURL) {
					items[i].SavedSelection = true
					found = true
				}
			}
			if !found {
				items = append(items, resolver.CandidateTrackItem{URL: pin.PinnedURL, Title: "Сохранённый источник (требует проверки)", SavedSelection: true, SearchIncomplete: err != nil})
			}
		}
	}
	if len(items) == 0 && err != nil {
		return nil, err
	}
	return items, nil
}
