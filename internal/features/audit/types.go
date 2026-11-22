package audit

import (
	"time"

	"music-manager/internal/db"
	"music-manager/internal/id3"
)

// ============================================================================
// 1. Audit Summary & High-level Statistics
// ============================================================================

// AuditSummary представляет сводный отчет о состоянии и дефектах медиатеки.
type AuditSummary struct {
	TotalTracks           int `json:"total_tracks"`
	TotalAlbums           int `json:"total_albums"`
	MissingLyricsCount    int `json:"missing_lyrics_count"`
	MissingCoversCount    int `json:"missing_covers_count"`
	DuplicatesCount       int `json:"duplicates_count"`
	IncompleteAlbumsCount int `json:"incomplete_albums_count"`
	InvalidPathsCount     int `json:"invalid_paths_count"`
	NonEnGenresCount      int `json:"non_en_genres_count"`
	TagIssuesCount        int `json:"tag_issues_count"`
}

// SearchTrackItem представляет элемент трека в результатах поиска по медиатеке.
type SearchTrackItem struct {
	ID           int64   `json:"id"`
	Title        string  `json:"title"`
	Artist       string  `json:"artist"`
	Album        string  `json:"album"`
	Year         int     `json:"year"`
	Duration     float64 `json:"duration"`
	Path         string  `json:"path"`
	LyricsStatus string  `json:"lyrics_status"`
	HasCover     bool    `json:"has_cover"`
	CoverPath    string  `json:"cover_path"`
}

// SearchAlbumItem представляет элемент альбома в результатах поиска.
type SearchAlbumItem struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	Year      int    `json:"year"`
	HasCover  bool   `json:"has_cover"`
	CoverPath string `json:"cover_path"`
	Path      string `json:"path"`
}

// SearchArtistItem представляет элемент артиста в результатах поиска.
type SearchArtistItem struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// SearchLibraryResult содержит сгруппированные результаты поиска по медиатеке.
type SearchLibraryResult struct {
	Tracks  []SearchTrackItem  `json:"tracks"`
	Albums  []SearchAlbumItem  `json:"albums"`
	Artists []SearchArtistItem `json:"artists"`
}

// ============================================================================
// 2. Duplicates Audit & Resolution
// ============================================================================

// DuplicateFile представляет отдельный файл дубликата трека.
type DuplicateFile struct {
	Path       string `json:"path"`
	Filename   string `json:"filename"`
	SizeBytes  *int64 `json:"size_bytes,omitempty"`
	Duration   *int   `json:"duration,omitempty"`
	Bitrate    *int   `json:"bitrate,omitempty"`
	ModifiedAt string `json:"modified_at"`
}

// DuplicateGroup объединяет группу файлов, признанных дубликатами.
type DuplicateGroup struct {
	Key      string          `json:"key"`
	Title    string          `json:"title"`
	Artist   string          `json:"artist"`
	AlbumDir string          `json:"album_dir"`
	Album    string          `json:"album"`
	Tracks   []DuplicateFile `json:"tracks"`
}

// DuplicatesResponse — HTTP-ответ со списком групп дубликатов.
type DuplicatesResponse struct {
	Groups              []DuplicateGroup `json:"groups"`
	TotalDuplicateFiles int              `json:"total_duplicate_files"`
}

// FixDuplicatesRequest — запрос на удаление выбранных файлов дубликатов.
type FixDuplicatesRequest struct {
	GroupKey      string   `json:"group_key,omitempty"`
	AlbumPath     string   `json:"album_path,omitempty"`
	PathsToDelete []string `json:"paths_to_delete"`
}

// ResolveDuplicatesRequest — запрос на разрешение конкретной группы дубликатов.
type ResolveDuplicatesRequest struct {
	GroupKey      string   `json:"group_key,omitempty"`
	KeepPath      string   `json:"keep_path,omitempty"`
	PathsToDelete []string `json:"paths_to_delete,omitempty"`
}

// FixDuplicatesResponse — результат операции разрешения дубликатов.
type FixDuplicatesResponse struct {
	ReviewGroups int      `json:"review_groups,omitempty"`
	Success      bool     `json:"success"`
	DeletedCount int      `json:"deleted_count"`
	DeletedFiles []string `json:"deleted_files"`
	Errors       []string `json:"errors"`
}

