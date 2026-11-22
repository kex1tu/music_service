package resolver

import "music-manager/internal/db"

// CatalogReleaseSummary содержит краткую информацию о найденном релизе для списка выбора.
type CatalogReleaseSummary struct {
	ProviderCredits  []db.RadarCredit `json:"-"`
	DeezerAlbumLinks []string         `json:"-"`
	ReleaseType      string           `json:"release_type,omitempty"`
	ID               string           `json:"id"`
	Source           string           `json:"source"` // "deezer", "musicbrainz", "shazam"
	Title            string           `json:"title"`
	Artist           string           `json:"artist"`
	CoverURL         string           `json:"cover_url,omitempty"`
	Year             int              `json:"year,omitempty"`
	Date             string           `json:"date,omitempty"`
	TrackCount       int              `json:"track_count"`
	Label            string           `json:"label,omitempty"`
	Country          string           `json:"country,omitempty"`
	Barcode          string           `json:"barcode,omitempty"`
	Score            int              `json:"score,omitempty"`
}

// CatalogTrackDetail представляет отдельную дорожку в треклисте релиза.
type CatalogTrackDetail struct {
	Position     int      `json:"position"`
	TrackNumber  int      `json:"track_number"`
	DiscNumber   int      `json:"disc_number"`
	Title        string   `json:"title"`
	Artist       string   `json:"artist"` // Исполнители через "\\" для мульти-артистов
	Artists      []string `json:"artists,omitempty"`
	Duration     int      `json:"duration"` // В секундах
	DurationMs   int      `json:"duration_ms,omitempty"`
	ISRC         string   `json:"isrc,omitempty"`
	ReleaseTitle string   `json:"release_title,omitempty"`
}

// CatalogReleaseDetails содержит полные метаданные релиза с треклистом.
type CatalogReleaseDetails struct {
	ID         string               `json:"id"`
	Source     string               `json:"source"` // "deezer", "musicbrainz", "shazam"
	Title      string               `json:"title"`
	Artist     string               `json:"artist"`
	CoverURL   string               `json:"cover_url,omitempty"`
	Year       int                  `json:"year,omitempty"`
	Date       string               `json:"date,omitempty"`
	Genre      string               `json:"genre,omitempty"`
	Genres     []string             `json:"genres,omitempty"`
	Label      string               `json:"label,omitempty"`
	TrackCount int                  `json:"track_count"`
	Tracks     []CatalogTrackDetail `json:"tracks"`
}

// CatalogSourceResult группирует результаты одного провайдера метаданных.
type CatalogSourceResult struct {
	Count int                     `json:"count"`
	Items []CatalogReleaseSummary `json:"items"`
	Error string                  `json:"error,omitempty"`
}

// CatalogSearchResponse агрегирует результаты поиска по всем трем базам данных.
type CatalogSearchResponse struct {
	Query       string              `json:"query"`
	Deezer      CatalogSourceResult `json:"deezer"`
	MusicBrainz CatalogSourceResult `json:"musicbrainz"`
	Shazam      CatalogSourceResult `json:"shazam"`
}
