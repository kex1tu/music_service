package resolver

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SoundCloudSearchItem is a lightweight catalog result; tracklists are fetched only on selection.
type SoundCloudSearchItem struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	URL        string `json:"url"`
	Duration   int64  `json:"duration"`
	TrackCount int    `json:"track_count"`
}

type SoundCloudSearchGroup struct {
	Type       string                 `json:"type"`
	Items      []SoundCloudSearchItem `json:"items"`
	NextOffset int                    `json:"next_offset"` // -1 means exhausted
	Error      string                 `json:"error,omitempty"`
}

func SoundCloudCandidateLimit(limit int) int {
	if limit <= 0 {
		return 30
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// SearchSoundCloud returns up to 30 results, ten of each type, with independent pagination.
func (m *FullReleaseMatcher) SearchSoundCloud(ctx context.Context, query string, offsets [3]int) ([]SoundCloudSearchGroup, error) {
	if m.scSearcher == nil {
		return nil, fmt.Errorf("SoundCloud search is unavailable")
	}
	return m.scSearcher.SearchCatalog(ctx, query, offsets)
}

func (s *SoundCloudSetSearcher) SearchCatalog(ctx context.Context, query string, offsets [3]int) ([]SoundCloudSearchGroup, error) {
	query = strings.TrimSpace(query)
	if query == "" || len([]rune(query)) > 200 {
		return nil, fmt.Errorf("search query must contain 1–200 characters")
	}
	for _, offset := range offsets {
		if offset < -1 || offset > 10000 {
			return nil, fmt.Errorf("invalid search offset")
		}
	}
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	clientID, _ := s.GetClientID(ctx)
	if clientID == "" {
		clientID = defaultSoundCloudClientID
	}
	types := []string{"track", "album", "playlist"}
	endpoints := []string{"search/tracks", "search/albums", "search/playlists_without_albums"}
	groups := make([]SoundCloudSearchGroup, 3)
	var wg sync.WaitGroup
	for i := range groups {
		groups[i] = SoundCloudSearchGroup{Type: types[i], Items: []SoundCloudSearchItem{}, NextOffset: -1}
		if offsets[i] == -1 {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := clientID
			page, err := s.querySearchPage(ctx, endpoints[i], query, id, 10, offsets[i])
			if err != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403")) {
				s.InvalidateClientID()
				id, _ = s.GetClientID(ctx)
				page, err = s.querySearchPage(ctx, endpoints[i], query, id, 10, offsets[i])
			}
			if err != nil {
				groups[i].Error = "Не удалось загрузить эту группу. Повторите поиск."
				groups[i].NextOffset = offsets[i] // retry without skipping failed results
				return
			}
			if len(page.Collection) > 0 && offsets[i]+len(page.Collection) <= 10000 && (page.NextHref != "" || len(page.Collection) >= 10) {
				groups[i].NextOffset = offsets[i] + len(page.Collection)
			}
			seen := make(map[string]bool)
			for _, item := range page.Collection {
				u, err := url.Parse(item.PermalinkURL)
				if err != nil || u.Scheme != "https" || u.Host != "soundcloud.com" || u.User != nil || item.Title == "" || seen[item.PermalinkURL] {
					continue
				}
				seen[item.PermalinkURL] = true
				groups[i].Items = append(groups[i].Items, SoundCloudSearchItem{
					ID: strconv.FormatInt(item.ID, 10), Title: item.Title, Artist: item.User.Username,
					URL: item.PermalinkURL, Duration: item.Duration / 1000, TrackCount: item.TrackCount,
				})
			}
		}(i)
	}
	wg.Wait()
	if parentCtx.Err() != nil {
		return nil, parentCtx.Err()
	}
	failed, requested := 0, 0
	for i, g := range groups {
		if offsets[i] >= 0 {
			requested++
			if g.Error != "" {
				failed++
			}
		}
	}
	if requested > 0 && failed == requested {
		return nil, fmt.Errorf("SoundCloud search is temporarily unavailable")
	}
	return groups, nil
}
