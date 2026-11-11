package resolver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrYandexNoToken возвращается при попытке запроса текстов без указания OAuth токена.
	ErrYandexNoToken         = errors.New("токен Яндекс Музыки не задан или пуст")
	ErrYandexNoLyricsSignKey = errors.New("ключ подписи текстов Яндекс Музыки не задан")
	// ErrYandexLyricsNotFound возвращается, если у трека отсутствует официальный текст в каталоге.
	ErrYandexLyricsNotFound = errors.New("текст для трека Яндекс Музыки не найден")
)

var errYandexLyricsRedirectLimit = errors.New("слишком много перенаправлений хранилища текстов")

var lrcTimestampRegex = regexp.MustCompile(`\[\d{2}:\d{2}(?:\.\d{2,3})?\]`)

// YandexMusicClient инкапсулирует вызовы к открытому HTTPS API Яндекс Музыки.
type YandexMusicClient struct {
	httpClient *http.Client
	baseURL    string
	token      string
	signKey    string
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

// SetSignKey задаёт ключ подписи запросов текстов до запуска обработчиков.
func (c *YandexMusicClient) SetSignKey(key string) {
	c.signKey = strings.TrimSpace(key)
}

func signYandexLyricsRequest(trackID int64, key string) (int64, string) {
	timestamp := time.Now().UTC().Unix()
	msg := fmt.Sprintf("%d%d", trackID, timestamp)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(msg))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return timestamp, sign
}

