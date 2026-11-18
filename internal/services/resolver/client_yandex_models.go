package resolver

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

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