// ============================================================================
// 3. Incomplete Albums & Backfill
// ============================================================================

// IncompleteAlbum представляет неполный альбом с дефицитом дорожек.
type IncompleteAlbum struct {
	Path                string            `json:"path"`
	Album               string            `json:"album"`
	Artist              string            `json:"artist"`
	Year                *int              `json:"year"`
	HasCover            bool              `json:"has_cover"`
	CoverURL            *string           `json:"cover_url"`
	PresentCount        int               `json:"present_count"`
	ExpectedCount       int               `json:"expected_count"`
	MissingTracks       []db.CatalogTrack `json:"missing_tracks"`
	MissingTrackNumbers []int             `json:"missing_track_numbers"`
	Completeness        string            `json:"completeness"`
	IsCatalogMatched    bool              `json:"is_catalog_matched"`
	DeezerAlbumID       *string           `json:"deezer_album_id"`
}

// IncompleteAlbumsResponse — ответ со списком неполных альбомов.
type IncompleteAlbumsResponse struct {
	Albums                []IncompleteAlbum `json:"albums"`
	TotalIncompleteAlbums int               `json:"total_incomplete_albums"`
	TotalMissingTracks    int               `json:"total_missing_tracks"`
}

// DownloadMissingRequest — запрос на запуск бэкфилла недостающих треков.
type DownloadMissingRequest struct {
	Positions     []db.BackfillSelection `json:"positions"`
	Path          string                 `json:"path"`
	DeezerAlbumID *string                `json:"deezer_album_id"`
	TrackURLs     []string               `json:"track_urls"`
	TrackNumbers  []int                  `json:"track_numbers"`
}

// BackfillJobInfo — базовая информация о созданной задаче бэкфилла.
type BackfillJobInfo struct {
	ID              string  `json:"id"`
	Status          string  `json:"status"`
	IsBackfill      bool    `json:"is_backfill"`
	TargetAlbumPath *string `json:"target_album_path,omitempty"`
	AlbumTitle      *string `json:"album_title,omitempty"`
	ArtistName      *string `json:"artist_name,omitempty"`
}

// BackfillEditorDataResponse — метаданные альбома и треков для Puddletag-редактора после бэкфилла.
type BackfillEditorDataResponse struct {
	Album           map[string]any `json:"album"`
	Tracks          []any          `json:"tracks"`
	SourceDir       string         `json:"source_dir"`
	CoverPath       string         `json:"cover_path"`
	HasCover        bool           `json:"has_cover"`
	JobIDs          []string       `json:"job_ids"`
	MissingCount    int            `json:"missing_count"`
	BackfilledCount int            `json:"backfilled_count"`
}

// BackfillCoverRequest — запрос на дозагрузку обложки для одного альбома.
type BackfillCoverRequest struct {
	FolderPath string `json:"folder_path"`
	AlbumID    int64  `json:"album_id,omitempty"`
}

// BackfillCoverResponse — результат дозагрузки обложки альбома.
type BackfillCoverResponse struct {
	Success       bool   `json:"success"`
	FolderPath    string `json:"folder_path"`
	TracksUpdated int    `json:"tracks_updated"`
	Album         string `json:"album"`
	Artist        string `json:"artist"`
}

// BatchProgress — унифицированный прогресс фоновых пакетных задач (обложки, тексты).
type BatchProgress struct {
	IsRunning    bool   `json:"is_running"`
	Total        int    `json:"total"`
	Current      int    `json:"current"`
	Found        int    `json:"found"`
	Failed       int    `json:"failed"`
	CurrentTrack string `json:"current_track,omitempty"`
	CurrentAlbum string `json:"current_album,omitempty"`
}

// ============================================================================
// 4. Invalid Paths & Reorganization
// ============================================================================

// InvalidPathRecord представляет трек с нарушением канонической структуры путей.
type InvalidPathRecord struct {
	FilePath            string `json:"path"`
	SuggestedPath       string `json:"suggested_path"`
	PathViolationReason string `json:"reason"`
}

// FixPathRequest — запрос на приведение пути к каноническому стандарту.
type FixPathRequest struct {
	Path      string `json:"path"`
	Overwrite bool   `json:"overwrite"`
}

