package tageditor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
)

// GetCover извлекает фронтальную обложку (APIC type=3) из MP3 либо находит cover.jpg в каталоге.
func (s *Service) GetCover(ctx context.Context, relPath string) (*CoverInfo, error) {
	fullPath, isTmp, err := s.ResolvePath(relPath)
	if err != nil {
		return nil, err
	}

	st, err := safefs.Stat(fullPath)
	if err != nil {
		return nil, ErrFileNotFound
	}

	// 1. Попытка прочесть встроенный APIC фрейм из MP3
	if strings.EqualFold(filepath.Ext(fullPath), ".mp3") {
		cover, err := id3.ReadCover(fullPath)
		if err == nil && cover != nil && len(cover.Data) > 0 {
			return cover, nil
		}
	}

	// 2. Поиск внешнего графического файла рядом с треком или в папке
	var candidates []string
	if !st.IsDir() {
		trackBase := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))
		candidates = append(candidates,
			trackBase+".jpg", trackBase+".png", trackBase+".jpeg", trackBase+".webp",
		)
	}
	dir := fullPath
	if !st.IsDir() {
		dir = filepath.Dir(fullPath)
	}
	for _, cand := range []string{"cover.jpg", "cover.png", "folder.jpg", "folder.png", "cover.jpeg"} {
		candidates = append(candidates, filepath.Join(dir, cand))
	}

	var sb *sandbox.PathSandbox = s.musicSandbox
	if isTmp {
		sb = s.tmpSandbox
	}

	for _, candPath := range candidates {
		if sb != nil {
			relCand, err := sb.ToRelative(candPath)
			if err != nil {
				continue
			}
			resolvedPath, err := sb.Resolve(relCand, false)
			if err != nil {
				continue
			}
			candPath = resolvedPath
		}
		if data, err := safefs.ReadFile(candPath); err == nil {
			mime := "image/jpeg"
			if strings.HasSuffix(candPath, ".png") {
				mime = "image/png"
			} else if strings.HasSuffix(candPath, ".webp") {
				mime = "image/webp"
			}
			return &CoverInfo{
				MimeType: mime,
				Data:     data,
			}, nil
		}
	}

	return nil, ErrCoverNotFound
}

// SearchCovers выполняет параллельный мультисервисный поиск обложек высокого разрешения:
// Apple Music (1400x1400), Deezer (1400x1400), Яндекс Музыка (1000x1000), MusicBrainz (1000x1000).
func (s *Service) SearchCovers(ctx context.Context, query string, limit int) ([]CoverSearchResultItem, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return []CoverSearchResultItem{}, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 24
	}

	client := s.httpClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	perSourceLimit := limit / 2
	if perSourceLimit < 6 {
		perSourceLimit = 6
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results = make(map[string][]CoverSearchResultItem)
	)

	addResults := func(src string, items []CoverSearchResultItem) {
		if len(items) == 0 {
			return
		}
		mu.Lock()
		results[src] = items
		mu.Unlock()
	}

	// 1. Apple Music / iTunes Search API
	wg.Add(1)
	go func() {
		defer wg.Done()
		items := s.searchAppleCovers(ctx, client, q, perSourceLimit)
		addResults("apple", items)
	}()

	// 2. Deezer API
	wg.Add(1)
	go func() {
		defer wg.Done()
		items := s.searchDeezerCovers(ctx, client, q, perSourceLimit)
		addResults("deezer", items)
	}()

	// 3. Яндекс Музыка API
	wg.Add(1)
	go func() {
		defer wg.Done()
		items := s.searchYandexCovers(ctx, client, q, perSourceLimit)
		addResults("yandex", items)
	}()

	// 4. MusicBrainz / Cover Art Archive
	wg.Add(1)
	go func() {
		defer wg.Done()
		items := s.searchMusicBrainzCovers(ctx, client, q, perSourceLimit)
		addResults("musicbrainz", items)
	}()

	wg.Wait()

	// Перемежение (round-robin) результатов для максимального разнообразия первых карточек
	sourceOrder := []string{"apple", "deezer", "yandex", "musicbrainz"}
	maxLen := 0
	for _, src := range sourceOrder {
		if l := len(results[src]); l > maxLen {
			maxLen = l
		}
	}

	merged := make([]CoverSearchResultItem, 0, limit)
	seen := make(map[string]bool)

	for i := 0; i < maxLen; i++ {
		for _, src := range sourceOrder {
			list := results[src]
			if i < len(list) {
				item := list[i]
				cleanURL := strings.TrimSpace(item.CoverURL)
				if cleanURL != "" && !seen[cleanURL] {
					seen[cleanURL] = true
					merged = append(merged, item)
					if len(merged) >= limit {
						break
					}
				}
			}
		}
		if len(merged) >= limit {
			break
		}
	}

	return merged, nil
}

