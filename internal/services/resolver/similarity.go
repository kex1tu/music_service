package resolver

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"music-manager/internal/db"
)

// SimilarityEngine отвечает за построение графа похожих исполнителей и кэширование в SQLite.
type SimilarityEngine struct {
	deezer *DeezerClient
	lastfm *LastfmClient
	db     *db.DB
}

// NewSimilarityEngine создает новый экземпляр SimilarityEngine.
func NewSimilarityEngine(deezer *DeezerClient, lastfm *LastfmClient, database *db.DB) *SimilarityEngine {
	return &SimilarityEngine{
		deezer: deezer,
		lastfm: lastfm,
		db:     database,
	}
}

// GetRelatedArtistsFromCache возвращает похожих исполнителей из локальной SQLite базы данных.
func (s *SimilarityEngine) GetRelatedArtistsFromCache(ctx context.Context, artistName string, limit int) ([]string, error) {
	if s.db == nil {
		return nil, nil
	}
	return s.db.GetRelatedArtists(ctx, artistName, limit)
}

// FetchAndCacheRelations опрашивает Deezer и Last.fm API, рассчитывает веса и сохраняет связи в SQLite.
func (s *SimilarityEngine) FetchAndCacheRelations(ctx context.Context, artistName string) ([]string, error) {
	artistName = strings.TrimSpace(artistName)
	if artistName == "" {
		return nil, fmt.Errorf("artist name cannot be empty")
	}

	type candidate struct {
		name   string
		score  float64
		source string
	}

	var mu sync.Mutex
	candidates := make(map[string]*candidate)

	var wg sync.WaitGroup

	// 1. Deezer Related API
	if s.deezer != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			art, err := s.deezer.SearchArtist(ctx, artistName)
			if err != nil || art == nil {
				return
			}

			related, err := s.deezer.GetRelatedArtists(ctx, strconv.FormatInt(art.ID, 10))
			if err != nil {
				return
			}

			mu.Lock()
			defer mu.Unlock()
			for idx, rel := range related {
				key := CleanArtistKey(rel.Name)
				if key == CleanArtistKey(artistName) || key == "" {
					continue
				}
				// Вес убывает от 0.95 до 0.50 в зависимости от позиции
				rankScore := math.Max(0.50, 0.95-float64(idx)*0.03)
				if existing, exists := candidates[key]; exists {
					existing.score = math.Min(1.0, existing.score+0.2)
					existing.source = "deezer+" + existing.source
				} else {
					candidates[key] = &candidate{
						name:   rel.Name,
						score:  rankScore,
						source: "deezer",
					}
				}
			}
		}()
	}

	// 2. Last.fm Similar API
	if s.lastfm != nil && s.lastfm.IsConfigured() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			similar, err := s.lastfm.GetSimilarArtists(ctx, artistName, 15)
			if err != nil {
				return
			}

			mu.Lock()
			defer mu.Unlock()
			for _, sim := range similar {
				key := CleanArtistKey(sim.Name)
				if key == CleanArtistKey(artistName) || key == "" {
					continue
				}
				score := sim.Match
				if score <= 0.05 {
					score = 0.5
				}
				if existing, exists := candidates[key]; exists {
					existing.score = math.Min(1.0, (existing.score+score)/2.0+0.15)
					existing.source = existing.source + "+lastfm"
				} else {
					candidates[key] = &candidate{
						name:   sim.Name,
						score:  score,
						source: "lastfm",
					}
				}
			}
		}()
	}

	wg.Wait()

	var items []db.ArtistRelationItem
	var names []string

	for _, c := range candidates {
		items = append(items, db.ArtistRelationItem{
			ArtistName:  artistName,
			RelatedName: c.name,
			Score:       c.score,
			Source:      c.source,
		})
		names = append(names, c.name)
	}

	if s.db != nil && len(items) > 0 {
		_ = s.db.SaveArtistRelations(ctx, artistName, items)
	}

	return names, nil
}
