package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultMusicBrainzBaseURL  = "https://musicbrainz.org/ws/2"
	defaultMusicBrainzOAuthURL = "https://musicbrainz.org/oauth2"
	defaultCoverArtBaseURL     = "https://coverartarchive.org/release"
	defaultMusicBrainzUserAgnt = "MusicManager/1.0 (contact@verymad.net)"
)

// OAuthTokenResponse представляет ответ MusicBrainz OAuth 2.0 token endpoint.
type OAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope,omitempty"`
}

// MusicBrainzUserInfo представляет профиль пользователя MusicBrainz из userinfo.
type MusicBrainzUserInfo struct {
	Sub     string `json:"sub"`
	Profile string `json:"profile,omitempty"`
	Email   string `json:"email,omitempty"`
}

// MusicBrainzOAuthStatus содержит текущий статус подключения к MusicBrainz OAuth 2.0.
type MusicBrainzOAuthStatus struct {
	Configured bool      `json:"configured"`
	Connected  bool      `json:"connected"`
	Username   string    `json:"username,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
}

// MusicBrainzTokenPersistence интерфейс для сохранения и загрузки OAuth токенов.
type MusicBrainzTokenPersistence interface {
	SaveTokens(ctx context.Context, accessToken, refreshToken string, expiresAt time.Time, username string) error
	ClearTokens(ctx context.Context) error
}

// MusicBrainzClient осуществляет запросы к MusicBrainz API v2 и Cover Art Archive.
type MusicBrainzClient struct {
	baseURL         string
	oauthBaseURL    string
	coverArtBaseURL string
	client          *http.Client
	token           string
	refreshToken    string
	tokenExpiresAt  time.Time
	username        string
	clientID        string
	clientSecret    string
	redirectURI     string
	persistence     MusicBrainzTokenPersistence
	userAgent       string

	mu          sync.Mutex
	lastRequest time.Time
	minInterval time.Duration

	oauthMu       sync.RWMutex
	oauthChangeMu sync.Mutex

	cacheMu sync.RWMutex
	cache   map[string]mbCacheEntry
}

type mbCacheEntry struct {
	data      any
	expiresAt time.Time
}

// NewMusicBrainzClient создает новый экземпляр клиента MusicBrainz.
func NewMusicBrainzClient(token string) *MusicBrainzClient {
	interval := 1050 * time.Millisecond
	if token != "" {
		interval = 250 * time.Millisecond
	}
	return &MusicBrainzClient{
		baseURL:         defaultMusicBrainzBaseURL,
		oauthBaseURL:    defaultMusicBrainzOAuthURL,
		coverArtBaseURL: defaultCoverArtBaseURL,
		client:          &http.Client{Timeout: 12 * time.Second},
		token:           token,
		userAgent:       defaultMusicBrainzUserAgnt,
		minInterval:     interval,
		cache:           make(map[string]mbCacheEntry),
	}
}

// SetBaseURL переопределяет базовый URL (используется для тестов httptest.Server).
func (c *MusicBrainzClient) SetBaseURL(u string) {
	c.baseURL = strings.TrimRight(u, "/")
}

// SetCoverArtBaseURL переопределяет базовый URL архива обложек (для тестов).
func (c *MusicBrainzClient) SetCoverArtBaseURL(u string) {
	c.coverArtBaseURL = strings.TrimRight(u, "/")
}

// SetOAuthBaseURL переопределяет базовый URL OAuth 2.0 (используется для тестов httptest.Server).
func (c *MusicBrainzClient) SetOAuthBaseURL(u string) {
	c.oauthMu.Lock()
	defer c.oauthMu.Unlock()
	c.oauthBaseURL = strings.TrimRight(u, "/")
}

// SetOAuthConfig конфигурирует параметры OAuth 2.0 Web Application.
func (c *MusicBrainzClient) SetOAuthConfig(clientID, clientSecret, redirectURI string) {
	c.oauthMu.Lock()
	defer c.oauthMu.Unlock()
	c.clientID = clientID
	c.clientSecret = clientSecret
	c.redirectURI = redirectURI
}

// SetPersistence подключает обработчик персистентного хранения токенов.
func (c *MusicBrainzClient) SetPersistence(p MusicBrainzTokenPersistence) {
	c.oauthMu.Lock()
	defer c.oauthMu.Unlock()
	c.persistence = p
}

// SetAccessToken устанавливает активный access token и корректирует rate limit.
func (c *MusicBrainzClient) SetAccessToken(token string) {
	c.oauthMu.Lock()
	c.token = token
	c.oauthMu.Unlock()

	c.mu.Lock()
	if token != "" {
		c.minInterval = 250 * time.Millisecond
	} else {
		c.minInterval = 1050 * time.Millisecond
	}
	c.mu.Unlock()
}

// SetRefreshToken устанавливает refresh token.
func (c *MusicBrainzClient) SetRefreshToken(token string) {
	c.oauthMu.Lock()
	defer c.oauthMu.Unlock()
	c.refreshToken = token
}

// SetTokens устанавливает полный набор OAuth токенов с временем жизни и именем пользователя.
func (c *MusicBrainzClient) SetTokens(accessToken, refreshToken string, expiresAt time.Time, username string) {
	c.oauthMu.Lock()
	c.token = accessToken
	c.refreshToken = refreshToken
	c.tokenExpiresAt = expiresAt
	c.username = username
	c.oauthMu.Unlock()

	c.mu.Lock()
	if accessToken != "" {
		c.minInterval = 250 * time.Millisecond
	} else {
		c.minInterval = 1050 * time.Millisecond
	}
	c.mu.Unlock()
}

// GetOAuthStatus возвращает текущее состояние авторизации.
func (c *MusicBrainzClient) GetOAuthStatus() MusicBrainzOAuthStatus {
	c.oauthMu.RLock()
	defer c.oauthMu.RUnlock()
	return MusicBrainzOAuthStatus{
		Configured: c.clientID != "" && c.clientSecret != "",
		Connected:  c.token != "",
		Username:   c.username,
		ExpiresAt:  c.tokenExpiresAt,
	}
}

// Disconnect сбрасывает токены авторизации и очищает персистентное хранилище.
func (c *MusicBrainzClient) Disconnect(ctx context.Context) error {
	c.oauthChangeMu.Lock()
	defer c.oauthChangeMu.Unlock()
	c.oauthMu.RLock()
	persistence := c.persistence
	c.oauthMu.RUnlock()
	if persistence != nil {
		if err := persistence.ClearTokens(ctx); err != nil {
			return fmt.Errorf("clear MusicBrainz OAuth session: %w", err)
		}
	}
	c.SetTokens("", "", time.Time{}, "")
	return nil
}

// GetAuthorizationURL генерирует ссылку для авторизации пользователя в MusicBrainz OAuth 2.0.
func (c *MusicBrainzClient) GetAuthorizationURL(state string, overrideRedirectURI ...string) string {
	c.oauthMu.RLock()
	clientID := c.clientID
	redirectURI := c.redirectURI
	oauthBase := c.oauthBaseURL
	c.oauthMu.RUnlock()

	if len(overrideRedirectURI) > 0 && overrideRedirectURI[0] != "" {
		redirectURI = overrideRedirectURI[0]
	}

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", "profile tag rating collection")
	params.Set("access_type", "offline")
	if state != "" {
		params.Set("state", state)
	}

	return fmt.Sprintf("%s/authorize?%s", oauthBase, params.Encode())
}

// ExchangeCode обменивает authorization code на пару access и refresh токенов.
func (c *MusicBrainzClient) ExchangeCode(ctx context.Context, code string, redirectURI string) (*OAuthTokenResponse, error) {
	c.oauthChangeMu.Lock()
	defer c.oauthChangeMu.Unlock()
	c.oauthMu.RLock()
	clientID := c.clientID
	clientSecret := c.clientSecret
	defaultRedir := c.redirectURI
	oauthBase := c.oauthBaseURL
	c.oauthMu.RUnlock()

	if redirectURI == "" {
		redirectURI = defaultRedir
	}

	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("MusicBrainz OAuth не настроен: отсутствуют client_id или client_secret")
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)

	tokenURL := fmt.Sprintf("%s/token", oauthBase)
	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("mb exchange code request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mb exchange code http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("mb token exchange failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp OAuthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("mb token decode: %w", err)
	}

	expiresAt := time.Time{}
	if tokenResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}

	username := ""
	if userInfo, err := c.fetchUserInfo(ctx, oauthBase, tokenResp.AccessToken); err == nil {
		username = userInfo.Sub
	}
	c.oauthMu.RLock()
	persistence := c.persistence
	c.oauthMu.RUnlock()
	if persistence != nil {
		if err := persistence.SaveTokens(ctx, tokenResp.AccessToken, tokenResp.RefreshToken, expiresAt, username); err != nil {
			return nil, fmt.Errorf("save MusicBrainz OAuth session: %w", err)
		}
	}
	c.SetTokens(tokenResp.AccessToken, tokenResp.RefreshToken, expiresAt, username)

	return &tokenResp, nil
}

// RefreshToken обновляет access token с использованием refresh token.
func (c *MusicBrainzClient) RefreshToken(ctx context.Context, refreshToken string) (*OAuthTokenResponse, error) {
	c.oauthChangeMu.Lock()
	defer c.oauthChangeMu.Unlock()
	c.oauthMu.RLock()
	clientID := c.clientID
	clientSecret := c.clientSecret
	oauthBase := c.oauthBaseURL
	currentRefresh := c.refreshToken
	persistence := c.persistence
	c.oauthMu.RUnlock()

	rf := refreshToken
	if rf == "" {
		rf = currentRefresh
	}
	if rf == "" {
		return nil, fmt.Errorf("отсутствует refresh_token для обновления сессии MusicBrainz")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", rf)

	tokenURL := fmt.Sprintf("%s/token", oauthBase)
	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("mb refresh token request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mb refresh token http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("mb refresh token failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp OAuthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("mb refresh decode: %w", err)
	}

	expiresAt := time.Time{}
	if tokenResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}

	newRefresh := tokenResp.RefreshToken
	if newRefresh == "" {
		newRefresh = rf
	}
	c.oauthMu.RLock()
	username := c.username
	c.oauthMu.RUnlock()
	if persistence != nil {
		if err := persistence.SaveTokens(ctx, tokenResp.AccessToken, newRefresh, expiresAt, username); err != nil {
			return nil, fmt.Errorf("save refreshed MusicBrainz OAuth session: %w", err)
		}
	}
	c.SetTokens(tokenResp.AccessToken, newRefresh, expiresAt, username)

	return &tokenResp, nil
}

// FetchUserInfo запрашивает профиль пользователя MusicBrainz через OAuth 2.0 userinfo.
func (c *MusicBrainzClient) FetchUserInfo(ctx context.Context) (*MusicBrainzUserInfo, error) {
	c.oauthMu.RLock()
	tok := c.token
	oauthBase := c.oauthBaseURL
	c.oauthMu.RUnlock()

	return c.fetchUserInfo(ctx, oauthBase, tok)
}

func (c *MusicBrainzClient) fetchUserInfo(ctx context.Context, oauthBase, tok string) (*MusicBrainzUserInfo, error) {
	if tok == "" {
		return nil, fmt.Errorf("MusicBrainz: отсутствует активный токен для userinfo")
	}

	userInfoURL := fmt.Sprintf("%s/userinfo", oauthBase)
	req, err := http.NewRequestWithContext(ctx, "GET", userInfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("mb userinfo request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mb userinfo http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("mb userinfo failed (status %d): %s", resp.StatusCode, string(body))
	}

	var info MusicBrainzUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("mb userinfo decode: %w", err)
	}

	return &info, nil
}

func (c *MusicBrainzClient) ensureToken(ctx context.Context) {
	c.oauthMu.RLock()
	needRefresh := c.token != "" && c.refreshToken != "" && !c.tokenExpiresAt.IsZero() && time.Until(c.tokenExpiresAt) < 1*time.Minute
	c.oauthMu.RUnlock()

	if needRefresh {
		_, _ = c.RefreshToken(ctx, "")
	}
}

func (c *MusicBrainzClient) waitRateLimit(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.lastRequest.IsZero() {
		elapsed := time.Since(c.lastRequest)
		if elapsed < c.minInterval {
			waitDur := c.minInterval - elapsed
			select {
			case <-time.After(waitDur):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	c.lastRequest = time.Now()
	return nil
}

// MusicBrainz Raw API Response Types

type mbArtistCredit struct {
	Name       string `json:"name"`
	Joinphrase string `json:"joinphrase"`
	Artist     struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"artist"`
}

