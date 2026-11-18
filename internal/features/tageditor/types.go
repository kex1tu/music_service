package tageditor

import (
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
)

// ============================================================================
// 1. Core Domain Models & Aliases
// ============================================================================

// TrackTags представляет полные метаданные аудиодорожки в ID3v2.4 и базе данных.
type TrackTags = id3.TrackTags

// CoverInfo содержит бинарные данные и метаданные обложки альбома/трека.
type CoverInfo = id3.CoverInfo

// AlbumSummaryDTO содержит сводку метаданных альбома или директории.
type AlbumSummaryDTO struct {
	AlbumArtist string `json:"album_artist"`
	Album       string `json:"album"`
	Year        *int   `json:"year"`
	Genre       string `json:"genre"`
	DiscTotal   int    `json:"disc_total"`
	TrackTotal  int    `json:"track_total"`
}

// GetTagsResponse представляет ответ эндпоинта /tags при чтении папки или файла.
type GetTagsResponse struct {
	Album     AlbumSummaryDTO `json:"album"`
	Tracks    []TrackTags     `json:"tracks"`
	SourceDir string          `json:"source_dir"`
	CoverPath *string         `json:"cover_path"`
	HasCover  bool            `json:"has_cover"`

	// Поля обратной совместимости для клиентов одиночных треков
	Title        string  `json:"title,omitempty"`
	Artist       string  `json:"artist,omitempty"`
	AlbumArtist  string  `json:"album_artist,omitempty"`
	Path         string  `json:"path,omitempty"`
	TrackNumber  int     `json:"track_number,omitempty"`
	TrackTotal   int     `json:"track_total,omitempty"`
	DiscNumber   int     `json:"disc_number,omitempty"`
	DiscTotal    int     `json:"disc_total,omitempty"`
	Year         int     `json:"year,omitempty"`
	Genre        string  `json:"genre,omitempty"`
	Duration     float64 `json:"duration,omitempty"`
	Bitrate      int     `json:"bitrate,omitempty"`
	LyricsStatus string  `json:"lyrics_status,omitempty"`
}

// SaveTagsResponse представляет результат сохранения тегов одиночного трека.
type SaveTagsResponse struct {
	Success bool      `json:"success"`
	Track   TrackTags `json:"track"`
	Error   string    `json:"error,omitempty"`
}

// BatchSaveRequest представляет тело запроса пакетного сохранения тегов и раскладки альбома.
type BatchSaveRequest struct {
	Album struct {
		AlbumArtist string `json:"album_artist"`
		Album       string `json:"album"`
		Year        *int   `json:"year"`
		Genre       string `json:"genre"`
	} `json:"album"`
	Tracks      []TrackTags `json:"tracks"`
	SourceDir   string      `json:"source_dir"`
	CoverBase64 string      `json:"cover_base64,omitempty"`
	CoverURL    string      `json:"cover_url,omitempty"`
	Reorganize  bool        `json:"reorganize"`
}

// BatchSaveResponse представляет результат пакетного сохранения тегов.
type BatchSaveResponse struct {
	TrackResults   []organizer.TrackResult `json:"track_results,omitempty"`
	Success        bool                    `json:"success"`
	DestinationDir string                  `json:"destination_dir"`
	Tracks         []TrackTags             `json:"tracks"`
	Error          string                  `json:"error,omitempty"`
}

// CoverSearchResultItem результат онлайн-поиска обложек (Deezer, iTunes, MusicBrainz).
type CoverSearchResultItem struct {
	Source   string `json:"source"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	CoverURL string `json:"cover_url"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Year     *int   `json:"year,omitempty"`
}

// RecognizeAudioRequest запрос на акустическое распознавание аудиофайла.
type RecognizeAudioRequest struct {
	Path string `json:"path"`
}

// RecognizeAudioResponse результат акустического распознавания аудиофайла.
type RecognizeAudioResponse struct {
	Recognized bool   `json:"recognized"`
	Title      string `json:"title,omitempty"`
	Artist     string `json:"artist,omitempty"`
	Album      string `json:"album,omitempty"`
	Year       int    `json:"year,omitempty"`
	Genre      string `json:"genre,omitempty"`
	CoverURL   string `json:"cover_url,omitempty"`
}

// DeezerSearchAlbumItem элемент выдачи поиска альбомов в Deezer.
type DeezerSearchAlbumItem struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	CoverURL *string `json:"cover_url"`
	NbTracks int     `json:"nb_tracks"`
	Year     *int    `json:"year"`
}

// DeezerSearchTrackItem элемент выдачи поиска треков в Deezer.
type DeezerSearchTrackItem struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Artist      string  `json:"artist"`
	Album       string  `json:"album"`
	AlbumArtist string  `json:"album_artist"`
	CoverURL    *string `json:"cover_url"`
	Duration    int     `json:"duration"`
	Year        *int    `json:"year"`
}

// DeezerAlbumTrackItem дорожка в деталях альбома Deezer.
type DeezerAlbumTrackItem struct {
	TrackNumber  int      `json:"track_number"`
	DiscNumber   int      `json:"disc_number"`
	Title        string   `json:"title"`
	Artist       string   `json:"artist"`
	Duration     int      `json:"duration"`
	ISRC         string   `json:"isrc,omitempty"`
	Contributors []string `json:"contributors,omitempty"`
}

// DeezerAlbumDetailsResponse детальная информация об альбоме Deezer.
type DeezerAlbumDetailsResponse struct {
	ID         string                 `json:"id"`
	Title      string                 `json:"title"`
	Artist     string                 `json:"artist"`
	Year       int                    `json:"year"`
	Genre      string                 `json:"genre"`
	CoverURL   string                 `json:"cover_url"`
	TrackCount int                    `json:"track_count,omitempty"`
	Tracks     []DeezerAlbumTrackItem `json:"tracks"`
}

