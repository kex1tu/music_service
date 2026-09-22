package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// YandexMusicClient инкапсулирует вызовы к открытому HTTPS API Яндекс Музыки.
type YandexMusicClient struct {
	httpClient *http.Client
	baseURL    string
	token      string
}

// NewYandexMusicClient создает новый экземпляр клиента Яндекс Музыки.
// Поддерживает опциональный proxyURL (http://, https://, socks5://) для обхода региональных ограничений (HTTP 451).
func NewYandexMusicClient(token string, proxyURL ...string) *YandexMusicClient {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}

	if len(proxyURL) > 0 && strings.TrimSpace(proxyURL[0]) != "" {
		proxyStr := strings.TrimSpace(proxyURL[0])
		if parsedProxy, err := url.Parse(proxyStr); err == nil {
			transport.Proxy = http.ProxyURL(parsedProxy)
		}
	}

	return &YandexMusicClient{
		httpClient: &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
		},
		baseURL: "https://api.music.yandex.net",
		token:   strings.TrimSpace(token),
	}
}

func yandexStatusError(resp *http.Response, contextMsg string) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusUnavailableForLegalReasons || resp.StatusCode == 451 {
		return fmt.Errorf("яндекс музыка заблокировала доступ (HTTP 451 Geoblock): IP-адрес сервера находится вне РФ/СНГ. Укажите YANDEX_MUSIC_PROXY в .env (например, http://ru-proxy:port или socks5://ru-proxy:1080) или используйте ссылки Deezer/YouTube")
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("яндекс музыка вернула HTTP %d (доступ запрещен/требуется авторизация): проверьте корректность YANDEX_MUSIC_TOKEN в .env", resp.StatusCode)
	}
	return fmt.Errorf("яндекс музыка вернула HTTP %d для %s", resp.StatusCode, contextMsg)
}

// SetBaseURL позволяет переопределить базовый URL для мок-тестов.
func (c *YandexMusicClient) SetBaseURL(u string) {
	c.baseURL = strings.TrimRight(u, "/")
}

// YandexArtistMeta представляет базовые метаданные артиста.
type YandexArtistMeta struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Cover struct {
		URI    string `json:"uri"`
		Prefix string `json:"prefix"`
	} `json:"cover"`
}

// AvatarURL возвращает прямую ссылку на аватар артиста в высоком разрешении.
func (a *YandexArtistMeta) AvatarURL() string {
	uri := a.Cover.URI
	if uri == "" {
		uri = a.Cover.Prefix
	}
	if uri == "" {
		return ""
	}
	uri = strings.Replace(uri, "%%", "1000x1000", 1)
	if !strings.HasPrefix(uri, "http://") && !strings.HasPrefix(uri, "https://") {
		uri = "https://" + uri
	}
	return uri
}

// YandexAlbum представляет метаданные альбома или сингла.
type YandexAlbum struct {
	ID          int64              `json:"id"`
	Title       string             `json:"title"`
	MetaType    string             `json:"metaType"`
	Year        int                `json:"year"`
	ReleaseDate string             `json:"releaseDate"`
	CoverURI    string             `json:"coverUri"`
	Genre       string             `json:"genre"`
	TrackCount  int                `json:"trackCount"`
	Artists     []YandexArtistMeta `json:"artists"`
}

// BestCoverURL возвращает ссылку на студийную обложку 1000x1000.
func (alb *YandexAlbum) BestCoverURL() string {
	if alb.CoverURI == "" {
		return ""
	}
	uri := strings.Replace(alb.CoverURI, "%%", "1000x1000", 1)
	if !strings.HasPrefix(uri, "http://") && !strings.HasPrefix(uri, "https://") {
		uri = "https://" + uri
	}
	return uri
}

// RecordType возвращает нормализованный тип: "album", "single" или "compilation".
func (alb *YandexAlbum) RecordType() string {
	mt := strings.ToLower(alb.MetaType)
	if mt == "single" || alb.TrackCount == 1 {
		return "single"
	}
	if mt == "compilation" {
		return "compilation"
	}
	return "album"
}

// PrimaryArtistName возвращает имя основного исполнителя альбома.
func (alb *YandexAlbum) PrimaryArtistName() string {
	if len(alb.Artists) > 0 && alb.Artists[0].Name != "" {
		return alb.Artists[0].Name
	}
	return "Unknown Artist"
}

// YandexTrack представляет дорожку альбома со списком всех соавторов.
type YandexTrack struct {
	ID            int64              `json:"id"`
	Title         string             `json:"title"`
	DurationMs    int                `json:"durationMs"`
	TrackPosition int                `json:"track_position,omitempty"`
	DiskNumber    int                `json:"disk_number,omitempty"`
	Artists       []YandexArtistMeta `json:"artists"`
	Albums        []YandexAlbum      `json:"albums"`
}