type mbCoverArtArchive struct {
	Front   bool `json:"front"`
	Back    bool `json:"back"`
	Count   int  `json:"count"`
	Artwork bool `json:"artwork"`
}

type mbReleaseSearchItem struct {
	ID              string            `json:"id"`
	Score           int               `json:"score"`
	Title           string            `json:"title"`
	Status          string            `json:"status"`
	Date            string            `json:"date"`
	Country         string            `json:"country"`
	Barcode         string            `json:"barcode"`
	TrackCount      int               `json:"track-count"`
	ArtistCredit    []mbArtistCredit  `json:"artist-credit"`
	CoverArtArchive mbCoverArtArchive `json:"cover-art-archive"`
	LabelInfo       []struct {
		Label struct {
			Name string `json:"name"`
		} `json:"label"`
	} `json:"label-info"`
}

type mbReleaseSearchResponse struct {
	Count    int                   `json:"count"`
	Releases []mbReleaseSearchItem `json:"releases"`
}

type mbTrackRecording struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Length int      `json:"length"`
	ISRCs  []string `json:"isrcs,omitempty"`
}

type mbTrack struct {
	ID           string           `json:"id"`
	Number       string           `json:"number"`
	Position     int              `json:"position"`
	Title        string           `json:"title"`
	Length       int              `json:"length"` // Длительность в миллисекундах
	ArtistCredit []mbArtistCredit `json:"artist-credit"`
	Recording    mbTrackRecording `json:"recording"`
}

