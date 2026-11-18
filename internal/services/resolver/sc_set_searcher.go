package resolver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	Verified  bool   `json:"verified"`
	AvatarURL string `json:"avatar_url"`
}

type scAPITrackItem struct {
	ID           int64     `json:"id"`
	Title        string    `json:"title"`
	Duration     int64     `json:"duration"` // in milliseconds
	PermalinkURL string    `json:"permalink_url"`
	ArtworkURL   string    `json:"artwork_url"`
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

// SoundCloudSetsPageResult содержит результат постраничного поиска сетов/альбомов с сохранением признака пагинации.
type SoundCloudSetsPageResult struct {
	Candidates        []AlbumReleaseCandidate `json:"candidates"`
	HasMoreSoundCloud bool                    `json:"has_more_soundcloud"`
	AlbumOffset       int                     `json:"album_offset"`
	PlaylistOffset    int                     `json:"playlist_offset"`
	AlbumToken        string                  `json:"album_token,omitempty"`
	PlaylistToken     string                  `json:"playlist_token,omitempty"`
}

// SoundCloudContinuationToken безопасный токен продолжения пагинации SoundCloud.
type SoundCloudContinuationToken struct {
	Category string `json:"c"`
	Query    string `json:"q"`
	Offset   int    `json:"o"`
	Cursor   string `json:"cur,omitempty"`
}

// SoundCloudSetSearcher осуществляет поиск и разбор плейлистов/сетов SoundCloud и других внешних ссылок.
type SoundCloudSetSearcher struct {
	cookiesPath  string
	searchFunc   func(ctx context.Context, artist, album string, limit int) ([]AlbumReleaseCandidate, error)
	parseURLFunc func(ctx context.Context, setURL string) (*AlbumReleaseCandidate, error)

	httpClient    *http.Client
	apiBaseURL    string
	webBaseURL    string
	clientID      string
	clientIDMu    sync.RWMutex
	clientIDExp   time.Time
	tokenSecret   []byte
	tokenSecretMu sync.RWMutex
}

// NewSoundCloudSetSearcher создает новый экземпляр SoundCloudSetSearcher.
func NewSoundCloudSetSearcher(cookiesPath string) *SoundCloudSetSearcher {
	s := &SoundCloudSetSearcher{
		cookiesPath: cookiesPath,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("слишком много перенаправлений")
				}
				if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
					return fmt.Errorf("недопустимая схема редиректа: %s", req.URL.Scheme)
				}
				if req.URL.User != nil {
					return fmt.Errorf("учетные данные в URL редиректа запрещены")
				}
				return nil
			},
		},
		apiBaseURL: "https://api-v2.soundcloud.com",
		webBaseURL: "https://soundcloud.com",
		clientID:   defaultSoundCloudClientID,
	}
	s.getTokenSecret()
	return s
}

func (s *SoundCloudSetSearcher) getTokenSecret() []byte {
	s.tokenSecretMu.RLock()
	if len(s.tokenSecret) == 32 {
		defer s.tokenSecretMu.RUnlock()
		return s.tokenSecret
	}
	s.tokenSecretMu.RUnlock()

	s.tokenSecretMu.Lock()
	defer s.tokenSecretMu.Unlock()
	if len(s.tokenSecret) == 32 {
		return s.tokenSecret
	}
	sec := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, sec); err != nil {
		copy(sec, []byte("sc-sec-token-rnd-default-key-32b"))
	}
	s.tokenSecret = sec
	return s.tokenSecret
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
	return s.querySearchPageDetailed(ctx, endpoint, query, clientID, limit, offset, "")
}

func (s *SoundCloudSetSearcher) querySearchPageDetailed(ctx context.Context, endpoint, query, clientID string, limit, offset int, cursor string) (scAPISearchResponse, error) {
	var root scAPISearchResponse
	baseURL := s.apiBaseURL
	if baseURL == "" {
		baseURL = "https://api-v2.soundcloud.com"
	}

	var qBuilder strings.Builder
	qBuilder.WriteString(fmt.Sprintf("%s/%s?q=%s&client_id=%s&limit=%d&linked_partitioning=1",
		baseURL,
		endpoint,
		url.QueryEscape(query),
		url.QueryEscape(clientID),
		limit,
	))
	if cursor != "" {
		qBuilder.WriteString("&cursor=" + url.QueryEscape(cursor))
	} else {
		qBuilder.WriteString(fmt.Sprintf("&offset=%d", offset))
	}

	reqURL := qBuilder.String()
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

	limitedReader := io.LimitReader(resp.Body, 5*1024*1024)
	if err := json.NewDecoder(limitedReader).Decode(&root); err != nil {
		return root, err
	}

	return root, nil
}