func yandexStatusError(resp *http.Response, contextMsg string) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusUnavailableForLegalReasons || resp.StatusCode == 451 {
		return fmt.Errorf("яндекс музыка заблокировала доступ (HTTP 451 Geoblock): IP-адрес сервера находится вне РФ/СНГ. Укажите YANDEX_MUSIC_PROXY в .env (например, http://ru-proxy:port или socks5://ru-proxy:1080) или используйте ссылки Deezer/YouTube")
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("яндекс музыка вернула HTTP %d (доступ запрещен/требуется авторизация): проверьте корректность YANDEX_MUSIC_TOKEN в .env или убедитесь, что плейлист является публичным", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("яндекс музыка вернула HTTP 404: запрашиваемый ресурс для %s не найден или удален", contextMsg)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 429 {
		return fmt.Errorf("яндекс музыка вернула HTTP 429 (слишком много запросов / rate limit): повторите попытку позже")
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

// OriginalAvatarURL возвращает прямую ссылку на оригинальный аватар артиста ('orig').
func (a *YandexArtistMeta) OriginalAvatarURL() string {
	uri := a.Cover.URI
	if uri == "" {
		uri = a.Cover.Prefix
	}
	if uri == "" {
		return ""
	}
	uri = strings.Replace(uri, "%%", "orig", 1)
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

// OriginalCoverURL возвращает ссылку на оригинальное изображение обложки альбома (разрешение 'orig').
func (alb *YandexAlbum) OriginalCoverURL() string {
	if alb.CoverURI == "" {
		return ""
	}
	uri := strings.Replace(alb.CoverURI, "%%", "orig", 1)
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

// YandexCover содержит данные обложки плейлиста или релиза.
type YandexCover struct {
	Type   string `json:"type"`
	URI    string `json:"uri"`
	Prefix string `json:"prefix"`
	Dir    string `json:"dir"`
}

// YandexOwner описывает владельца/создателя плейлиста.
type YandexOwner struct {
	UID   int64  `json:"uid"`
	Login string `json:"login"`
	Name  string `json:"name"`
	Sex   string `json:"sex"`
}

// YandexPager описывает структуру пагинации Яндекс Музыки.
type YandexPager struct {
	Page    int `json:"page"`
	PerPage int `json:"perPage"`
	Total   int `json:"total"`
}

// YandexPlaylistTrackItem представляет элемент треклиста плейлиста.
type YandexPlaylistTrackItem struct {
	ID        int64        `json:"id"`
	AlbumID   int64        `json:"albumId"`
	Timestamp string       `json:"timestamp"`
	Track     *YandexTrack `json:"track"`
}

// UnmarshalJSON реализует гибкий парсинг элемента плейлиста (поддержка вложенного track, плоского track и строковых ID).
func (item *YandexPlaylistTrackItem) UnmarshalJSON(data []byte) error {
	type rawItem struct {
		ID        json.RawMessage `json:"id"`
		AlbumID   json.RawMessage `json:"albumId"`
		Timestamp string          `json:"timestamp"`
		Track     *YandexTrack    `json:"track"`
	}
	var r rawItem
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}

	item.Timestamp = r.Timestamp
	item.Track = r.Track

	if len(r.ID) > 0 {
		var idInt int64
		if err := json.Unmarshal(r.ID, &idInt); err == nil {
			item.ID = idInt
		} else {
			var idStr string
			if err := json.Unmarshal(r.ID, &idStr); err == nil {
				item.ID, _ = strconv.ParseInt(idStr, 10, 64)
			}
		}
	}

	if len(r.AlbumID) > 0 {
		var albInt int64
		if err := json.Unmarshal(r.AlbumID, &albInt); err == nil {
			item.AlbumID = albInt
		} else {
			var albStr string
			if err := json.Unmarshal(r.AlbumID, &albStr); err == nil {
				item.AlbumID, _ = strconv.ParseInt(albStr, 10, 64)
			}
		}
	}

	if item.Track == nil {
		var tr YandexTrack
		if err := json.Unmarshal(data, &tr); err == nil && tr.Title != "" {
			item.Track = &tr
			if item.ID == 0 {
				item.ID = tr.ID
			}
		}
	} else if item.Track.ID == 0 && item.ID > 0 {
		item.Track.ID = item.ID
	}

	return nil
}

// YandexPlaylist представляет метаданные и треклист публичного плейлиста.
type YandexPlaylist struct {
	UID          int64                     `json:"uid"`
	Kind         int64                     `json:"kind"`
	Title        string                    `json:"title"`
	Description  string                    `json:"description"`
	TrackCount   int                       `json:"trackCount"`
	PlaylistUUID string                    `json:"playlistUuid"`
	Cover        YandexCover               `json:"cover"`
	OGImage      string                    `json:"ogImage"`
	Owner        YandexOwner               `json:"owner"`
	Tracks       []YandexPlaylistTrackItem `json:"tracks"`
	Pager        *YandexPager              `json:"pager,omitempty"`
}

// BestCoverURL возвращает ссылку на лучшую обложку плейлиста 1000x1000.
func (p *YandexPlaylist) BestCoverURL() string {
	uri := p.Cover.URI
	if uri == "" {
		uri = p.Cover.Prefix
	}
	if uri == "" {
		uri = p.OGImage
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

// OriginalCoverURL возвращает ссылку на оригинальное изображение обложки плейлиста (разрешение 'orig').
func (p *YandexPlaylist) OriginalCoverURL() string {
	uri := p.Cover.URI
	if uri == "" {
		uri = p.Cover.Prefix
	}
	if uri == "" {
		uri = p.OGImage
	}
	if uri == "" {
		return ""
	}
	uri = strings.Replace(uri, "%%", "orig", 1)
	if !strings.HasPrefix(uri, "http://") && !strings.HasPrefix(uri, "https://") {
		uri = "https://" + uri
	}
	return uri
}

var yandexCoverDimRegex = regexp.MustCompile(`/(?:[0-9]+x[0-9]+|%%|orig)(?:[/?#]|$)`)

// YandexCoverCandidateURLs возвращает пару URL (original, fallback) для обложки Яндекс Музыки.
// Если переданный URL относится к сервису аватаров/контента Яндекса, первым кандидатом будет оригинал ('orig'),
// а вторым — проверенное студийное разрешение '1000x1000'.
// Если переданный URL не относится к Яндекс Музыке, возвращается (coverURL, "").
func YandexCoverCandidateURLs(coverURL string) (origURL string, fallbackURL string) {
	trimmed := strings.TrimSpace(coverURL)
	if trimmed == "" {
		return "", ""
	}

	isYandex := false
	parsed, err := url.Parse(trimmed)
	if err == nil {
		host := strings.ToLower(parsed.Host)
		if strings.Contains(host, "yandex.net") || strings.Contains(host, "yandex.ru") || strings.Contains(host, "yastatic.net") {
			isYandex = true
		} else if strings.Contains(parsed.Path, "get-music-content") || strings.Contains(parsed.Path, "get-music-user-playlist") {
			isYandex = true
		}
	} else if strings.Contains(trimmed, "get-music-content") || strings.Contains(trimmed, "get-music-user-playlist") {
		isYandex = true
	}

	if !isYandex {
		return trimmed, ""
	}

	if strings.Contains(trimmed, "%%") {
		orig := strings.Replace(trimmed, "%%", "orig", 1)
		fallback := strings.Replace(trimmed, "%%", "1000x1000", 1)
		return orig, fallback
	}

	if loc := yandexCoverDimRegex.FindStringIndex(trimmed); loc != nil {
		trailing := trimmed[loc[1]:]
		if loc[1] > 0 && (trimmed[loc[1]-1] == '?' || trimmed[loc[1]-1] == '/' || trimmed[loc[1]-1] == '#') {
			trailing = trimmed[loc[1]-1:]
		}
		orig := trimmed[:loc[0]] + "/orig" + trailing
		fallback := trimmed[:loc[0]] + "/1000x1000" + trailing
		return orig, fallback
	}

	return trimmed, ""
}

// OwnerName возвращает имя владельца или логин плейлиста.
func (p *YandexPlaylist) OwnerName() string {
	if strings.TrimSpace(p.Owner.Name) != "" {
		return strings.TrimSpace(p.Owner.Name)
	}
	if strings.TrimSpace(p.Owner.Login) != "" {
		return strings.TrimSpace(p.Owner.Login)
	}
	return "Разные исполнители"
}

// YandexResourceRef содержит разобранные идентификаторы ресурса Яндекс Музыки.
type YandexResourceRef struct {
	Type         string // "artist", "album", "track", "playlist"
	ID           int64  // ID для artist, album, track
	Owner        string // Owner login/UID для owner/kind плейлистов
	Kind         int64  // Номер/kind для owner/kind плейлистов
	PlaylistUUID string // UUID для плейлистов по UUID
	RawURL       string
}

func isYandexHost(host string) bool {
	h := strings.ToLower(host)
	yandexHosts := []string{
		"music.yandex.ru", "music.yandex.com",
		"music.yandex.kz", "music.yandex.by", "music.yandex.uz",
	}
	for _, yh := range yandexHosts {
		if h == yh || strings.HasSuffix(h, "."+yh) {
			return true
		}
	}
	return false
}

var uuidRegex = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var uuidSimpleRegex = regexp.MustCompile(`(?i)^[0-9a-f]{32}$`)

func isUUID(s string) bool {
	clean := strings.TrimSpace(s)
	return uuidRegex.MatchString(clean) || uuidSimpleRegex.MatchString(clean)
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

// ParseResource извлекает тип ресурса ("artist", "album", "track", "playlist") и его специфичные идентификаторы из ссылки или iframe Яндекс Музыки.
func (c *YandexMusicClient) ParseResource(rawURL string) (*YandexResourceRef, error) {
	cleanURL := ExtractURLFromInput(rawURL)
	if cleanURL == "" {
		return nil, errors.New("empty URL")
	}
	if !strings.HasPrefix(cleanURL, "http://") && !strings.HasPrefix(cleanURL, "https://") {
		cleanURL = "https://" + cleanURL
	}
	u, err := url.Parse(cleanURL)
	if err != nil {
		return nil, fmt.Errorf("некорректный URL: %w", err)
	}

	if !isYandexHost(u.Hostname()) {
		return nil, fmt.Errorf("хост %q не принадлежит белому списку доменов Яндекс Музыки", u.Hostname())
	}

	ref := &YandexResourceRef{RawURL: cleanURL}

	// 1. Проверяем Fragment (актуально для iframe: #playlist/{owner}/{kind}, #playlist/{uuid}, #album/{id}, #track/{id})
	frag := strings.Trim(u.Fragment, "/")
	if frag != "" {
		fragParts := strings.Split(frag, "/")
		if len(fragParts) > 0 {
			switch strings.ToLower(fragParts[0]) {
			case "playlist", "playlists":
				if len(fragParts) >= 3 {
					owner, _ := url.PathUnescape(fragParts[1])
					kind, pErr := strconv.ParseInt(fragParts[2], 10, 64)
					if pErr == nil && kind > 0 {
						ref.Type = "playlist"
						ref.Owner = owner
						ref.Kind = kind
						return ref, nil
					}
				}
				if len(fragParts) >= 2 {
					candidate := fragParts[1]
					if isUUID(candidate) || (len(candidate) >= 16 && strings.Contains(candidate, "-")) {
						ref.Type = "playlist"
						ref.PlaylistUUID = candidate
						return ref, nil
					}
				}
			case "album":
				if len(fragParts) >= 2 {
					if id, pErr := strconv.ParseInt(fragParts[1], 10, 64); pErr == nil && id > 0 {
						ref.Type = "album"
						ref.ID = id
						return ref, nil
					}
				}
			case "track":
				if len(fragParts) >= 2 {
					if id, pErr := strconv.ParseInt(fragParts[1], 10, 64); pErr == nil && id > 0 {
						ref.Type = "track"
						ref.ID = id
						return ref, nil
					}
				}
			case "artist":
				if len(fragParts) >= 2 {
					if id, pErr := strconv.ParseInt(fragParts[1], 10, 64); pErr == nil && id > 0 {
						ref.Type = "artist"
						ref.ID = id
						return ref, nil
					}
				}
			}
		}
	}

	// 2. Проверяем Path
	pathParts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, part := range pathParts {
		lowerPart := strings.ToLower(part)
		switch lowerPart {
		case "users":
			if i+3 < len(pathParts) && strings.EqualFold(pathParts[i+2], "playlists") {
				owner, _ := url.PathUnescape(pathParts[i+1])
				kind, pErr := strconv.ParseInt(pathParts[i+3], 10, 64)
				if pErr == nil && kind > 0 {
					ref.Type = "playlist"
					ref.Owner = owner
					ref.Kind = kind
					return ref, nil
				}
			}
		case "playlists", "playlist":
			if i+2 < len(pathParts) {
				owner, _ := url.PathUnescape(pathParts[i+1])
				if kind, pErr := strconv.ParseInt(pathParts[i+2], 10, 64); pErr == nil && kind > 0 {
					ref.Type = "playlist"
					ref.Owner = owner
					ref.Kind = kind
					return ref, nil
				}
			}
			if i+1 < len(pathParts) {
				candidate := pathParts[i+1]
				if isUUID(candidate) || (len(candidate) >= 16 && strings.Contains(candidate, "-")) {
					ref.Type = "playlist"
					ref.PlaylistUUID = candidate
					return ref, nil
				}
			}
		case "album":
			if i+1 < len(pathParts) {
				if id, pErr := strconv.ParseInt(pathParts[i+1], 10, 64); pErr == nil && id > 0 {
					ref.Type = "album"
					ref.ID = id
					return ref, nil
				}
			}
		case "track":
			if i+1 < len(pathParts) {
				if id, pErr := strconv.ParseInt(pathParts[i+1], 10, 64); pErr == nil && id > 0 {
					ref.Type = "track"
					ref.ID = id
					return ref, nil
				}
			}
		case "artist":
			if i+1 < len(pathParts) {
				if id, pErr := strconv.ParseInt(pathParts[i+1], 10, 64); pErr == nil && id > 0 {
					ref.Type = "artist"
					ref.ID = id
					return ref, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("не удалось извлечь ресурс (playlist/artist/album/track) из ссылки Яндекс Музыки %q", rawURL)
}

// ParseURL извлекает тип ресурса ("artist", "album", "track", "playlist") и ID из ссылки Яндекс Музыки.
// Сохраняет полную обратную совместимость с существующими вызовами.
func (c *YandexMusicClient) ParseURL(rawURL string) (string, int64, error) {
	ref, err := c.ParseResource(rawURL)
	if err != nil {
		return "", 0, err
	}
	switch ref.Type {
	case "artist", "album", "track":
		return ref.Type, ref.ID, nil
	case "playlist":
		if ref.Kind > 0 {
			return "playlist", ref.Kind, nil
		}
		return "playlist", 0, nil
	default:
		return ref.Type, ref.ID, nil
	}
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

// GetPlaylist возвращает публичный плейлист по owner и kind (например, /users/yamusic-top/playlists/1076).
func (c *YandexMusicClient) GetPlaylist(ctx context.Context, owner string, kind int64) (*YandexPlaylist, error) {
	cleanOwner := strings.TrimSpace(owner)
	if cleanOwner == "" || kind <= 0 {
		return nil, errors.New("owner and positive kind are required to fetch playlist")
	}

	path := fmt.Sprintf("/users/%s/playlists/%d?rich-tracks=true", url.PathEscape(cleanOwner), kind)
	req, err := c.newRequest(ctx, http.MethodGet, path)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("сбой запроса плейлиста Яндекс Музыки: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, yandexStatusError(resp, fmt.Sprintf("плейлиста %s/%d", cleanOwner, kind))
	}

	var data struct {
		Result YandexPlaylist `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON плейлиста Яндекс Музыки: %w", err)
	}

	playlist := &data.Result
	if playlist.Kind == 0 {
		playlist.Kind = kind
	}
	if playlist.Owner.Login == "" && playlist.Owner.UID == 0 {
		playlist.Owner.Login = cleanOwner
	}

	// Обработка пагинации
	if playlist.Pager != nil && playlist.Pager.PerPage > 0 && playlist.Pager.Total > len(playlist.Tracks) {
		c.paginatePlaylist(ctx, playlist, func(page int) string {
			return fmt.Sprintf("/users/%s/playlists/%d?rich-tracks=true&page=%d", url.PathEscape(cleanOwner), kind, page)
		})
	}

	return playlist, nil
}

// GetPlaylistByUUID возвращает публичный плейлист по его глобальному UUID.
func (c *YandexMusicClient) GetPlaylistByUUID(ctx context.Context, playlistUUID string) (*YandexPlaylist, error) {
	cleanUUID := strings.TrimSpace(playlistUUID)
	if cleanUUID == "" {
		return nil, errors.New("playlist UUID cannot be empty")
	}

	path := fmt.Sprintf("/playlist/%s?rich-tracks=true", url.PathEscape(cleanUUID))
	req, err := c.newRequest(ctx, http.MethodGet, path)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("сбой запроса плейлиста по UUID Яндекс Музыки: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Резервный эндпоинт во множественном числе /playlists/{uuid}
		altPath := fmt.Sprintf("/playlists/%s?rich-tracks=true", url.PathEscape(cleanUUID))
		if altReq, altErr := c.newRequest(ctx, http.MethodGet, altPath); altErr == nil {
			if altResp, doErr := c.httpClient.Do(altReq); doErr == nil {
				defer altResp.Body.Close()
				if altResp.StatusCode == http.StatusOK {
					var altData struct {
						Result YandexPlaylist `json:"result"`
					}
					if json.NewDecoder(altResp.Body).Decode(&altData) == nil {
						playlist := &altData.Result
						if playlist.PlaylistUUID == "" {
							playlist.PlaylistUUID = cleanUUID
						}
						return playlist, nil
					}
				}
			}
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, yandexStatusError(resp, fmt.Sprintf("плейлиста UUID %s", cleanUUID))
	}

	var data struct {
		Result YandexPlaylist `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON плейлиста по UUID Яндекс Музыки: %w", err)
	}

	playlist := &data.Result
	if playlist.PlaylistUUID == "" {
		playlist.PlaylistUUID = cleanUUID
	}

	if playlist.Pager != nil && playlist.Pager.PerPage > 0 && playlist.Pager.Total > len(playlist.Tracks) {
		c.paginatePlaylist(ctx, playlist, func(page int) string {
			return fmt.Sprintf("/playlist/%s?rich-tracks=true&page=%d", url.PathEscape(cleanUUID), page)
		})
	}

	return playlist, nil
}

func (c *YandexMusicClient) paginatePlaylist(ctx context.Context, playlist *YandexPlaylist, pageURLFunc func(int) string) {
	if playlist.Pager == nil || playlist.Pager.PerPage <= 0 {
		return
	}
	total := playlist.Pager.Total
	perPage := playlist.Pager.PerPage
	currentPage := playlist.Pager.Page + 1
	maxPages := (total + perPage - 1) / perPage
	if maxPages > 50 {
		maxPages = 50
	}

	for page := currentPage; page < maxPages; page++ {
		if ctx.Err() != nil {
			break
		}
		req, err := c.newRequest(ctx, http.MethodGet, pageURLFunc(page))
		if err != nil {
			break
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			break
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			break
		}
		var pageData struct {
			Result struct {
				Tracks []YandexPlaylistTrackItem `json:"tracks"`
			} `json:"result"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&pageData)
		resp.Body.Close()
		if decodeErr != nil || len(pageData.Result.Tracks) == 0 {
			break
		}
		playlist.Tracks = append(playlist.Tracks, pageData.Result.Tracks...)
		if len(playlist.Tracks) >= total {
			break
		}
	}
}

// GetTracks выполняет пакетный запрос метаданных треков через POST /tracks.
func (c *YandexMusicClient) GetTracks(ctx context.Context, trackIDs []string) ([]YandexTrack, error) {
	if len(trackIDs) == 0 {
		return nil, nil
	}

	var allTracks []YandexTrack
	batchSize := 100
	for i := 0; i < len(trackIDs); i += batchSize {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		end := i + batchSize
		if end > len(trackIDs) {
			end = len(trackIDs)
		}
		batch := trackIDs[i:end]

		form := url.Values{}
		form.Set("track-ids", strings.Join(batch, ","))

		req, err := c.newRequest(ctx, http.MethodPost, "/tracks")
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Body = io.NopCloser(strings.NewReader(form.Encode()))

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("сбой пакетного запроса треков Яндекс Музыки: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			err := yandexStatusError(resp, "пакетного запроса треков")
			resp.Body.Close()
			return nil, err
		}

		var data struct {
			Result []YandexTrack `json:"result"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("ошибка парсинга JSON пакета треков Яндекс Музыки: %w", decodeErr)
		}
		allTracks = append(allTracks, data.Result...)
	}

	return allTracks, nil
}

// GetTrackLyrics запрашивает официальный текст песни Яндекс Музыки по точному числовому ID трека.
// Сначала запрашивается синхронизированный формат (format=LRC).
// Если LRC отсутствует (404), автоматически пробуется обычный текст (format=TEXT).
// Возвращает текст песни, признак синхронизированности (synced) и ошибку.
func (c *YandexMusicClient) GetTrackLyrics(ctx context.Context, trackID int64) (string, bool, error) {
	if trackID <= 0 {
		return "", false, errors.New("некорректный ID трека Яндекс Музыки")
	}
	if strings.TrimSpace(c.token) == "" {
		return "", false, ErrYandexNoToken
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}

	if c.signKey == "" {
		return "", false, ErrYandexNoLyricsSignKey
	}
	metadataClient := *c.httpClient
	metadataClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	// 1. Вспомогательная функция запроса метаданных текста для указанного формата
	fetchMetadata := func(format string) (string, error) {
		ts, sign := signYandexLyricsRequest(trackID, c.signKey)
		endpoint := fmt.Sprintf("/tracks/%d/lyrics?format=%s&timeStamp=%d&sign=%s",
			trackID, url.QueryEscape(format), ts, url.QueryEscape(sign))

		req, err := c.newRequest(ctx, http.MethodGet, endpoint)
		if err != nil {
			return "", err
		}

		resp, err := metadataClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("сбой запроса метаданных текста Яндекс Музыки: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return "", ErrYandexLyricsNotFound
		}
		if resp.StatusCode != http.StatusOK {
			return "", yandexStatusError(resp, fmt.Sprintf("текста трека %d (%s)", trackID, format))
		}

		var env struct {
			Result struct {
				DownloadURL string `json:"downloadUrl"`
			} `json:"result"`
		}
		if decErr := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&env); decErr != nil {
			return "", fmt.Errorf("ошибка парсинга JSON метаданных текста: %w", decErr)
		}

		cleanURL := strings.TrimSpace(env.Result.DownloadURL)
		if cleanURL == "" {
			return "", ErrYandexLyricsNotFound
		}
		return cleanURL, nil
	}

	// Сначала запрашиваем синхронизированный LRC
	downloadURL, err := fetchMetadata("LRC")
	if err != nil {
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		if errors.Is(err, ErrYandexLyricsNotFound) {
			// Fallback на обычный текст (TEXT)
			var textErr error
			downloadURL, textErr = fetchMetadata("TEXT")
			if textErr != nil {
				if ctx.Err() != nil {
					return "", false, ctx.Err()
				}
				return "", false, textErr
			}
		} else {
			return "", false, err
		}
	}

	baseURL, err := url.Parse(c.baseURL)
	if err != nil {
		return "", false, err
	}
	u, err := baseURL.Parse(downloadURL)
	if err != nil || !allowedYandexLyricsURL(u, baseURL) {
		return "", false, errors.New("недопустимый адрес хранилища текстов Яндекс Музыки")
	}

	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", false, err
	}
	dlReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	dlReq.Header.Set("Accept", "*/*")
	// ВАЖНО: Заголовок Authorization: OAuth СТРОГО не передается на сервер хранения текста (AWS S3 / Yandex Storage)

	dlClient := &http.Client{
		Timeout:   15 * time.Second,
		Transport: c.httpClient.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errYandexLyricsRedirectLimit
			}
			if !allowedYandexLyricsURL(req.URL, baseURL) {
				return errors.New("недопустимый адрес перенаправления хранилища текстов")
			}
			req.Header.Del("Authorization")
			return nil
		},
	}

	dlResp, err := dlClient.Do(dlReq)
	if err != nil {
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		if errors.Is(err, errYandexLyricsRedirectLimit) {
			return "", false, errYandexLyricsRedirectLimit
		}
		return "", false, errors.New("сбой скачивания файла текста Яндекс Музыки")
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		if dlResp.StatusCode == http.StatusNotFound {
			return "", false, ErrYandexLyricsNotFound
		}
		return "", false, fmt.Errorf("сервер хранения текста вернул HTTP %d", dlResp.StatusCode)
	}

	const maxLyricsBytes = 512 * 1024 // 512 KB
	rawBytes, err := io.ReadAll(io.LimitReader(dlResp.Body, maxLyricsBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		return "", false, fmt.Errorf("ошибка чтения тела текста: %w", err)
	}
	if len(rawBytes) > maxLyricsBytes {
		return "", false, fmt.Errorf("размер файла текста превышает допустимый лимит 512 КБ")
	}

	content := strings.TrimSpace(string(rawBytes))
	if content == "" {
		return "", false, ErrYandexLyricsNotFound
	}

	lower := strings.ToLower(content)
	if strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html") || (strings.Contains(lower, "<head>") && strings.Contains(lower, "<body>")) {
		return "", false, fmt.Errorf("сервер вернул HTML-страницу вместо текста песни")
	}

	if (strings.HasPrefix(content, "{") && strings.HasSuffix(content, "}")) || (strings.HasPrefix(content, "[") && strings.HasSuffix(content, "]")) {
		var jsTest any
		if json.Unmarshal([]byte(content), &jsTest) == nil {
			return "", false, fmt.Errorf("сервер вернул JSON-ответ вместо текста песни")
		}
	}

	synced := lrcTimestampRegex.MatchString(content)
	return content, synced, nil
}

// allowedYandexLyricsURL allows the API origin and the known Yandex lyrics CDN.
// The injected API origin also permits same-origin requests from local HTTP fixtures.
func allowedYandexLyricsURL(target, api *url.URL) bool {
	if target.User != nil || target.Host == "" {
		return false
	}
	if target.Scheme == api.Scheme && strings.EqualFold(target.Host, api.Host) {
		return true
	}
	return target.Scheme == "https" && strings.EqualFold(target.Host, "music-lyrics.s3-private.mds.yandex.net")
}