// FixPathResponse — результат перемещения одного трека.
type FixPathResponse struct {
	Success    bool   `json:"success"`
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
	Message    string `json:"message"`
}

// FixAllPathsResponse — результат массового исправления неканонических путей.
type FixAllPathsResponse struct {
	Success      bool     `json:"success"`
	TotalInvalid int      `json:"total_invalid"`
	FixedCount   int      `json:"fixed_count"`
	Errors       []string `json:"errors"`
}

// ReorganizeLibraryResponse — результат полной реорганизации медиатеки.
type ReorganizeLibraryResponse struct {
	Success     bool     `json:"success"`
	TotalTracks int      `json:"total_tracks"`
	MovedTracks int      `json:"moved_tracks"`
	TotalAlbums int      `json:"total_albums"`
	Errors      []string `json:"errors"`
}

// NavidromeReport — отчет о нормализации прав и структуры для Navidrome.
type NavidromeReport struct {
	DirectoriesFixed int `json:"directories_fixed"`
	FilesPermsFixed  int `json:"files_perms_fixed"`
	TagsNormalized   int `json:"tags_normalized"`
	SinglesFixed     int `json:"singles_fixed"`
	TotalScanned     int `json:"total_scanned"`
}

// ============================================================================
// 5. Tag Issues & Catalog Matching
// ============================================================================

// ProposedTagChange содержит вычисленные корректные теги для автоисправления.
type ProposedTagChange struct {
	Title       string   `json:"title"`
	Artist      string   `json:"artist"`
	Artists     []string `json:"artists"`
	Album       string   `json:"album"`
	AlbumArtist string   `json:"album_artist,omitempty"`
	Year        int      `json:"year"`
}