// MusicBrainzConfig конфигурация параметров OAuth для MusicBrainz.
type MusicBrainzConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
}

// ============================================================================
// 2. File Pattern Renaming Models
// ============================================================================

// RenamePatternItem представляет один файл для переименования по шаблону.
type RenamePatternItem struct {
	CurrentPath string `json:"current_path"` // Относительный путь к файлу
	NewName     string `json:"new_name"`     // Новое имя файла или относительный путь
}

// RenamePatternRequest запрос на переименование пачки файлов.
type RenamePatternRequest struct {
	Items  []RenamePatternItem `json:"items"`
	DryRun bool                `json:"dry_run"`
}

// RenamePatternResult результат операции переименования одного файла.
type RenamePatternResult struct {
	CurrentPath string `json:"current_path"`
	NewPath     string `json:"new_path"`
	Status      string `json:"status"` // "ok" | "conflict" | "error"
	Error       string `json:"error,omitempty"`
}

// ============================================================================
// 3. Puddletag Workstation Domain Models
// ============================================================================

// PatternDirection указывает направление применения шаблона.
type PatternDirection string

const (
	DirectionFilenameToTag PatternDirection = "filename_to_tag"
	DirectionTagToFilename PatternDirection = "tag_to_filename"
)

// PuddlePattern описывает шаблон переименования или тегирования.
type PuddlePattern struct {
	Pattern   string           `json:"pattern"`
	Label     string           `json:"label"`
	Direction PatternDirection `json:"direction"`
}

// FieldDiff хранит старое и новое значения поля.
type FieldDiff struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// PatternDiffItem представляет результат сравнения для одного файла.
type PatternDiffItem struct {
	TrackPath       string               `json:"track_path"`
	CurrentFilename string               `json:"current_filename"`
	NewFilename     string               `json:"new_filename"`
	HasChange       bool                 `json:"has_change"`
	HasConflict     bool                 `json:"has_conflict"`
	ConflictReason  string               `json:"conflict_reason,omitempty"`
	Changes         map[string]FieldDiff `json:"changes,omitempty"`
}

// PuddleActionType представляет тип быстрой операции Puddletag.
type PuddleActionType string

const (
	ActionCaseConversion PuddleActionType = "case_conversion"
	ActionAutoNumber     PuddleActionType = "auto_number"
	ActionQuickTagFeat   PuddleActionType = "quick_tag_feat"
	ActionFindReplace    PuddleActionType = "find_replace"
	ActionApplyPattern   PuddleActionType = "apply_pattern"
)

// AutoNumberOptions параметры мастера автонумерации треков.
type AutoNumberOptions struct {
	StartNum     int  `json:"start_num"`
	LeadingZeros bool `json:"leading_zeros"`
	ResetPerDisc bool `json:"reset_per_disc"`
	TotalTracks  int  `json:"total_tracks"`
}

// FindReplaceOptions параметры поиска и замены по полям тегов.
type FindReplaceOptions struct {
	SearchText  string   `json:"search_text"`
	ReplaceText string   `json:"replace_text"`
	MatchCase   bool     `json:"match_case"`
	UseRegex    bool     `json:"use_regex"`
	Fields      []string `json:"fields"`
}

// PuddleActionRequest тело запроса на выполнение быстрой операции Puddletag.
type PuddleActionRequest struct {
	Action         PuddleActionType    `json:"action"`
	TargetIndices  []int               `json:"target_indices,omitempty"`
	Fields         []string            `json:"fields,omitempty"`
	CaseStyle      string              `json:"case_style,omitempty"`
	AutoNumberOpts *AutoNumberOptions  `json:"auto_number_opts,omitempty"`
	FindReplace    *FindReplaceOptions `json:"find_replace_opts,omitempty"`
	Pattern        string              `json:"pattern,omitempty"`
	Direction      PatternDirection    `json:"direction,omitempty"`
	Tracks         []TrackTags         `json:"tracks"`
	DryRun         bool                `json:"dry_run"`
}

// PuddleActionResponse результат выполнения быстрой операции Puddletag.
type PuddleActionResponse struct {
	Success       bool              `json:"success"`
	ModifiedCount int               `json:"modified_count"`
	Tracks        []TrackTags       `json:"tracks"`
	Diffs         []PatternDiffItem `json:"diffs,omitempty"`
	Error         string            `json:"error,omitempty"`
}

// PatternPreviewRequest запрос на расчет превью применения шаблона.
type PatternPreviewRequest struct {
	Pattern   string           `json:"pattern"`
	Direction PatternDirection `json:"direction"`
	Tracks    []TrackTags      `json:"tracks"`
}

// PatternPreviewResponse ответ с результатом предпросмотра шаблона.
type PatternPreviewResponse struct {
	Pattern       string            `json:"pattern"`
	TotalTracks   int               `json:"total_tracks"`
	ChangedCount  int               `json:"changed_count"`
	ConflictCount int               `json:"conflict_count"`
	Diffs         []PatternDiffItem `json:"diffs"`
}

// ============================================================================
// 4. Catalog Models Aliasing
// ============================================================================

type CatalogReleaseSummary = resolver.CatalogReleaseSummary
type CatalogTrackDetail = resolver.CatalogTrackDetail
type CatalogReleaseDetails = resolver.CatalogReleaseDetails
type CatalogSourceResult = resolver.CatalogSourceResult
type CatalogSearchResponse = resolver.CatalogSearchResponse
type MusicBrainzOAuthStatus = resolver.MusicBrainzOAuthStatus
type OAuthTokenResponse = resolver.OAuthTokenResponse