// AllArtistNames возвращает слайс имен всех исполнителей трека (основной + фиты).
func (t *YandexTrack) AllArtistNames() []string {
	var names []string
	seen := make(map[string]bool)
	for _, a := range t.Artists {
		trimmed := strings.TrimSpace(a.Name)
		if trimmed != "" {
			lower := strings.ToLower(trimmed)
			if !seen[lower] {
				seen[lower] = true
				names = append(names, trimmed)
			}
		}
	}
	return names
}

// YandexAlbumWithTracks представляет полный треклист альбома.
type YandexAlbumWithTracks struct {
	YandexAlbum
	Tracks []YandexTrack
}

func (c *YandexMusicClient) newRequest(ctx context.Context, method, path string) (*http.Request, error) {
	reqURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	if c.token != "" {
		req.Header.Set("Authorization", "OAuth "+c.token)
	}
	return req, nil
}

// ParseURL извлекает тип ресурса ("artist", "album", "track") и ID из ссылки Яндекс Музыки.
func (c *YandexMusicClient) ParseURL(rawURL string) (string, int64, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", 0, fmt.Errorf("некорректный URL: %w", err)
	}

	pathParts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, part := range pathParts {
		if (part == "artist" || part == "album" || part == "track") && i+1 < len(pathParts) {
			idStr := pathParts[i+1]
			if id, pErr := strconv.ParseInt(idStr, 10, 64); pErr == nil && id > 0 {
				return part, id, nil
			}
		}
	}

	return "", 0, fmt.Errorf("не удалось извлечь ресурс (artist/album/track) из ссылки Яндекс Музыки %q", rawURL)
}

// GetArtistBriefInfo возвращает имя и аватар артиста.
func (c *YandexMusicClient) GetArtistBriefInfo(ctx context.Context, artistID int64) (*YandexArtistMeta, error) {
	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("/artists/%d/brief-info", artistID))
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("сбой запроса профиля артиста Яндекс Музыки: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, yandexStatusError(resp, fmt.Sprintf("артиста %d", artistID))
	}

	var data struct {
		Result struct {
			Artist YandexArtistMeta `json:"artist"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON артиста Яндекс Музыки: %w", err)
	}

	if data.Result.Artist.ID == 0 {
		data.Result.Artist.ID = artistID
	}
	return &data.Result.Artist, nil
}

// GetArtistDirectAlbums возвращает список официальных альбомов и синглов артиста.
func (c *YandexMusicClient) GetArtistDirectAlbums(ctx context.Context, artistID int64, page, pageSize int) ([]YandexAlbum, error) {
	if pageSize <= 0 {
		pageSize = 100
	}
	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("/artists/%d/direct-albums?page=%d&pageSize=%d", artistID, page, pageSize))
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("сбой запроса дискографии артиста Яндекс Музыки: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, yandexStatusError(resp, fmt.Sprintf("дискографии артиста %d", artistID))
	}

	var data struct {
		Result struct {
			Albums []YandexAlbum `json:"albums"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON дискографии Яндекс Музыки: %w", err)
	}

	return data.Result.Albums, nil
}

// GetAlbumWithTracks возвращает метаданные альбома и его треклист.
func (c *YandexMusicClient) GetAlbumWithTracks(ctx context.Context, albumID int64) (*YandexAlbumWithTracks, error) {
	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("/albums/%d/with-tracks", albumID))
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("сбой запроса треклиста альбома Яндекс Музыки: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, yandexStatusError(resp, fmt.Sprintf("альбома %d", albumID))
	}

	var data struct {
		Result struct {
			YandexAlbum
			Volumes [][]YandexTrack `json:"volumes"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON треклиста альбома Яндекс Музыки: %w", err)
	}

	res := &YandexAlbumWithTracks{
		YandexAlbum: data.Result.YandexAlbum,
	}

	pos := 1
	for discIdx, volume := range data.Result.Volumes {
		discNum := discIdx + 1
		for _, tr := range volume {
			tCopy := tr
			tCopy.DiskNumber = discNum
			tCopy.TrackPosition = pos
			pos++
			res.Tracks = append(res.Tracks, tCopy)
		}
	}

	return res, nil
}

// SearchTrack выполняет поиск треков для сверки метаданных и соавторов.
func (c *YandexMusicClient) SearchTrack(ctx context.Context, query string) ([]YandexTrack, error) {
	cleanQ := strings.TrimSpace(query)
	if cleanQ == "" {
		return nil, nil
	}

	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("/search?text=%s&type=track&page=0", url.QueryEscape(cleanQ)))
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("сбой поиска трека в Яндекс Музыке: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, yandexStatusError(resp, fmt.Sprintf("поиска трека %q", cleanQ))
	}

	var data struct {
		Result struct {
			Tracks struct {
				Results []YandexTrack `json:"results"`
			} `json:"tracks"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON результатов поиска: %w", err)
	}

	return data.Result.Tracks.Results, nil
}
