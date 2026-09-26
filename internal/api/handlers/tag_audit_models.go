package handlers

// TagIssueItem представляет трек с дефектами тегов и предлагаемым исправлением.
type TagIssueItem struct {
	ID          int64             `json:"id"`
	FilePath    string            `json:"file_path"`
	Title       string            `json:"title"`
	Artist      string            `json:"artist"`
	Album       string            `json:"album"`
	Year        int               `json:"year"`
	TrackNumber int               `json:"track_number"`
	Duration    float64           `json:"duration"`
	Issues      []string          `json:"issues"`
	Severity    string            `json:"severity"` // "safe_autofix", "review_needed", "info"
	Proposed    ProposedTagChange `json:"proposed"`
}

// ProposedTagChange содержит вычисленные корректные теги для автоисправления.
type ProposedTagChange struct {
	Title       string   `json:"title"`
	Artist      string   `json:"artist"`
	Artists     []string `json:"artists"`
	Album       string   `json:"album"`
	AlbumArtist string   `json:"album_artist,omitempty"`
	Year        int      `json:"year"`
}

// CatalogTrackMatch представляет результат поиска трека в онлайн-каталогах.
type CatalogTrackMatch struct {
	Source      string   `json:"source"` // "yandex", "youtube_music", "deezer", "itunes"
	Title       string   `json:"title"`
	Artist      string   `json:"artist"`
	Artists     []string `json:"artists,omitempty"`
	Album       string   `json:"album"`
	Year        int      `json:"year"`
	TrackNumber int      `json:"track_number"`
	Duration    int      `json:"duration,omitempty"`
	CoverURL    string   `json:"cover_url"`
	IsBestFeat  bool     `json:"is_best_feat,omitempty"`
	MatchScore  int      `json:"match_score"` // 0..100%
}

// FixTagIssueRequest представляет тело запроса на исправление тегов одного трека.
type FixTagIssueRequest struct {
	Path   string `json:"path"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album"`
	Year   int    `json:"year"`
}

// FixSelectedTagsRequest представляет запрос на пакетное исправление выбранных чекбоксами треков.
type FixSelectedTagsRequest struct {
	Items []FixTagIssueRequest `json:"items"`
}

// ApplyCatalogTagsRequest представляет запрос на применение каталожных тегов к файлу.
type ApplyCatalogTagsRequest struct {
	Path        string `json:"path"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	Year        int    `json:"year"`
	TrackNumber int    `json:"track_number"`
	CoverURL    string `json:"cover_url"`
}