func (s *Service) searchAppleCovers(ctx context.Context, client *http.Client, query string, limit int) []CoverSearchResultItem {
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	u := fmt.Sprintf("https://itunes.apple.com/search?term=%s&entity=album&limit=%d", url.QueryEscape(query), limit)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var data struct {
		Results []struct {
			CollectionName string `json:"collectionName"`
			TrackName      string `json:"trackName"`
			ArtistName     string `json:"artistName"`
			ArtworkURL100  string `json:"artworkUrl100"`
			ReleaseDate    string `json:"releaseDate"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}

	reArt := regexp.MustCompile(`\d+x\d+bb(?:\.jpg|\.png)?`)
	items := make([]CoverSearchResultItem, 0, len(data.Results))
	for _, it := range data.Results {
		if it.ArtworkURL100 == "" {
			continue
		}
		coverURL := reArt.ReplaceAllString(it.ArtworkURL100, "1400x1400bb.jpg")
		if !strings.HasSuffix(coverURL, ".jpg") && !strings.HasSuffix(coverURL, ".png") && !strings.HasSuffix(coverURL, ".jpeg") {
			coverURL += ".jpg"
		}

		title := it.CollectionName
		if title == "" {
			title = it.TrackName
		}
		var pYear *int
		if len(it.ReleaseDate) >= 4 {
			if yr, err := strconv.Atoi(it.ReleaseDate[:4]); err == nil && yr > 0 {
				pYear = &yr
			}
		}

		items = append(items, CoverSearchResultItem{
			Source:   "apple",
			Title:    title,
			Artist:   it.ArtistName,
			CoverURL: coverURL,
			Width:    1400,
			Height:   1400,
			Year:     pYear,
		})
	}
	return items
}

func (s *Service) searchDeezerCovers(ctx context.Context, client *http.Client, query string, limit int) []CoverSearchResultItem {
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	if s.deezer != nil {
		albums, err := s.deezer.SearchAlbums(reqCtx, query)
		if err == nil && len(albums) > 0 {
			items := make([]CoverSearchResultItem, 0, len(albums))
			for _, a := range albums {
				cover := a.BestCoverURL()
				if cover == "" {
					continue
				}
				year := a.ReleaseYear()
				var pYear *int
				if year > 0 {
					pYear = &year
				}
				items = append(items, CoverSearchResultItem{
					Source:   "deezer",
					Title:    a.Title,
					Artist:   a.Artist.Name,
					CoverURL: cover,
					Width:    1400,
					Height:   1400,
					Year:     pYear,
				})
				if len(items) >= limit {
					break
				}
			}
			return items
		}
	}

	u := fmt.Sprintf("https://api.deezer.com/search/album?q=%s&limit=%d", url.QueryEscape(query), limit)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var data struct {
		Data []struct {
			Title  string `json:"title"`
			Artist struct {
				Name string `json:"name"`
			} `json:"artist"`
			CoverXL     string `json:"cover_xl"`
			CoverBig    string `json:"cover_big"`
			CoverMedium string `json:"cover_medium"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}

	items := make([]CoverSearchResultItem, 0, len(data.Data))
	for _, a := range data.Data {
		cov := a.CoverXL
		if cov == "" {
			cov = a.CoverBig
		}
		if cov == "" {
			cov = a.CoverMedium
		}
		if cov == "" {
			continue
		}
		items = append(items, CoverSearchResultItem{
			Source:   "deezer",
			Title:    a.Title,
			Artist:   a.Artist.Name,
			CoverURL: cov,
			Width:    1400,
			Height:   1400,
		})
	}
	return items
}

func (s *Service) searchYandexCovers(ctx context.Context, client *http.Client, query string, limit int) []CoverSearchResultItem {
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	u := fmt.Sprintf("https://api.music.yandex.net/search?text=%s&type=album&page=0", url.QueryEscape(query))
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var data struct {
		Result struct {
			Albums struct {
				Results []struct {
					Title    string `json:"title"`
					Year     int    `json:"year"`
					CoverURI string `json:"coverUri"`
					Artists  []struct {
						Name string `json:"name"`
					} `json:"artists"`
				} `json:"results"`
			} `json:"albums"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}

	items := make([]CoverSearchResultItem, 0, len(data.Result.Albums.Results))
	for _, a := range data.Result.Albums.Results {
		if a.CoverURI == "" {
			continue
		}
		cov := strings.Replace(a.CoverURI, "%%", "1000x1000", 1)
		if !strings.HasPrefix(cov, "http://") && !strings.HasPrefix(cov, "https://") {
			cov = "https://" + cov
		}
		artist := ""
		if len(a.Artists) > 0 {
			artist = a.Artists[0].Name
		}
		var pYear *int
		if a.Year > 0 {
			yr := a.Year
			pYear = &yr
		}
		items = append(items, CoverSearchResultItem{
			Source:   "yandex",
			Title:    a.Title,
			Artist:   artist,
			CoverURL: cov,
			Width:    1000,
			Height:   1000,
			Year:     pYear,
		})
		if len(items) >= limit {
			break
		}
	}
	return items
}

func (s *Service) searchMusicBrainzCovers(ctx context.Context, client *http.Client, query string, limit int) []CoverSearchResultItem {
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	lucene := fmt.Sprintf(`release:"%s" OR artist:"%s" OR "%s"`, query, query, query)
	u := fmt.Sprintf("https://musicbrainz.org/ws/2/release/?query=%s&limit=%d&fmt=json", url.QueryEscape(lucene), limit)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "MusicManager/1.0.0 (https://github.com/kex1tu/music-manager)")

	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var data struct {
		Releases []struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			Date         string `json:"date"`
			ArtistCredit []struct {
				Name       string `json:"name"`
				JoinPhrase string `json:"joinphrase"`
			} `json:"artist-credit"`
		} `json:"releases"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}

	items := make([]CoverSearchResultItem, 0, len(data.Releases))
	for _, r := range data.Releases {
		if r.ID == "" {
			continue
		}
		artist := ""
		for _, ac := range r.ArtistCredit {
			artist += ac.Name + ac.JoinPhrase
		}
		var pYear *int
		if len(r.Date) >= 4 {
			if yr, err := strconv.Atoi(r.Date[:4]); err == nil && yr > 0 {
				pYear = &yr
			}
		}
		items = append(items, CoverSearchResultItem{
			Source:   "musicbrainz",
			Title:    r.Title,
			Artist:   strings.TrimSpace(artist),
			CoverURL: fmt.Sprintf("https://coverartarchive.org/release/%s/front-500", r.ID),
			Width:    1000,
			Height:   1000,
			Year:     pYear,
		})
	}
	return items
}

func (s *Service) downloadImage(ctx context.Context, rawURL string) ([]byte, error) {
	client := s.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download cover status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
}
