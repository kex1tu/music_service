package radar

import (
	"music-manager/internal/db"
)

// TrackedArtist описывает артиста в каталоге отслеживания Радара.
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

// TrackedArtistsResponse представляет список отслеживаемых артистов.
type TrackedArtistsResponse struct {
	Items []TrackedArtist `json:"items"`
	Total int             `json:"total"`
}

// AddTrackedArtistRequest содержит имя артиста для добавления в отслеживание.
type AddTrackedArtistRequest struct {
	Name string `json:"name"`
}

// FollowArtistRequest управляет статусом отслеживания артиста.
type FollowArtistRequest struct {
	ArtistID int64 `json:"artist_id"`
	Followed bool  `json:"followed"`
}

// AutoDownloadRequest управляет автозагрузкой новых релизов артиста.
type AutoDownloadRequest struct {
	ArtistID     int64 `json:"artist_id"`
	AutoDownload bool  `json:"auto_download"`
}

// CustomLinksRequest обновляет пользовательские идентификаторы артиста в каталогах.
type CustomLinksRequest struct {
	ArtistID      int64  `json:"artist_id"`
	Alias         string `json:"alias"`
	DeezerID      string `json:"deezer_id"`
	MusicBrainzID string `json:"musicbrainz_id"`
	YandexID      string `json:"yandex_id"`
}

// ArtistDiscographyResponse содержит дискографию артиста и его статус отслеживания.
type ArtistDiscographyResponse struct {
	Artist   string         `json:"artist"`
	Tracked  *TrackedArtist `json:"tracked,omitempty"`
	Releases []RadarRelease `json:"releases"`
}

// ScanRequest содержит параметры запуска сканирования.
type ScanRequest struct {
	Artist string `json:"artist"`
}

// SetReleaseStatusRequest содержит новый статус релиза.
type SetReleaseStatusRequest struct {
	Status string `json:"status"`
}

// UnreadCountResponse содержит количество непросмотренных новинок.
type UnreadCountResponse struct {
	Unread int `json:"unread"`
}
