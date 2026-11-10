package resolver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"

	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"music-manager/internal/core/mediaexec"
)

const defaultSoundCloudClientID = "xk6BGxLCsQJ5Ss0bH6sRdsZhts3gTNaU"

var (
	scriptSrcRegex = regexp.MustCompile(`<script[^>]+src="([^"]+)"`)
	clientIDRegex  = regexp.MustCompile(`client_id[:=]\s*"([0-9a-zA-Z]{32})"`)
)

type scAPIUser struct {
	Username  string `json:"username"`
	Permalink string `json:"permalink"`
}

type scAPITrackItem struct {
	ID           int64     `json:"id"`
	Title        string    `json:"title"`
	Duration     int64     `json:"duration"` // in milliseconds
	PermalinkURL string    `json:"permalink_url"`
	User         scAPIUser `json:"user"`
}

type scAPIAlbumItem struct {
	ResultType   string           `json:"-"`
	Duration     int64            `json:"duration"`
	ID           int64            `json:"id"`
	Title        string           `json:"title"`
	PermalinkURL string           `json:"permalink_url"`
	ArtworkURL   string           `json:"artwork_url"`
	TrackCount   int              `json:"track_count"`
	User         scAPIUser        `json:"user"`
	Tracks       []scAPITrackItem `json:"tracks"`
}

type scAPISearchResponse struct {
	Collection   []scAPIAlbumItem `json:"collection"`
	TotalResults int              `json:"total_results"`
	NextHref     string           `json:"next_href"`
}

// SoundCloudSetSearcher осуществляет поиск и разбор плейлистов/сетов SoundCloud и других внешних ссылок.
type SoundCloudSetSearcher struct {
	cookiesPath  string
	searchFunc   func(ctx context.Context, artist, album string, limit int) ([]AlbumReleaseCandidate, error)
	parseURLFunc func(ctx context.Context, setURL string) (*AlbumReleaseCandidate, error)

	httpClient  *http.Client
	apiBaseURL  string
	webBaseURL  string
	clientID    string
	clientIDMu  sync.RWMutex
	clientIDExp time.Time
}

// NewSoundCloudSetSearcher создает новый экземпляр SoundCloudSetSearcher.
func NewSoundCloudSetSearcher(cookiesPath string) *SoundCloudSetSearcher {
	return &SoundCloudSetSearcher{
		cookiesPath: cookiesPath,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		apiBaseURL: "https://api-v2.soundcloud.com",
		webBaseURL: "https://soundcloud.com",
		clientID:   defaultSoundCloudClientID,
	}
}

// SetCustomHTTPClient позволяет настроить HTTP клиент (для тестов или прокси).
func (s *SoundCloudSetSearcher) SetCustomHTTPClient(c *http.Client) {
	if c != nil {
		s.httpClient = c
	}
}

// SetBaseURLs позволяет переопределить базовые URL для тестов.
func (s *SoundCloudSetSearcher) SetBaseURLs(apiBaseURL, webBaseURL string) {
	if apiBaseURL != "" {
		s.apiBaseURL = strings.TrimRight(apiBaseURL, "/")
	}
	if webBaseURL != "" {
		s.webBaseURL = strings.TrimRight(webBaseURL, "/")
	}
}

// InvalidateClientID инвалидирует кэшированный client_id.
func (s *SoundCloudSetSearcher) InvalidateClientID() {
	s.clientIDMu.Lock()
	defer s.clientIDMu.Unlock()
	s.clientID = ""
	s.clientIDExp = time.Time{}
}

// GetClientID возвращает валидный client_id, извлекая его динамически или возвращая кэшированный/дефолтный.
func (s *SoundCloudSetSearcher) GetClientID(ctx context.Context) (string, error) {
	s.clientIDMu.RLock()
	if s.clientID != "" && time.Now().Before(s.clientIDExp) {
		cid := s.clientID
		s.clientIDMu.RUnlock()
		return cid, nil
	}
	s.clientIDMu.RUnlock()

	s.clientIDMu.Lock()
	defer s.clientIDMu.Unlock()

	if s.clientID != "" && time.Now().Before(s.clientIDExp) {
		return s.clientID, nil
	}

	cid, err := s.resolveClientID(ctx)
	if err != nil || cid == "" {
		s.clientID = defaultSoundCloudClientID
		s.clientIDExp = time.Now().Add(1 * time.Hour)
		return s.clientID, nil
	}

	s.clientID = cid
	s.clientIDExp = time.Now().Add(12 * time.Hour)
	return s.clientID, nil
}

