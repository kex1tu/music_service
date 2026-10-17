package filemanager

import "time"

// FileItem представляет отдельный элемент файловой системы (аудиофайл или директорию) для медиатеки.
type FileItem struct {
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	IsDir        bool      `json:"is_dir"`
	Size         int64     `json:"size"`
	ModTime      time.Time `json:"mod_time"`
	Extension    string    `json:"extension,omitempty"`
	Title        string    `json:"title,omitempty"`
	Artist       string    `json:"artist,omitempty"`
	Album        string    `json:"album,omitempty"`
	Duration     float64   `json:"duration,omitempty"`
	Bitrate      int       `json:"bitrate,omitempty"`
	HasLyrics    bool      `json:"has_lyrics,omitempty"`
	LyricsStatus string    `json:"lyrics_status,omitempty"`
	TrackID      int64     `json:"track_id,omitempty"`
	TrackHash    string    `json:"track_hash,omitempty"`
	HasCover     bool      `json:"has_cover,omitempty"`
}

// RenameRequest задает параметры переименования или перемещения файла/каталога.
type RenameRequest struct {
	OldPath string `json:"oldPath"`
	NewPath string `json:"newPath"`
}

// RenameResponse содержит результат операции переименования.
type RenameResponse struct {
	Success        bool     `json:"success"`
	OldPath        string   `json:"oldPath"`
	NewPath        string   `json:"newPath"`
	AffectedFiles  []string `json:"affected_files,omitempty"`
	AffectedTracks int      `json:"affected_tracks,omitempty"`
}

// DeleteResponse содержит результат удаления файла или каталога.
type DeleteResponse struct {
	Success       bool   `json:"success"`
	Message       string `json:"message"`
	DeletedTracks int    `json:"deleted_tracks,omitempty"`
	Path          string `json:"path"`
}

// TrackMetadataResponse представляет полную информацию о треке для веб-плеера и клиентов.
type TrackMetadataResponse struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Artist      string  `json:"artist"`
	Album       string  `json:"album"`
	DurationSec int     `json:"duration_sec"`
	BitrateKbps int     `json:"bitrate_kbps"`
	SampleRate  int     `json:"sample_rate"`
	TrackNumber int     `json:"track_number,omitempty"`
	Year        int     `json:"year,omitempty"`
	Genre       string  `json:"genre,omitempty"`
	Codec       string  `json:"codec"`
	FileSize    int64   `json:"file_size"`
	HasCover    bool    `json:"has_cover"`
	StreamURL   string  `json:"stream_url"`
	CoverURL    string  `json:"cover_url"`
	FilePath    string  `json:"file_path,omitempty"`
}

// LibraryTrackItem описывает трек при выводе каталога медиатеки.
type LibraryTrackItem struct {
	ID        int64   `json:"id"`
	FileHash  string  `json:"file_hash"`
	Title     string  `json:"title"`
	Artist    string  `json:"artist"`
	Album     string  `json:"album"`
	Duration  float64 `json:"duration"`
	Bitrate   int     `json:"bitrate"`
	FilePath  string  `json:"file_path"`
	StreamURL string  `json:"stream_url"`
	CoverURL  string  `json:"cover_url"`
}

// LibraryTracksResponse представляет пагинированный ответ списка треков медиатеки.
type LibraryTracksResponse struct {
	Tracks []LibraryTrackItem `json:"tracks"`
	Total  int                `json:"total"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
}

// SearchTrackItem описывает найденный трек в глобальном поиске.
type SearchTrackItem struct {
	ID           int64   `json:"id"`
	Title        string  `json:"title"`
	Artist       string  `json:"artist"`
	Album        string  `json:"album"`
	Duration     float64 `json:"duration"`
	Bitrate      int     `json:"bitrate"`
	FilePath     string  `json:"file_path"`
	LyricsStatus string  `json:"lyrics_status"`
	StreamURL    string  `json:"stream_url"`
}

// SearchAlbumItem описывает найденный альбом в глобальном поиске.
type SearchAlbumItem struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	AlbumArtist string `json:"album_artist,omitempty"`
	Year        int    `json:"year,omitempty"`
	FolderPath  string `json:"folder_path"`
	TrackCount  int    `json:"track_count"`
	HasCover    bool   `json:"has_cover"`
}

// SearchArtistItem описывает найденного исполнителя в глобальном поиске.
type SearchArtistItem struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	TrackCount int    `json:"track_count"`
	FolderPath string `json:"folder_path"`
	FirstPath  string `json:"first_path"`
}

// SearchLibraryResult объединяет результаты поиска по трекам, альбомам и артистам.
type SearchLibraryResult struct {
	Tracks  []SearchTrackItem  `json:"tracks"`
	Albums  []SearchAlbumItem  `json:"albums"`
	Artists []SearchArtistItem `json:"artists"`
}

// ReorganizeResponse описывает результаты операции массовой реорганизации файлов.
type ReorganizeResponse struct {
	Success   bool     `json:"success"`
	Processed int      `json:"processed"`
	Renamed   int      `json:"renamed"`
	Errors    []string `json:"errors,omitempty"`
}