type mbMedium struct {
	Position   int       `json:"position"` // Номер диска
	Format     string    `json:"format"`
	TrackCount int       `json:"track-count"`
	Tracks     []mbTrack `json:"tracks"`
}

type mbReleaseDetailsResponse struct {
	ID              string            `json:"id"`
	Title           string            `json:"title"`
	Date            string            `json:"date"`
	Country         string            `json:"country"`
	Barcode         string            `json:"barcode"`
	ArtistCredit    []mbArtistCredit  `json:"artist-credit"`
	CoverArtArchive mbCoverArtArchive `json:"cover-art-archive"`
	Media           []mbMedium        `json:"media"`
	Genres          []struct {
		Name string `json:"name"`
	} `json:"genres"`
	LabelInfo []struct {
		Label struct {
			Name string `json:"name"`
		} `json:"label"`
	} `json:"label-info"`
}

// SearchReleases ищет релизы по текстовому запросу в каталоге MusicBrainz.
func (c *MusicBrainzClient) SearchReleases(ctx context.Context, query string) ([]CatalogReleaseSummary, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	cacheKey := "search:" + q
	c.cacheMu.RLock()
	if entry, ok := c.cache[cacheKey]; ok && time.Now().Before(entry.expiresAt) {
		c.cacheMu.RUnlock()
		if res, ok := entry.data.([]CatalogReleaseSummary); ok {
			return res, nil
		}
	}
	c.cacheMu.RUnlock()

	c.ensureToken(ctx)

	if err := c.waitRateLimit(ctx); err != nil {
		return nil, err
	}

	reqURL := fmt.Sprintf("%s/release?query=%s&fmt=json&limit=15", c.baseURL, url.QueryEscape(q))
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("mb request create: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mb search do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("mb search failed (status %d): %s", resp.StatusCode, string(body))
	}

	var searchResp mbReleaseSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("mb search decode: %w", err)
	}

	summaries := make([]CatalogReleaseSummary, 0, len(searchResp.Releases))
	for _, rel := range searchResp.Releases {
		artistName := formatArtistCredit(rel.ArtistCredit)
		year := parseYear(rel.Date)

		var labelName string
		if len(rel.LabelInfo) > 0 && rel.LabelInfo[0].Label.Name != "" {
			labelName = rel.LabelInfo[0].Label.Name
		}

		coverURL := ""
		if rel.CoverArtArchive.Front || rel.CoverArtArchive.Count > 0 {
			coverURL = fmt.Sprintf("%s/%s/front-500.jpg", c.coverArtBaseURL, rel.ID)
		}

		summaries = append(summaries, CatalogReleaseSummary{
			ID:         rel.ID,
			Source:     "musicbrainz",
			Title:      rel.Title,
			Artist:     artistName,
			CoverURL:   coverURL,
			Year:       year,
			Date:       rel.Date,
			TrackCount: rel.TrackCount,
			Label:      labelName,
			Country:    rel.Country,
			Barcode:    rel.Barcode,
			Score:      rel.Score,
		})
	}

	c.cacheMu.Lock()
	c.cache[cacheKey] = mbCacheEntry{
		data:      summaries,
		expiresAt: time.Now().Add(5 * time.Minute),
	}
	c.cacheMu.Unlock()

	return summaries, nil
}