// TagIssue представляет трек с обнаруженными дефектами ID3-тегов.
type TagIssue struct {
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

// TagIssuesResponse — список обнаруженных проблем с тегами.
type TagIssuesResponse struct {
	Issues     []TagIssue `json:"issues"`
	TotalCount int        `json:"total_count"`
}

// FixTagIssueRequest — запрос на исправление тегов одного трека.
type FixTagIssueRequest struct {
	Path   string `json:"path"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album"`
	Year   int    `json:"year"`
}

// FixTagIssueResponse — результат исправления одного трека.
type FixTagIssueResponse struct {
	Success bool           `json:"success"`
	Path    string         `json:"path"`
	Tags    *id3.TrackTags `json:"tags,omitempty"`
}

// FixBatchResponse — результат пакетного исправления тегов.
type FixBatchResponse struct {
	Success    bool     `json:"success"`
	FixedCount int      `json:"fixed_count"`
	TotalFound int      `json:"total_found"`
	Errors     []string `json:"errors"`
}

// FixSelectedTagsRequest — запрос на массовое исправление выбранных треков.
type FixSelectedTagsRequest struct {
	Items []FixTagIssueRequest `json:"items"`
}

// CatalogTrackMatch представляет совпадение трека в онлайн-каталогах.
type CatalogTrackMatch struct {
	Source      string   `json:"source"`
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

// TrackCatalogMatchResult — результат сверки трека с каталогами.
type TrackCatalogMatchResult struct {
	Current any                 `json:"current"`
	Matches []CatalogTrackMatch `json:"matches"`
}

// AlbumCatalogTrackDiff содержит Diff между локальным треком и каталожным источником.
type AlbumCatalogTrackDiff struct {
	TrackNumber        int      `json:"track_number"`
	LocalTitle         string   `json:"local_title"`
	LocalArtist        string   `json:"local_artist"`
	LocalPath          string   `json:"local_path"`
	CatalogTitle       string   `json:"catalog_title"`
	CatalogArtist      string   `json:"catalog_artist"`
	CatalogArtists     []string `json:"catalog_artists"`
	CatalogDuration    int      `json:"catalog_duration"`
	CatalogTrackNumber int      `json:"catalog_track_number"`
	IsMatched          bool     `json:"is_matched"`
}

// AlbumCatalogMatchResponse — результат сверки альбома с каталогом Deezer/Apple Music.
type AlbumCatalogMatchResponse struct {
	FolderPath      string                  `json:"folder_path"`
	AlbumTitle      string                  `json:"album_title"`
	AlbumArtist     string                  `json:"album_artist"`
	CatalogAlbum    string                  `json:"catalog_album"`
	CatalogArtist   string                  `json:"catalog_artist"`
	CatalogCoverURL string                  `json:"catalog_cover_url"`
	CatalogYear     int                     `json:"catalog_year"`
	CatalogGenre    string                  `json:"catalog_genre"`
	Source          string                  `json:"source"`
	Tracks          []AlbumCatalogTrackDiff `json:"tracks"`
}

// ApplyCatalogTagsRequest — запрос на применение каталожных тегов к файлу.
type ApplyCatalogTagsRequest struct {
	Path        string `json:"path"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	Year        int    `json:"year"`
	TrackNumber int    `json:"track_number"`
	CoverURL    string `json:"cover_url"`
}

// ApplyCatalogAlbumRequest — запрос на применение каталожных тегов ко всему альбому.
type ApplyCatalogAlbumRequest struct {
	FolderPath  string                       `json:"folder_path"`
	Album       string                       `json:"album"`
	AlbumArtist string                       `json:"album_artist"`
	Year        int                          `json:"year,omitempty"`
	Genre       string                       `json:"genre,omitempty"`
	CoverURL    string                       `json:"cover_url,omitempty"`
	Tracks      []ApplyCatalogAlbumTrackItem `json:"tracks"`
}

// ApplyCatalogAlbumTrackItem — элемент списка треков при обновлении альбома из каталога.
type ApplyCatalogAlbumTrackItem struct {
	Path        string `json:"path"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	TrackNumber int    `json:"track_number"`
}

// ============================================================================
// 6. Missing Lyrics, Covers & Non-EN Genres
// ============================================================================

// MissingLyricsItem представляет трек с отсутствующим текстом песни.
type MissingLyricsItem struct {
	Path      string `json:"path"`
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	Album     string `json:"album"`
	Directory string `json:"directory"`
}

// MissingCoverItem представляет альбом без обложки.
type MissingCoverItem struct {
	Path       string `json:"path"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Album      string `json:"album"`
	IsAlbumDir bool   `json:"is_album_dir"`
	TrackCount int    `json:"track_count"`
}

// NonEnGenreItem представляет трек с неанглийским или неканоническим жанром.
type NonEnGenreItem struct {
	Path           string `json:"path"`
	CurrentGenre   string `json:"current_genre"`
	CanonicalGenre string `json:"canonical_genre"`
	IsAutoFixed    bool   `json:"is_auto_fixed"`
}

// RetagGenresResponse — результат пакетной нормализации жанров.
type RetagGenresResponse struct {
	Success      bool             `json:"success"`
	UpdatedCount int              `json:"updated_count"`
	TotalChecked int              `json:"total_checked"`
	Items        []NonEnGenreItem `json:"items"`
	Errors       []string         `json:"errors"`
}

// ============================================================================
// 7. Audit Journal Entity
// ============================================================================

// AuditIssue представляет запись в таблице audit_issues SQLite.
type AuditIssue struct {
	ID          int64     `json:"id"`
	IssueType   string    `json:"issue_type"`
	EntityType  string    `json:"entity_type"`
	EntityID    *int64    `json:"entity_id,omitempty"`
	FilePath    string    `json:"file_path,omitempty"`
	Details     string    `json:"details,omitempty"`
	IsResolved  bool      `json:"is_resolved"`
	IsAutoFixed bool      `json:"is_auto_fixed"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ============================================================================
// 8. Compatibility Aliases
// ============================================================================

type AuditSummaryResponse = AuditSummary
type TagIssueItem = TagIssue
type DuplicateTrackItem = DuplicateFile
type DuplicateGroupItem = DuplicateGroup
type DuplicateReport = DuplicatesResponse
type DuplicateFixResult = FixDuplicatesResponse
type TagIssuesReport = TagIssuesResponse
type TagFixSummary = FixBatchResponse
type BackfillJobResult = BackfillJobInfo
type InvalidPathItem = InvalidPathRecord
type BatchProgressResponse = BatchProgress
type BatchCoversProgressResponse = BatchProgress
type AlbumMatchResponse = map[string]any

type NormalizeNavidromeResponse struct {
	Success bool            `json:"success"`
	Report  NavidromeReport `json:"report"`
}