func (s *SoundCloudSetSearcher) resolveClientID(ctx context.Context) (string, error) {
	webURL := s.webBaseURL
	if webURL == "" {
		webURL = "https://soundcloud.com"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, webURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	matches := scriptSrcRegex.FindAllStringSubmatch(string(bodyBytes), -1)
	var scriptURLs []string
	for _, m := range matches {
		if len(m) > 1 && strings.HasSuffix(m[1], ".js") {
			scriptURLs = append(scriptURLs, m[1])
		}
	}

	count := 0
	for i := len(scriptURLs) - 1; i >= 0 && count < 8; i-- {
		sURL := scriptURLs[i]
		if !strings.HasPrefix(sURL, "http") && strings.HasPrefix(sURL, "/") {
			sURL = webURL + sURL
		}
		count++

		sReq, err := http.NewRequestWithContext(ctx, http.MethodGet, sURL, nil)
		if err != nil {
			continue
		}
		sReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")

		sResp, err := s.httpClient.Do(sReq)
		if err != nil {
			continue
		}
		jsBytes, err := io.ReadAll(sResp.Body)
		sResp.Body.Close()
		if err != nil {
			continue
		}

		cMatch := clientIDRegex.FindStringSubmatch(string(jsBytes))
		if len(cMatch) > 1 && len(cMatch[1]) == 32 {
			return cMatch[1], nil
		}
	}

	return "", fmt.Errorf("client_id не найден в скриптах SoundCloud")
}

// SetCustomSearchFunc позволяет переопределить функцию поиска сетов для тестов.
func (s *SoundCloudSetSearcher) SetCustomSearchFunc(fn func(ctx context.Context, artist, album string, limit int) ([]AlbumReleaseCandidate, error)) {
	s.searchFunc = fn
}

// SetCustomParseURLFunc позволяет переопределить функцию разбора URL для тестов.
func (s *SoundCloudSetSearcher) SetCustomParseURLFunc(fn func(ctx context.Context, setURL string) (*AlbumReleaseCandidate, error)) {
	s.parseURLFunc = fn
}

// rawEntry представляет промежуточный JSON-объект, возвращаемый yt-dlp --dump-json.
type rawSetEntry struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Uploader      string  `json:"uploader"`
	Channel       string  `json:"channel"`
	Duration      float64 `json:"duration"`
	WebpageURL    string  `json:"webpage_url"`
	URL           string  `json:"url"`
	PlaylistTitle string  `json:"playlist_title"`
	Playlist      string  `json:"playlist"`
	Thumbnails    []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
}

// ParseOutputBytes парсит вывод yt-dlp и конструирует AlbumReleaseCandidate.
func (s *SoundCloudSetSearcher) ParseOutputBytes(data []byte, sourceURL string) (*AlbumReleaseCandidate, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var entries []rawSetEntry
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var e rawSetEntry
		if err := json.Unmarshal(line, &e); err == nil && (e.ID != "" || e.URL != "" || e.WebpageURL != "") {
			entries = append(entries, e)
		}
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("не найдено треков в выводе yt-dlp для %s", sourceURL)
	}

	isSoundCloud := strings.Contains(sourceURL, "soundcloud.com")
	playlistTitle := entries[0].PlaylistTitle
	if playlistTitle == "" {
		playlistTitle = entries[0].Playlist
	}

	firstArtist := ""
	coverURL := ""
	if len(entries[0].Thumbnails) > 0 {
		coverURL = entries[0].Thumbnails[len(entries[0].Thumbnails)-1].URL
	}

	var tracks []AlbumTrackCandidate
	for idx, e := range entries {
		var art, tit string
		if isSoundCloud {
			art, tit = ParseSoundCloudTitle(e.Title, e.Uploader)
		} else {
			parsed := ParseYouTubeTitle(e.Title, e.Channel, e.Uploader)
			art = parsed.Artist
			tit = parsed.Title
		}

		if firstArtist == "" && art != "" && art != "Unknown Artist" {
			firstArtist = art
		}

		durSec := int(math.Round(e.Duration))
		directURL := e.WebpageURL
		if directURL == "" {
			directURL = e.URL
		}
		if directURL == "" && e.ID != "" && strings.Contains(sourceURL, "youtube") {
			directURL = fmt.Sprintf("https://music.youtube.com/watch?v=%s", e.ID)
		}

		tracks = append(tracks, AlbumTrackCandidate{
			VideoID:   e.ID,
			DirectURL: directURL,
			Title:     tit,
			Artist:    art,
			Duration:  durSec,
			Index:     idx + 1,
		})
	}

	if playlistTitle == "" {
		if len(tracks) > 0 {
			playlistTitle = tracks[0].Title
		} else {
			playlistTitle = "SoundCloud Set"
		}
	}

	return &AlbumReleaseCandidate{
		BrowseID:    sourceURL,
		PlaylistID:  sourceURL,
		Title:       playlistTitle,
		Artist:      firstArtist,
		CoverURL:    coverURL,
		Tracks:      tracks,
		TotalTracks: len(tracks),
	}, nil
}