func (s *SoundCloudSetSearcher) parseAndValidateNextHref(nextHref, expectedEndpoint, expectedQuery string, currentOffset int, currentCursor string) (nextOffset int, nextCursor string, valid bool) {
	trimmed := strings.TrimSpace(nextHref)
	if trimmed == "" {
		return -1, "", false
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.User != nil {
		return -1, "", false
	}

	baseU, err := url.Parse(s.apiBaseURL)
	if err != nil {
		return -1, "", false
	}
	expectedHost := baseU.Host
	if expectedHost == "" {
		expectedHost = "api-v2.soundcloud.com"
	}
	if !strings.EqualFold(u.Host, expectedHost) {
		return -1, "", false
	}

	expectedScheme := baseU.Scheme
	if expectedScheme == "" {
		expectedScheme = "https"
	}
	if !strings.EqualFold(u.Scheme, expectedScheme) {
		return -1, "", false
	}

	expectedPath := "/" + strings.TrimPrefix(expectedEndpoint, "/")
	if u.Path != expectedPath {
		return -1, "", false
	}

	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || (q.Has("q") && q.Get("q") != expectedQuery) {
		return -1, "", false
	}
	nextCursor = q.Get("cursor")
	if len(nextCursor) > 512 {
		return -1, "", false
	}
	nextOffset = -1
	if rawOff := q.Get("offset"); rawOff != "" {
		parsedOff, err := strconv.Atoi(rawOff)
		if err != nil || parsedOff < 0 || parsedOff > 100000 {
			return -1, "", false
		}
		nextOffset = parsedOff
	}

	if (nextCursor != "" && nextCursor == currentCursor) ||
		(nextCursor == "" && currentCursor == "" && nextOffset <= currentOffset) {
		return -1, "", false
	}
	if nextOffset < 0 && nextCursor == "" {
		return -1, "", false
	}

	return nextOffset, nextCursor, true
}

func (s *SoundCloudSetSearcher) createContinuationToken(category, query string, offset int, cursor string) string {
	tok := SoundCloudContinuationToken{
		Category: category,
		Query:    strings.TrimSpace(query),
		Offset:   offset,
		Cursor:   cursor,
	}
	data, err := json.Marshal(tok)
	if err != nil {
		return ""
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	h := hmac.New(sha256.New, s.getTokenSecret())
	h.Write([]byte(payload))
	sig := hex.EncodeToString(h.Sum(nil))
	return payload + "." + sig
}

func (s *SoundCloudSetSearcher) parseContinuationToken(tokenStr, expectedCategory, expectedQuery string) (offset int, cursor string, err error) {
	tokenStr = strings.TrimSpace(tokenStr)
	if tokenStr == "" {
		return 0, "", nil
	}
	if len(tokenStr) > 2048 {
		return -1, "", fmt.Errorf("token too long")
	}
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 2 {
		return -1, "", fmt.Errorf("malformed token structure")
	}
	payload, sig := parts[0], parts[1]
	h := hmac.New(sha256.New, s.getTokenSecret())
	h.Write([]byte(payload))
	expectedSig := hex.EncodeToString(h.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return -1, "", fmt.Errorf("invalid token signature")
	}

	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return -1, "", fmt.Errorf("invalid token encoding")
	}

	var tok SoundCloudContinuationToken
	if err := json.Unmarshal(data, &tok); err != nil {
		return -1, "", fmt.Errorf("invalid token payload")
	}

	if tok.Category != expectedCategory {
		return -1, "", fmt.Errorf("token category mismatch: want %s, got %s", expectedCategory, tok.Category)
	}
	if strings.TrimSpace(tok.Query) != strings.TrimSpace(expectedQuery) {
		return -1, "", fmt.Errorf("token query mismatch")
	}

	return tok.Offset, tok.Cursor, nil
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

// SearchSetsAPIPaged выполняет постраничный поиск альбомов и сетов через SoundCloud API v2.
func (s *SoundCloudSetSearcher) SearchSetsAPIPaged(ctx context.Context, query string, albumOffset, playlistOffset int, albumToken, playlistToken string, limit int) (SoundCloudSetsPageResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	limit = SoundCloudCandidateLimit(limit)
	pageSize := limit

	clientID, err := s.GetClientID(ctx)
	if err != nil || clientID == "" {
		clientID = defaultSoundCloudClientID
	}

	var res SoundCloudSetsPageResult
	var albums, playlists []scAPIAlbumItem
	var hasMoreAlbums, hasMorePlaylists bool
	var albumErr, playlistErr error

	// 1. Поиск альбомов
	if albumOffset >= 0 || albumToken != "" {
		off := albumOffset
		cur := ""
		if albumToken != "" {
			parsedOff, parsedCur, err := s.parseContinuationToken(albumToken, "album", query)
			if err != nil {
				return res, err
			}
			off, cur = parsedOff, parsedCur
		}
		page, err := s.querySearchPageDetailed(ctx, "search/albums", query, clientID, pageSize, off, cur)
		if err != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403")) {
			s.InvalidateClientID()
			if newID, nErr := s.GetClientID(ctx); nErr == nil && newID != "" {
				clientID = newID
				page, err = s.querySearchPageDetailed(ctx, "search/albums", query, clientID, pageSize, off, cur)
			}
		}
		if err != nil {
			albumErr = err
			res.AlbumOffset = albumOffset
			res.AlbumToken = albumToken
		} else {
			nextOff, nextCur, valid := s.parseAndValidateNextHref(page.NextHref, "search/albums", query, off, cur)
			hasMoreAlbums = valid
			if valid {
				res.AlbumOffset = nextOff
				res.AlbumToken = s.createContinuationToken("album", query, nextOff, nextCur)
			} else {
				res.AlbumOffset = -1
			}
			for i := range page.Collection {
				page.Collection[i].ResultType = "album"
			}
			albums = page.Collection
		}
	} else {
		res.AlbumOffset = -1
	}

	// 2. Поиск плейлистов
	if playlistOffset >= 0 || playlistToken != "" {
		off := playlistOffset
		cur := ""
		if playlistToken != "" {
			parsedOff, parsedCur, err := s.parseContinuationToken(playlistToken, "playlist", query)
			if err != nil {
				return res, err
			}
			off, cur = parsedOff, parsedCur
		}
		page, err := s.querySearchPageDetailed(ctx, "search/playlists_without_albums", query, clientID, pageSize, off, cur)
		if err != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403")) {
			s.InvalidateClientID()
			if newID, nErr := s.GetClientID(ctx); nErr == nil && newID != "" {
				clientID = newID
				page, err = s.querySearchPageDetailed(ctx, "search/playlists_without_albums", query, clientID, pageSize, off, cur)
			}
		}
		if err != nil {
			playlistErr = err
			res.PlaylistOffset = playlistOffset
			res.PlaylistToken = playlistToken
		} else {
			nextOff, nextCur, valid := s.parseAndValidateNextHref(page.NextHref, "search/playlists_without_albums", query, off, cur)
			hasMorePlaylists = valid
			if valid {
				res.PlaylistOffset = nextOff
				res.PlaylistToken = s.createContinuationToken("playlist", query, nextOff, nextCur)
			} else {
				res.PlaylistOffset = -1
			}
			for i := range page.Collection {
				page.Collection[i].ResultType = "playlist"
			}
			playlists = page.Collection
		}
	} else {
		res.PlaylistOffset = -1
	}

	res.HasMoreSoundCloud = hasMoreAlbums || hasMorePlaylists || albumErr != nil || playlistErr != nil

	if albumErr != nil && playlistErr != nil {
		return res, fmt.Errorf("SoundCloud search failed: albums: %v; playlists: %w", albumErr, playlistErr)
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
		return res, nil
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

	res.Candidates = candidates
	return res, nil
}

// SearchSetsAPI выполняет прямой поиск альбомов и сетов через SoundCloud API v2 (первая страница).
func (s *SoundCloudSetSearcher) SearchSetsAPI(ctx context.Context, query string, limit int) ([]AlbumReleaseCandidate, error) {
	pageRes, err := s.SearchSetsAPIPaged(ctx, query, 0, 0, "", "", limit)
	return limitSoundCloudSets(pageRes.Candidates, limit), err
}

func limitSoundCloudSets(candidates []AlbumReleaseCandidate, limit int) []AlbumReleaseCandidate {
	return candidates[:min(len(candidates), SoundCloudCandidateLimit(limit))]
}

// SearchSoundCloudSets выполняет поиск сетов/плейлистов на SoundCloud по артисту и названию альбома.
func (s *SoundCloudSetSearcher) SearchSoundCloudSets(ctx context.Context, artist, album string, limit int) ([]AlbumReleaseCandidate, error) {
	if s.searchFunc != nil {
		return s.searchFunc(ctx, artist, album, limit)
	}

	res, err := s.SearchSoundCloudSetsPaged(ctx, artist, album, 0, 0, "", "", limit)
	if err == nil && len(res.Candidates) > 0 {
		return limitSoundCloudSets(res.Candidates, limit), nil
	}
	return nil, err
}

// SearchSoundCloudSetsPaged выполняет постраничный поиск сетов/плейлистов на SoundCloud по артисту и названию альбома.
func (s *SoundCloudSetSearcher) SearchSoundCloudSetsPaged(ctx context.Context, artist, album string, albumOffset, playlistOffset int, albumToken, playlistToken string, limit int) (SoundCloudSetsPageResult, error) {
	if s.searchFunc != nil {
		cands, err := s.searchFunc(ctx, artist, album, limit)
		return SoundCloudSetsPageResult{
			Candidates: cands,
		}, err
	}

	if limit <= 0 {
		limit = 30
	}

	cleanArt := strings.TrimSpace(artist)
	cleanAlb := strings.TrimSpace(album)
	if cleanArt == "" && cleanAlb == "" {
		return SoundCloudSetsPageResult{}, nil
	}

	query := strings.TrimSpace(cleanArt + " " + cleanAlb)
	return s.SearchSetsAPIPaged(ctx, query, albumOffset, playlistOffset, albumToken, playlistToken, limit)
}
