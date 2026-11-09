package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

type coverSearchResultItem struct {
	Source   string `json:"source"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	CoverURL string `json:"cover_url"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Year     *int   `json:"year,omitempty"`
}

func (h *TagHandler) GetCover(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}

	fullPath, err := h.resolvePath(relPath)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	st, err := safefs.Stat(fullPath)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	// 1. Try reading embedded APIC from MP3
	if strings.EqualFold(filepath.Ext(fullPath), ".mp3") {
		cover, err := id3.ReadCover(fullPath)
		if err == nil && cover != nil && len(cover.Data) > 0 {
			w.Header().Set("Content-Type", cover.MimeType)
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = w.Write(cover.Data)
			return
		}
	}

	// 2. Check for track-specific cover or cover.jpg / folder.jpg in directory
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

	for _, candPath := range candidates {
		if data, err := safefs.ReadFile(candPath); err == nil {
			mime := "image/jpeg"
			if strings.HasSuffix(candPath, ".png") {
				mime = "image/png"
			} else if strings.HasSuffix(candPath, ".webp") {
				mime = "image/webp"
			}
			w.Header().Set("Content-Type", mime)
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = w.Write(data)
			return
		}
	}

	http.Error(w, "cover art not found", http.StatusNotFound)
}

func (h *TagHandler) SearchCovers(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]coverSearchResultItem{})
		return
	}

	limit := 24
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}
	perSourceLimit := limit / 2
	if perSourceLimit < 6 {
		perSourceLimit = 6
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results = make(map[string][]coverSearchResultItem)
	)

	addResults := func(src string, items []coverSearchResultItem) {
		if len(items) == 0 {
			return
		}
		mu.Lock()
		results[src] = items
		mu.Unlock()
	}

	// 1. Apple Music
	wg.Add(1)
	go func() {
		defer wg.Done()
		items := searchAppleCoversHandler(r.Context(), client, query, perSourceLimit)
		addResults("apple", items)
	}()

	// 2. Deezer API
	wg.Add(1)
	go func() {
		defer wg.Done()
		items := searchDeezerCoversHandler(r.Context(), client, h.deezer, query, perSourceLimit)
		addResults("deezer", items)
	}()

	// 3. Yandex Music
	wg.Add(1)
	go func() {
		defer wg.Done()
		items := searchYandexCoversHandler(r.Context(), client, query, perSourceLimit)
		addResults("yandex", items)
	}()

	// 4. MusicBrainz
	wg.Add(1)
	go func() {
		defer wg.Done()
		items := searchMusicBrainzCoversHandler(r.Context(), client, query, perSourceLimit)
		addResults("musicbrainz", items)
	}()

	wg.Wait()

	sourceOrder := []string{"apple", "deezer", "yandex", "musicbrainz"}
	maxLen := 0
	for _, src := range sourceOrder {
		if l := len(results[src]); l > maxLen {
			maxLen = l
		}
	}

	merged := make([]coverSearchResultItem, 0, limit)
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

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(merged)
}

func searchAppleCoversHandler(ctx context.Context, client *http.Client, query string, limit int) []coverSearchResultItem {
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
	items := make([]coverSearchResultItem, 0, len(data.Results))
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

		items = append(items, coverSearchResultItem{
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

func searchDeezerCoversHandler(ctx context.Context, client *http.Client, deezerClient *resolver.DeezerClient, query string, limit int) []coverSearchResultItem {
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	if deezerClient != nil {
		albums, err := deezerClient.SearchAlbums(reqCtx, query)
		if err == nil && len(albums) > 0 {
			items := make([]coverSearchResultItem, 0, len(albums))
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
				items = append(items, coverSearchResultItem{
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

	items := make([]coverSearchResultItem, 0, len(data.Data))
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
		items = append(items, coverSearchResultItem{
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

func searchYandexCoversHandler(ctx context.Context, client *http.Client, query string, limit int) []coverSearchResultItem {
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

	items := make([]coverSearchResultItem, 0, len(data.Result.Albums.Results))
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
		items = append(items, coverSearchResultItem{
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

func searchMusicBrainzCoversHandler(ctx context.Context, client *http.Client, query string, limit int) []coverSearchResultItem {
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

	items := make([]coverSearchResultItem, 0, len(data.Releases))
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
		items = append(items, coverSearchResultItem{
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

func fileExists(path string) bool {
	st, err := safefs.Stat(path)
	return err == nil && !st.IsDir()
}
