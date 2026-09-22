package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"music-manager/internal/id3"
)

// FeatEnricher выполняет многоуровневую детекцию и сверку соавторов (фитов).
type FeatEnricher struct {
	yandexClient  *YandexMusicClient
	ytmusicClient *YouTubeMusicClient
	httpClient    *http.Client
}

// NewFeatEnricher создает новый экземпляр FeatEnricher.
func NewFeatEnricher(yandex *YandexMusicClient, ytmusic *YouTubeMusicClient) *FeatEnricher {
	return &FeatEnricher{
		yandexClient:  yandex,
		ytmusicClient: ytmusic,
		httpClient:    &http.Client{Timeout: 6 * time.Second},
	}
}

// cleanArtistList очищает имена исполнителей от служебных слов, Topic-суффиксов и дубликатов.
func cleanArtistList(artists []string) []string {
	var res []string
	seen := make(map[string]bool)
	for _, a := range artists {
		t := strings.TrimSpace(a)
		t = strings.TrimSuffix(t, "- Topic")
		t = strings.TrimSuffix(t, "-Topic")
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		lower := strings.ToLower(t)
		if lower == "feat" || lower == "ft" || lower == "featuring" || lower == "with" {
			continue
		}
		if !seen[lower] {
			seen[lower] = true
			res = append(res, t)
		}
	}
	return res
}

// containsEquivalentArtist проверяет, входит ли артист в список с учетом эквивалентности имен.
func containsEquivalentArtist(target string, list []string) bool {
	for _, a := range list {
		if AreArtistsEquivalent(a, target) {
			return true
		}
	}
	return false
}

// EnrichTrackArtists принимает название песни и текущего исполнителя (или список),
// сверяет их с локальным текстом, Яндекс Музыкой и YouTube Music по правилу максимального количества
// подтвержденных соавторов ("где больше указано артистов, то и выбирается").
func (e *FeatEnricher) EnrichTrackArtists(ctx context.Context, title string, currentArtists []string) []string {
	var localArtists []string

	// 1. Извлекаем соавторов из текущих тегов (разбирая multi-value если была строка "A\\B" или фиты)
	for _, a := range currentArtists {
		for _, part := range id3.ParseMultiValue(a) {
			cleanPart, feats := ExtractFeatFromText(part)
			if cleanPart != "" {
				localArtists = append(localArtists, cleanPart)
			}
			localArtists = append(localArtists, feats...)
		}
	}

	// 2. Извлекаем соавторов из названия трека (например, "Песня (feat. Рем Дигга)")
	_, titleFeats := ExtractFeatFromText(title)
	localArtists = append(localArtists, titleFeats...)
	localArtists = cleanArtistList(localArtists)

	primaryArt := ""
	if len(localArtists) > 0 {
		primaryArt = localArtists[0]
	} else if len(currentArtists) > 0 {
		primaryArt = strings.TrimSpace(currentArtists[0])
	}

	cleanTit := CleanYouTubeVideoTitle(title)
	query := cleanTit
	if primaryArt != "" {
		query = fmt.Sprintf("%s %s", ExtractPrimaryArtist(primaryArt), cleanTit)
	}
	query = strings.TrimSpace(query)

	var yaArtists []string
	var ytmArtists []string

	// Параллельный опрос Яндекс Музыки и YouTube Music с таймаутом
	if query != "" && (e.yandexClient != nil || e.ytmusicClient != nil) {
		lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()

		var wg sync.WaitGroup
		var mu sync.Mutex

		// Запрос к Яндекс Музыке
		if e.yandexClient != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				yaTracks, err := e.yandexClient.SearchTrack(lookupCtx, query)
				if err == nil && len(yaTracks) > 0 {
					for _, cand := range yaTracks {
						candArts := cand.AllArtistNames()
						if len(candArts) > 0 {
							// Проверяем соответствие главного артиста
							if primaryArt == "" || len(cand.Artists) == 0 || AreArtistsEquivalent(cand.Artists[0].Name, primaryArt) || containsEquivalentArtist(primaryArt, candArts) {
								mu.Lock()
								yaArtists = candArts
								mu.Unlock()
								break
							}
						}
					}
				}
			}()
		}

		// Запрос к YouTube Music
		if e.ytmusicClient != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ytmTracks, err := e.ytmusicClient.Search(lookupCtx, query)
				if err == nil && len(ytmTracks) > 0 {
					for _, cand := range ytmTracks {
						if len(cand.Artists) > 0 {
							// Проверяем соответствие главного артиста
							if primaryArt == "" || AreArtistsEquivalent(cand.Artists[0], primaryArt) || containsEquivalentArtist(primaryArt, cand.Artists) {
								mu.Lock()
								ytmArtists = cand.Artists
								mu.Unlock()
								break
							}
						}
					}
				}
			}()
		}

		wg.Wait()
	}

	yaArtists = cleanArtistList(yaArtists)
	ytmArtists = cleanArtistList(ytmArtists)

	// Выбираем источник с максимальным количеством соавторов ("где больше указано артистов то и указывалось")
	best := localArtists

	if len(yaArtists) > len(best) {
		best = yaArtists
	}

	if len(ytmArtists) > len(best) {
		best = ytmArtists
	} else if len(ytmArtists) == len(yaArtists) && len(ytmArtists) > len(best) {
		// При одинаковом количестве у Yandex и YTM объединяем уникальных неэквивалентных артистов
		merged := yaArtists
		for _, a := range ytmArtists {
			if !containsEquivalentArtist(a, merged) {
				merged = append(merged, a)
			}
		}
		best = cleanArtistList(merged)
	}

	// Если ни один сервис не нашел фитов (остался 1 артист), проверяем iTunes Search API
	if len(best) <= 1 && e.httpClient != nil && primaryArt != "" {
		itCtx, itCancel := context.WithTimeout(ctx, 2*time.Second)
		defer itCancel()
		itunesFeats := e.searchItunesFeats(itCtx, primaryArt, cleanTit)
		for _, f := range itunesFeats {
			if !containsEquivalentArtist(f, best) {
				best = append(best, f)
			}
		}
		best = cleanArtistList(best)
	}

	if len(best) == 0 && len(currentArtists) > 0 {
		return currentArtists
	}
	return best
}

// searchItunesFeats ищет трек в iTunes Search API и извлекает соавторов из trackName
func (e *FeatEnricher) searchItunesFeats(ctx context.Context, artist, title string) []string {
	itunesURL := fmt.Sprintf("https://itunes.apple.com/search?term=%s&entity=song&limit=3", url.QueryEscape(artist+" "+title))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, itunesURL, nil)
	if err != nil {
		return nil
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var res struct {
		Results []struct {
			TrackName  string `json:"trackName"`
			ArtistName string `json:"artistName"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil
	}

	for _, item := range res.Results {
		if AreArtistsEquivalent(item.ArtistName, artist) {
			_, feats := ExtractFeatFromText(item.TrackName)
			if len(feats) > 0 {
				return feats
			}
		}
	}

	return nil
}
