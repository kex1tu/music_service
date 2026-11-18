package radio

// WaveStatsResponse содержит статистику радиовещания и интеграций.
type WaveStatsResponse struct {
	TopGenres          []string `json:"top_genres"`
	NavidromeAvailable bool     `json:"navidrome_available"`
	NavidromeWarning   string   `json:"navidrome_warning,omitempty"`
	SubsonicConfigured bool     `json:"subsonic_configured"`
	WaveSize           int      `json:"wave_size"`
	StreamTokenSet     bool     `json:"stream_token_set"`
	StreamToken        string   `json:"stream_token,omitempty"`
}

// WaveQueueResponse описывает очередь воспроизведения «Моя Волна».
type WaveQueueResponse struct {
	Tracks any `json:"tracks"`
	Count  int `json:"count"`
}

// NowPlayingResponse содержит информацию о текущем и недавних треках в эфире станции.
type NowPlayingResponse struct {
	CurrentTrack any `json:"current_track"`
	History      any `json:"history"`
}

// SyncRadioRequest задает параметры синхронизации радиостанции с Navidrome.
type SyncRadioRequest struct {
	StreamURL string `json:"stream_url"`
	Name      string `json:"name"`
}

// SyncRadioResponse содержит результат синхронизации радиостанции с Navidrome.
type SyncRadioResponse struct {
	Status    string `json:"status"`
	Name      string `json:"name"`
	StreamURL string `json:"stream_url"`
}

// GenerateM3UResponse содержит путь к сгенерированному плейлисту «Моя Волна.m3u».
type GenerateM3UResponse struct {
	Status       string `json:"status"`
	PlaylistPath string `json:"playlist_path"`
}

// GenresResponse содержит список доступных жанров медиатеки.
type GenresResponse struct {
	Genres []string `json:"genres"`
}