// ParseSetURL извлекает треклист и метаданные сета/плейлиста по прямой ссылке.
func (s *SoundCloudSetSearcher) ParseSetURL(ctx context.Context, setURL string) (*AlbumReleaseCandidate, error) {
	if s.parseURLFunc != nil {
		return s.parseURLFunc(ctx, setURL)
	}

	cleanURL := strings.TrimSpace(setURL)
	if cleanURL == "" {
		return nil, fmt.Errorf("пустой URL")
	}

	args := []string{
		"--ignore-config",
		"--no-download-archive",
		"--dump-json",
		"--flat-playlist",
		"--no-warnings",
		"--ignore-errors",
	}
	if IsValidNetscapeCookiesFile(s.cookiesPath) {
		args = append(args, "--cookies", s.cookiesPath)
	}
	args = append(args, cleanURL)

	timeout := 45 * time.Second
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := mediaexec.CommandContext(cmdCtx, "yt-dlp", args...)
	defer cmd.Close()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil && stdout.Len() == 0 {
		cleanErr := CleanYtDlpStderr(stderr.String())
		return nil, fmt.Errorf("ошибка парсинга сета yt-dlp: %w: %s", err, cleanErr)
	}

	return s.ParseOutputBytes(stdout.Bytes(), cleanURL)
}

func (s *SoundCloudSetSearcher) querySetsEndpoint(ctx context.Context, endpoint, query, clientID string, limit int) ([]scAPIAlbumItem, error) {
	page, err := s.querySearchPage(ctx, endpoint, query, clientID, limit, 0)
	return page.Collection, err
}

func (s *SoundCloudSetSearcher) querySearchPage(ctx context.Context, endpoint, query, clientID string, limit, offset int) (scAPISearchResponse, error) {
	var root scAPISearchResponse
	baseURL := s.apiBaseURL
	if baseURL == "" {
		baseURL = "https://api-v2.soundcloud.com"
	}

	reqURL := fmt.Sprintf("%s/%s?q=%s&client_id=%s&limit=%d&offset=%d&linked_partitioning=1",
		baseURL,
		endpoint,
		url.QueryEscape(query),
		url.QueryEscape(clientID),
		limit,
		offset,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return root, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return root, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return root, fmt.Errorf("SoundCloud API error: status %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(&root); err != nil {
		return root, err
	}

	return root, nil
}

// HydrateTracks запрашивает полную информацию о треках по их ID через батчевый эндпоинт SoundCloud API /tracks?ids=...
func (s *SoundCloudSetSearcher) HydrateTracks(ctx context.Context, clientID string, trackIDs []int64) (map[int64]scAPITrackItem, error) {
	if len(trackIDs) == 0 {
		return nil, nil
	}

	baseURL := s.apiBaseURL
	if baseURL == "" {
		baseURL = "https://api-v2.soundcloud.com"
	}

	result := make(map[int64]scAPITrackItem)

	chunkSize := 50
	for i := 0; i < len(trackIDs); i += chunkSize {
		end := i + chunkSize
		if end > len(trackIDs) {
			end = len(trackIDs)
		}
		chunk := trackIDs[i:end]

		idStrs := make([]string, len(chunk))
		for idx, id := range chunk {
			idStrs[idx] = strconv.FormatInt(id, 10)
		}

		reqURL := fmt.Sprintf("%s/tracks?ids=%s&client_id=%s",
			baseURL,
			url.QueryEscape(strings.Join(idStrs, ",")),
			url.QueryEscape(clientID),
		)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "application/json")

		resp, err := s.httpClient.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode == http.StatusOK {
			var tracks []scAPITrackItem
			if err := json.NewDecoder(resp.Body).Decode(&tracks); err == nil {
				for _, tr := range tracks {
					if tr.ID > 0 {
						result[tr.ID] = tr
					}
				}
			}
		}
		resp.Body.Close()
	}

	return result, nil
}

