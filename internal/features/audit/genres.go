package audit

import (
	"context"
	"fmt"

	"music-manager/internal/id3"
)

func (s *Service) GetNonEnGenres(ctx context.Context) ([]NonEnGenreItem, error) {
	return s.repo.GetNonEnGenreTracks(ctx)
}

func (s *Service) RetagGenres(ctx context.Context) (*RetagGenresResponse, error) {
	items, err := s.repo.GetNonEnGenreTracks(ctx)
	if err != nil {
		return nil, err
	}

	var updatedItems []NonEnGenreItem
	var errs []string
	updatedCount := 0

	for _, t := range items {
		canon := t.CanonicalGenre
		if canon == "" {
			normRes := id3.AnalyzeAndNormalizeGenre(t.CurrentGenre)
			canon = normRes.CanonicalGenre
		}
		if canon == "" {
			canon = t.CurrentGenre
		}

		fullPath, err := s.musicSandbox.Resolve(t.Path, false)
		if err != nil {
			errs = append(errs, fmt.Sprintf("invalid path %s: %v", t.Path, err))
			continue
		}

		currentTags, err := id3.ReadTags(fullPath, t.Path)
		if err != nil {
			errs = append(errs, fmt.Sprintf("read tags error %s: %v", t.Path, err))
			continue
		}

		currentTags.Genre = canon
		if err := id3.WriteTags(fullPath, *currentTags, nil); err != nil {
			errs = append(errs, fmt.Sprintf("write tags error %s: %v", t.Path, err))
			continue
		}

		// Update repository
		var trackID int64
		if s.db != nil {
			_ = s.db.QueryRowContext(ctx, "SELECT id FROM tracks WHERE file_path = ?;", t.Path).Scan(&trackID)
		}
		if trackID > 0 {
			_ = s.repo.RetagTrackGenre(ctx, trackID, canon)
		}

		updatedCount++
		updatedItems = append(updatedItems, NonEnGenreItem{
			Path:           t.Path,
			CurrentGenre:   t.CurrentGenre,
			CanonicalGenre: canon,
			IsAutoFixed:    true,
		})
	}

	return &RetagGenresResponse{
		Success:      len(errs) == 0,
		UpdatedCount: updatedCount,
		TotalChecked: len(items),
		Items:        updatedItems,
		Errors:       errs,
	}, nil
}
