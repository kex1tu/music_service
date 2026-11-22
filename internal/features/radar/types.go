package radar

import (
	"music-manager/internal/db"
)

// TrackedArtist описывает настройки артиста для Радара.
type TrackedArtist = db.TrackedArtistEntity

// RadarRelease описывает отдельный релиз Радара новинок или дискографии.
type RadarRelease = db.RadarReleaseEntity

// FeedResponse представляет пагинированную ленту новинок.
type FeedResponse struct {
	Items  []RadarRelease `json:"items"`
	Total  int            `json:"total"`
	Page   int            `json:"page"`
	Limit  int            `json:"limit"`
	Unread int            `json:"unread"`
}

// TrackedArtistsResponse представляет список артистов медиатеки.
type TrackedArtistsResponse struct {
	Items []TrackedArtist `json:"items"`
	Total int             `json:"total"`
}

// CustomLinksRequest обновляет пользовательские идентификаторы артиста в каталогах.
type CustomLinksRequest struct {
	ArtistID      int64  `json:"artist_id"`
	Alias         string `json:"alias"`
	DeezerID      string `json:"deezer_id"`
	MusicBrainzID string `json:"musicbrainz_id"`
	YandexID      string `json:"yandex_id"`
}

// ArtistDiscographyResponse содержит дискографию артиста и его настройки поиска.
type ArtistDiscographyResponse struct {
	Artist   string         `json:"artist"`
	Tracked  *TrackedArtist `json:"tracked,omitempty"`
	Releases []RadarRelease `json:"releases"`
}

// ScanRequest содержит параметры запуска сканирования.
type ScanRequest struct {
	ArtistID int64  `json:"artist_id,omitempty"`
	Artist   string `json:"artist"`
}

// SetReleaseStatusRequest содержит новый статус релиза.
type SetReleaseStatusRequest struct {
	Status string `json:"status"`
}

// UnreadCountResponse содержит количество непросмотренных новинок.
type UnreadCountResponse struct {
	Unread int `json:"unread"`
}