// GetRelease загружает подробные метаданные релиза с треклистом.
func (c *MusicBrainzClient) GetRelease(ctx context.Context, mbid string) (*CatalogReleaseDetails, error) {
	mbid = strings.TrimSpace(mbid)
	if mbid == "" {
		return nil, fmt.Errorf("empty release mbid")
	}

	cacheKey := "release:" + mbid
	c.cacheMu.RLock()
	if entry, ok := c.cache[cacheKey]; ok && time.Now().Before(entry.expiresAt) {
		c.cacheMu.RUnlock()
		if res, ok := entry.data.(*CatalogReleaseDetails); ok {
			return res, nil
		}
	}
	c.cacheMu.RUnlock()

	c.ensureToken(ctx)

	if err := c.waitRateLimit(ctx); err != nil {
		return nil, err
	}

	reqURL := fmt.Sprintf("%s/release/%s?inc=recordings+artist-credits+genres+labels+media&fmt=json", c.baseURL, url.PathEscape(mbid))
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("mb release request create: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mb get release do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("mb release failed (status %d): %s", resp.StatusCode, string(body))
	}

	var detailsResp mbReleaseDetailsResponse
	if err := json.NewDecoder(resp.Body).Decode(&detailsResp); err != nil {
		return nil, fmt.Errorf("mb release decode: %w", err)
	}

	releaseArtist := formatArtistCredit(detailsResp.ArtistCredit)
	year := parseYear(detailsResp.Date)

	var labelName string
	if len(detailsResp.LabelInfo) > 0 && detailsResp.LabelInfo[0].Label.Name != "" {
		labelName = detailsResp.LabelInfo[0].Label.Name
	}

	var genreNames []string
	for _, g := range detailsResp.Genres {
		if g.Name != "" {
			genreNames = append(genreNames, g.Name)
		}
	}
	primaryGenre := ""
	if len(genreNames) > 0 {
		primaryGenre = genreNames[0]
	}

	coverURL := ""
	if detailsResp.CoverArtArchive.Front || detailsResp.CoverArtArchive.Count > 0 {
		coverURL = fmt.Sprintf("%s/%s/front-500.jpg", c.coverArtBaseURL, detailsResp.ID)
	}

	var tracks []CatalogTrackDetail
	posCounter := 1

	for _, medium := range detailsResp.Media {
		discNum := medium.Position
		if discNum <= 0 {
			discNum = 1
		}

		for _, t := range medium.Tracks {
			tArtist := formatArtistCredit(t.ArtistCredit)
			if tArtist == "" {
				tArtist = releaseArtist
			}

			artistList := extractArtistList(t.ArtistCredit)
			if len(artistList) == 0 && tArtist != "" {
				artistList = strings.Split(tArtist, "\\\\")
			}

			trackNum := t.Position
			if trackNum <= 0 {
				if parsedNum, err := strconv.Atoi(t.Number); err == nil {
					trackNum = parsedNum
				} else {
					trackNum = posCounter
				}
			}

			durSec := t.Length / 1000
			if durSec == 0 && t.Recording.Length > 0 {
				durSec = t.Recording.Length / 1000
			}

			isrc := ""
			if len(t.Recording.ISRCs) > 0 {
				isrc = t.Recording.ISRCs[0]
			}

			tracks = append(tracks, CatalogTrackDetail{
				Position:     posCounter,
				TrackNumber:  trackNum,
				DiscNumber:   discNum,
				Title:        t.Title,
				Artist:       tArtist,
				Artists:      artistList,
				Duration:     durSec,
				DurationMs:   t.Length,
				ISRC:         isrc,
				ReleaseTitle: detailsResp.Title,
			})
			posCounter++
		}
	}

	res := &CatalogReleaseDetails{
		ID:         detailsResp.ID,
		Source:     "musicbrainz",
		Title:      detailsResp.Title,
		Artist:     releaseArtist,
		CoverURL:   coverURL,
		Year:       year,
		Date:       detailsResp.Date,
		Genre:      primaryGenre,
		Genres:     genreNames,
		Label:      labelName,
		TrackCount: len(tracks),
		Tracks:     tracks,
	}

	c.cacheMu.Lock()
	c.cache[cacheKey] = mbCacheEntry{
		data:      res,
		expiresAt: time.Now().Add(5 * time.Minute),
	}
	c.cacheMu.Unlock()

	return res, nil
}