func (s *SoundCloudSetSearcher) convertAlbumToCandidate(album scAPIAlbumItem, hydrated map[int64]scAPITrackItem) AlbumReleaseCandidate {
	coverURL := album.ArtworkURL
	if coverURL != "" {
		coverURL = strings.Replace(coverURL, "-large.jpg", "-t500x500.jpg", 1)
	}

	uploader := album.User.Username
	firstArtist := ""
	var tracks []AlbumTrackCandidate

	for idx, tr := range album.Tracks {
		fullTrack := tr
		if fullTrack.Title == "" && hydrated != nil {
			if h, ok := hydrated[fullTrack.ID]; ok {
				fullTrack = h
			}
		}

		if fullTrack.Title == "" {
			continue
		}

		art, tit := ParseSoundCloudTitle(fullTrack.Title, uploader)
		if firstArtist == "" && art != "" && art != "Unknown Artist" {
			firstArtist = art
		}

		durSec := int(math.Round(float64(fullTrack.Duration) / 1000.0))
		directURL := fullTrack.PermalinkURL

		tracks = append(tracks, AlbumTrackCandidate{
			VideoID:   strconv.FormatInt(fullTrack.ID, 10),
			DirectURL: directURL,
			Title:     tit,
			Artist:    art,
			Duration:  durSec,
			Index:     idx + 1,
		})
	}

	if firstArtist == "" {
		firstArtist = uploader
	}

	albumTitle := album.Title
	if albumTitle == "" && len(tracks) > 0 {
		albumTitle = tracks[0].Title
	}

	return AlbumReleaseCandidate{
		ResultType:  album.ResultType,
		BrowseID:    fmt.Sprintf("sc_%d", album.ID),
		PlaylistID:  album.PermalinkURL,
		Title:       albumTitle,
		Artist:      firstArtist,
		CoverURL:    coverURL,
		Tracks:      tracks,
		TotalTracks: len(tracks),
	}
}

// SearchSetsAPI выполняет прямой поиск альбомов и сетов через SoundCloud API v2.
func (s *SoundCloudSetSearcher) SearchSetsAPI(ctx context.Context, query string, limit int) ([]AlbumReleaseCandidate, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	limit = SoundCloudCandidateLimit(limit)

	clientID, err := s.GetClientID(ctx)
	if err != nil || clientID == "" {
		clientID = defaultSoundCloudClientID
	}

	search := func(endpoint, resultType string) ([]scAPIAlbumItem, error) {
		items, err := s.querySetsEndpoint(ctx, endpoint, query, clientID, limit)
		if err != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403")) {
			s.InvalidateClientID()
			if newID, nErr := s.GetClientID(ctx); nErr == nil && newID != "" {
				clientID = newID
				items, err = s.querySetsEndpoint(ctx, endpoint, query, clientID, limit)
			}
		}
		for i := range items {
			items[i].ResultType = resultType
		}
		return items, err
	}
	albums, albumErr := search("search/albums", "album")
	playlists, playlistErr := search("search/playlists_without_albums", "playlist")
	if albumErr != nil && playlistErr != nil {
		return nil, fmt.Errorf("SoundCloud search failed: albums: %v; playlists: %w", albumErr, playlistErr)
	}

	var allItems []scAPIAlbumItem
	seenURLs := make(map[string]bool)

	// Alternate provider ranks so albums cannot exhaust the limit before playlists.
	for i := 0; i < len(albums) || i < len(playlists); i++ {
		for _, group := range [][]scAPIAlbumItem{albums, playlists} {
			if i >= len(group) {
				continue
			}
			it := group[i]
			if it.PermalinkURL != "" && !seenURLs[it.PermalinkURL] && it.TrackCount > 0 {
				seenURLs[it.PermalinkURL] = true
				allItems = append(allItems, it)
			}
		}
	}

	if len(allItems) == 0 {
		return nil, nil
	}

	if len(allItems) > limit {
		allItems = allItems[:limit]
	}

	var stubIDs []int64
	seenIDs := make(map[int64]bool)
	for _, it := range allItems {
		for _, tr := range it.Tracks {
			if tr.Title == "" && tr.ID > 0 && !seenIDs[tr.ID] {
				seenIDs[tr.ID] = true
				stubIDs = append(stubIDs, tr.ID)
			}
		}
	}

	hydratedMap := make(map[int64]scAPITrackItem)
	if len(stubIDs) > 0 {
		hMap, _ := s.HydrateTracks(ctx, clientID, stubIDs)
		if hMap != nil {
			hydratedMap = hMap
		}
	}

	var candidates []AlbumReleaseCandidate
	for _, item := range allItems {
		cand := s.convertAlbumToCandidate(item, hydratedMap)
		if len(cand.Tracks) > 0 {
			candidates = append(candidates, cand)
		}
	}

	return candidates, nil
}

// SearchSoundCloudSets выполняет поиск сетов/плейлистов на SoundCloud по артисту и названию альбома.
func (s *SoundCloudSetSearcher) SearchSoundCloudSets(ctx context.Context, artist, album string, limit int) ([]AlbumReleaseCandidate, error) {
	if s.searchFunc != nil {
		return s.searchFunc(ctx, artist, album, limit)
	}

	if limit <= 0 {
		limit = 3
	}

	cleanArt := strings.TrimSpace(artist)
	cleanAlb := strings.TrimSpace(album)
	if cleanArt == "" && cleanAlb == "" {
		return nil, nil
	}

	query := fmt.Sprintf("%s %s", cleanArt, cleanAlb)
	candidates, err := s.SearchSetsAPI(ctx, query, limit)
	if err == nil && len(candidates) > 0 {
		return candidates, nil
	}

	return nil, err
}
