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

// SoundCloudSearchItem представляет легковесный элемент каталога без тяжелой предзагрузки треклистов.
type SoundCloudSearchItem struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Uploader   string `json:"uploader"`
	Verified   bool   `json:"verified"`
	URL        string `json:"url"`
	Duration   int64  `json:"duration"`
	TrackCount int    `json:"track_count"`
	ArtworkURL string `json:"artwork_url,omitempty"`
	AvatarURL  string `json:"avatar_url,omitempty"`
}

type SoundCloudSearchGroup struct {
	Type       string                 `json:"type"`
	Items      []SoundCloudSearchItem `json:"items"`
	NextOffset int                    `json:"next_offset"` // -1 means exhausted
	NextToken  string                 `json:"next_token,omitempty"`
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

// SearchSoundCloud возвращает до 30 результатов с независимой пагинацией по трем типам.
func (m *FullReleaseMatcher) SearchSoundCloud(ctx context.Context, query string, offsets [3]int) ([]SoundCloudSearchGroup, error) {
	if m.scSearcher == nil {
		return nil, fmt.Errorf("SoundCloud search is unavailable")
	}
	return m.scSearcher.SearchCatalog(ctx, query, offsets)
}

// SearchSoundCloudWithTokens возвращает результаты поиска с поддержкой безопасных токенов продолжения.
func (m *FullReleaseMatcher) SearchSoundCloudWithTokens(ctx context.Context, query string, offsets [3]int, tokens [3]string) ([]SoundCloudSearchGroup, error) {
	if m.scSearcher == nil {
		return nil, fmt.Errorf("SoundCloud search is unavailable")
	}
	return m.scSearcher.SearchCatalogWithTokens(ctx, query, offsets, tokens)
}

func (s *SoundCloudSetSearcher) SearchCatalog(ctx context.Context, query string, offsets [3]int) ([]SoundCloudSearchGroup, error) {
	return s.SearchCatalogWithTokens(ctx, query, offsets, [3]string{})
}

func (s *SoundCloudSetSearcher) SearchCatalogWithTokens(ctx context.Context, query string, offsets [3]int, tokens [3]string) ([]SoundCloudSearchGroup, error) {
	query = strings.TrimSpace(query)
	if query == "" || len([]rune(query)) > 200 {
		return nil, fmt.Errorf("search query must contain 1–200 characters")
	}
	for _, offset := range offsets {
		if offset < -1 || offset > 100000 {
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
	var groupErrors [3]error

	for i := range groups {
		groups[i] = SoundCloudSearchGroup{Type: types[i], Items: []SoundCloudSearchItem{}, NextOffset: -1}
		if offsets[i] == -1 && tokens[i] == "" {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			currentOffset := offsets[i]
			currentCursor := ""
			if tokens[i] != "" {
				parsedOff, parsedCur, tErr := s.parseContinuationToken(tokens[i], types[i], query)
				if tErr != nil {
					groupErrors[i] = tErr
					groups[i].Error = tErr.Error()
					return
				}
				currentOffset = parsedOff
				currentCursor = parsedCur
			}

			id := clientID
			page, err := s.querySearchPageDetailed(ctx, endpoints[i], query, id, 10, currentOffset, currentCursor)
			if err != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403")) {
				s.InvalidateClientID()
				id, _ = s.GetClientID(ctx)
				page, err = s.querySearchPageDetailed(ctx, endpoints[i], query, id, 10, currentOffset, currentCursor)
			}
			if err != nil {
				if strings.Contains(err.Error(), "429") {
					groups[i].Error = "SoundCloud ограничил частоту запросов (429). Подождите немного и повторите."
				} else {
					groups[i].Error = "Не удалось загрузить эту группу. Повторите поиск."
				}
				groups[i].NextOffset = currentOffset // retry without skipping failed results
				groups[i].NextToken = tokens[i]
				return
			}

			// Определение продолжения пагинации ДО локальной фильтрации результатов
			nextOff, nextCur, valid := s.parseAndValidateNextHref(page.NextHref, endpoints[i], query, currentOffset, currentCursor)
			if valid {
				groups[i].NextOffset = nextOff
				groups[i].NextToken = s.createContinuationToken(types[i], query, nextOff, nextCur)
			} else {
				if len(page.Collection) >= 10 && currentOffset+len(page.Collection) <= 100000 {
					groups[i].NextOffset = currentOffset + len(page.Collection)
				} else {
					groups[i].NextOffset = -1
				}
				groups[i].NextToken = ""
			}

			seen := make(map[string]bool)
			for _, item := range page.Collection {
				u, pErr := url.Parse(item.PermalinkURL)
				if pErr != nil || u.User != nil || item.Title == "" || seen[item.PermalinkURL] {
					continue
				}
				// Валидация хоста
				isValidHost := (u.Scheme == "https" && u.Host == "soundcloud.com")
				if !isValidHost && s.apiBaseURL != "" {
					baseU, _ := url.Parse(s.apiBaseURL)
					if u.Host == baseU.Host && (u.Scheme == baseU.Scheme || u.Scheme == "https") {
						isValidHost = true
					}
				}
				if !isValidHost {
					continue
				}

				seen[item.PermalinkURL] = true

				art, _ := ParseSoundCloudTitle(item.Title, item.User.Username)
				if art == "" || art == "Unknown Artist" {
					art = item.User.Username
				}

				groups[i].Items = append(groups[i].Items, SoundCloudSearchItem{
					ID:         strconv.FormatInt(item.ID, 10),
					Title:      item.Title,
					Artist:     art,
					Uploader:   item.User.Username,
					Verified:   item.User.Verified,
					URL:        item.PermalinkURL,
					Duration:   item.Duration / 1000,
					TrackCount: item.TrackCount,
					ArtworkURL: item.ArtworkURL,
					AvatarURL:  item.User.AvatarURL,
				})
			}
		}(i)
	}
	wg.Wait()
	if parentCtx.Err() != nil {
		return nil, parentCtx.Err()
	}

	for i := range groupErrors {
		if groupErrors[i] != nil {
			return nil, groupErrors[i]
		}
	}

	failed, requested := 0, 0
	for i, g := range groups {
		if offsets[i] >= 0 || tokens[i] != "" {
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