func (c *MusicBrainzClient) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	c.oauthMu.RLock()
	tok := c.token
	c.oauthMu.RUnlock()
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

// formatArtistCredit объединяет имена артистов с разделителем "\\" (инвариант проекта для фронтенда).
func formatArtistCredit(credits []mbArtistCredit) string {
	if len(credits) == 0 {
		return ""
	}
	var names []string
	for _, ac := range credits {
		name := strings.TrimSpace(ac.Name)
		if name == "" {
			name = strings.TrimSpace(ac.Artist.Name)
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, "\\\\")
}

// extractArtistList возвращает срез отдельных имен артистов.
func extractArtistList(credits []mbArtistCredit) []string {
	var list []string
	for _, ac := range credits {
		name := strings.TrimSpace(ac.Name)
		if name == "" {
			name = strings.TrimSpace(ac.Artist.Name)
		}
		if name != "" {
			list = append(list, name)
		}
	}
	return list
}

func parseYear(dateStr string) int {
	dateStr = strings.TrimSpace(dateStr)
	if len(dateStr) >= 4 {
		if y, err := strconv.Atoi(dateStr[:4]); err == nil && y > 1800 && y < 2100 {
			return y
		}
	}
	return 0
}

// GetArtistReleaseGroups запрашивает дискографию релиз-групп артиста.
func (c *MusicBrainzClient) GetArtistReleaseGroups(ctx context.Context, mbid string) ([]CatalogReleaseSummary, error) {
	mbid = strings.TrimSpace(mbid)
	if mbid == "" {
		return nil, fmt.Errorf("mbid cannot be empty")
	}

	reqURL := fmt.Sprintf("%s/release-group?artist=%s&type=album|ep|single|compilation&limit=100&fmt=json", c.baseURL, url.PathEscape(mbid))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultMusicBrainzUserAgnt)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("musicbrainz request failed with status %d", resp.StatusCode)
	}

	var data struct {
		ReleaseGroups []struct {
			ID               string           `json:"id"`
			Title            string           `json:"title"`
			PrimaryType      string           `json:"primary-type"`
			SecondaryTypes   []string         `json:"secondary-types"`
			FirstReleaseDate string           `json:"first-release-date"`
			ArtistCredit     []mbArtistCredit `json:"artist-credit"`
		} `json:"release-groups"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	var results []CatalogReleaseSummary
	for _, rg := range data.ReleaseGroups {
		artist := formatArtistCredit(rg.ArtistCredit)
		year := parseYear(rg.FirstReleaseDate)
		releaseType := strings.ToLower(rg.PrimaryType)
		for _, secondary := range rg.SecondaryTypes {
			if strings.EqualFold(secondary, "compilation") {
				releaseType = "compilation"
				break
			}
		}
		results = append(results, CatalogReleaseSummary{
			ID:          rg.ID,
			ReleaseType: releaseType,
			Source:      "musicbrainz",
			Title:       rg.Title,
			Artist:      artist,
			Year:        year,
			Date:        rg.FirstReleaseDate,
		})
	}
	return results, nil
}
