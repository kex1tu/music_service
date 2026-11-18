package radar

import "time"

// TrackedArtist описывает артиста в каталоге отслеживания Радара.
type TrackedArtist struct {
	ID                int64      `json:"id"`
	Name              string     `json:"name"`
	CleanName         string     `json:"clean_name"`
	IsFollowed        bool       `json:"is_followed"`
	AutoDownload      bool       `json:"auto_download"`
	CustomSearchAlias string     `json:"custom_search_alias,omitempty"`
	DeezerID          string     `json:"deezer_id,omitempty"`
	MusicBrainzID     string     `json:"musicbrainz_id,omitempty"`
	YandexID          string     `json:"yandex_id,omitempty"`
	LastCheckedAt     *time.Time `json:"last_checked_at,omitempty"`
	AlbumCount        int        `json:"album_count"`
	TrackCount        int        `json:"track_count"`
	GapCount          int        `json:"gap_count"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// RadarRelease описывает отдельный релиз Радара новинок или дискографии.
type RadarRelease struct {
	ID              int64     `json:"id"`
	ArtistName      string    `json:"artist_name"`
	CleanArtistName string    `json:"clean_artist_name"`
	Title           string    `json:"title"`
	CleanTitle      string    `json:"clean_title"`
	ReleaseType     string    `json:"release_type"` // album, single, ep, compilation
	ReleaseDate     string    `json:"release_date"`
	Year            int       `json:"year"`
	CoverURL        string    `json:"cover_url"`
	TrackCount      int       `json:"track_count"`
	DeezerID        string    `json:"deezer_id,omitempty"`
	MusicBrainzID   string    `json:"musicbrainz_id,omitempty"`
	ShazamID        string    `json:"shazam_id,omitempty"`
	SourceURL       string    `json:"source_url"`
	Status          string    `json:"status"` // new, in_library, version_exists, downloaded, ignored, missing
	IsRadar         bool      `json:"is_radar"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

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
