package id3

// TrackMeta содержит нормализованные метаданные трека для передачи в FastAPI через NDJSON.
type TrackMeta struct {
	RelPath      string  `json:"rel_path"`
	Title        string  `json:"title"`
	Artist       string  `json:"artist"`
	Album        string  `json:"album"`
	AlbumArtist  string  `json:"album_artist"`
	Year         int     `json:"year"`
	TrackNumber  int     `json:"track_number"`
	TrackTotal   int     `json:"track_total"`
	DiscNumber   int     `json:"disc_number"`
	DiscTotal    int     `json:"disc_total"`
	Duration     float64 `json:"duration"`
	HasCover     bool    `json:"has_cover"`
	LyricsStatus string  `json:"lyrics_status"` // "lrc" | "txt" | "none"
	Size         int64   `json:"size"`
	Mtime        float64 `json:"mtime"`
}
