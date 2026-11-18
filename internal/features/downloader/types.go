package downloader

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
)

// JobStatus представляет текущее состояние задачи загрузки.
type JobStatus string

const (
	StatusQueued        JobStatus = "QUEUED"
	StatusWaitingSource JobStatus = "WAITING_SOURCE"
	StatusResolving     JobStatus = "RESOLVING"
	StatusDownloading   JobStatus = "DOWNLOADING"
	StatusConverting    JobStatus = "CONVERTING"
	StatusTagging       JobStatus = "TAGGING"
	StatusReady         JobStatus = "READY"
	StatusPartialReady  JobStatus = "PARTIAL_READY"
	StatusError         JobStatus = "ERROR"
	StatusCancelled     JobStatus = "CANCELLED"
)

// Priority возвращает числовой приоритет для детерминированной сортировки очереди:
// 1 (Queued) -> 2 (В процессе) -> 3 (Ready) -> 4 (Error) -> 5 (Cancelled) -> 6 (Default).
func (s JobStatus) Priority() int {
	switch s {
	case StatusQueued, StatusWaitingSource:
		return 1
	case StatusResolving, StatusDownloading, StatusConverting, StatusTagging:
		return 2
	case StatusReady, StatusPartialReady:
		return 3
	case StatusError:
		return 4
	case StatusCancelled:
		return 5
	default:
		return 6
	}
}

// Job представляет доменную модель задачи загрузки в хранилище, сервисе и API.
type Job struct {
	mu                   sync.RWMutex
	ID                   string                   `json:"id"`
	Status               JobStatus                `json:"status"`
	Source               string                   `json:"source"`
	URL                  string                   `json:"url"`
	Progress             float64                  `json:"progress"`
	CurrentStep          string                   `json:"current_step"`
	TotalTracks          int                      `json:"total_tracks"`
	CompletedTracks      int                      `json:"completed_tracks"`
	Error                *string                  `json:"error,omitempty"`
	TargetAlbumPath      *string                  `json:"target_album_path,omitempty"`
	Backfill             *BackfillRequest         `json:"backfill,omitempty"`
	IsBackfill           bool                     `json:"is_backfill"`
	AlbumTitle           *string                  `json:"album_title,omitempty"`
	ArtistName           *string                  `json:"artist_name,omitempty"`
	CreatedAt            string                   `json:"created_at"` // RFC3339 / RFC3339Nano формат для совместимости с фронтендом
	UpdatedAt            time.Time                `json:"updated_at,omitempty"`
	CoverRelativePath    string                   `json:"-"`
	AlbumMetadata        resolver.ReleaseMetadata `json:"-"`
	SelectedTrackIndices []int                    `json:"selected_track_indices,omitempty"`
	Tracks               []resolver.ResolvedTrack `json:"tracks,omitempty"`
	trackProgress        []float64                `json:"-"`
	cancelFunc           func()                   `json:"-"`
	onUpdate             func()                   `json:"-"`
}

// DownloadJob является алиасом Job для совместимости.
type DownloadJob = Job

// MarshalJSON безопасно сериализует Job под read-lock для предотвращения гонок данных.
func (j *Job) MarshalJSON() ([]byte, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()

	type Alias Job
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(j),
	})
}

// JobSnapshot — алиас Job для обратной совместимости при передаче снимков.
type JobSnapshot = Job

// DownloadJobSnapshot — алиас Job для обратной совместимости.
type DownloadJobSnapshot = Job

// Snapshot создает независимую потокобезопасную копию задачи.
// Возвращает *Job для предотвращения копирования sync.RWMutex по значению (go vet clean).
func (j *Job) Snapshot() *Job {
	if j == nil {
		return nil
	}
	j.mu.RLock()
	defer j.mu.RUnlock()

	snap := &Job{
		ID:                j.ID,
		Status:            j.Status,
		Source:            j.Source,
		URL:               j.URL,
		Progress:          j.Progress,
		CurrentStep:       j.CurrentStep,
		TotalTracks:       j.TotalTracks,
		CompletedTracks:   j.CompletedTracks,
		IsBackfill:        j.IsBackfill,
		CreatedAt:         j.CreatedAt,
		UpdatedAt:         j.UpdatedAt,
		CoverRelativePath: j.CoverRelativePath,
		AlbumMetadata:     j.AlbumMetadata,
		cancelFunc:        j.cancelFunc,
	}
	if j.Error != nil {
		errCopy := *j.Error
		snap.Error = &errCopy
	}
	if j.AlbumTitle != nil {
		titleCopy := *j.AlbumTitle
		snap.AlbumTitle = &titleCopy
	}
	if j.ArtistName != nil {
		artistCopy := *j.ArtistName
		snap.ArtistName = &artistCopy
	}
	if j.TargetAlbumPath != nil {
		pathCopy := *j.TargetAlbumPath
		snap.TargetAlbumPath = &pathCopy
	}
	if j.Backfill != nil {
		selection := *j.Backfill
		selection.Positions = slices.Clone(j.Backfill.Positions)
		selection.TrackURLs = slices.Clone(j.Backfill.TrackURLs)
		selection.TrackNumbers = slices.Clone(j.Backfill.TrackNumbers)
		snap.Backfill = &selection
	}
	if len(j.Tracks) > 0 {
		snap.Tracks = make([]resolver.ResolvedTrack, len(j.Tracks))
		copy(snap.Tracks, j.Tracks)
	}
	if len(j.SelectedTrackIndices) > 0 {
		snap.SelectedTrackIndices = make([]int, len(j.SelectedTrackIndices))
		copy(snap.SelectedTrackIndices, j.SelectedTrackIndices)
	}
	return snap
}

// SetOnUpdate устанавливает функцию обратного вызова при изменении задачи.
func (j *Job) SetOnUpdate(fn func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.onUpdate = fn
}

func (j *Job) notifyUpdate() {
	j.mu.RLock()
	fn := j.onUpdate
	j.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

// UpdateProgress обновляет прогресс и шаг выполнения задачи.
func (j *Job) UpdateProgress(progress float64, step string) {
	j.mu.Lock()
	if progress > j.Progress {
		j.Progress = progress
	}
	j.CurrentStep = step
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

// UpdateTrackProgress рассчитывает агрегированный прогресс на основе прогресса отдельного трека.
func (j *Job) UpdateTrackProgress(trackIdx int, pct float64, step string) {
	j.mu.Lock()
	if trackIdx >= 0 && trackIdx < len(j.trackProgress) {
		if pct > j.trackProgress[trackIdx] {
			j.trackProgress[trackIdx] = pct
		}
	}
	sum := 0.0
	for _, p := range j.trackProgress {
		sum += p
	}
	n := float64(len(j.trackProgress))
	if n == 0 {
		n = 1
	}
	calcProgress := 10.0 + (sum/n)*0.85
	if calcProgress > j.Progress {
		j.Progress = calcProgress
	}
	if j.Progress > 99.0 && j.Status != StatusReady {
		j.Progress = 99.0
	}
	if len(j.Tracks) > 1 {
		j.CurrentStep = fmt.Sprintf("Загрузка треков (%d/%d готово, %.0f%%)", j.CompletedTracks, len(j.Tracks), j.Progress)
	} else {
		j.CurrentStep = step
	}
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

// SetStatus переводит задачу в указанный статус.
func (j *Job) SetStatus(status JobStatus) {
	j.mu.Lock()
	j.Status = status
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

// SetError переводит задачу в статус ошибки.
func (j *Job) SetError(errMsg string) {
	j.mu.Lock()
	j.Status = StatusError
	j.Error = &errMsg
	j.CurrentStep = errMsg
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

// SetCurrentStep обновляет текстовое описание текущего шага.
func (j *Job) SetCurrentStep(step string) {
	j.mu.Lock()
	if len(j.Tracks) > 1 && (j.Status == StatusDownloading || j.Status == StatusConverting || j.Status == StatusTagging) {
		j.CurrentStep = fmt.Sprintf("Загрузка треков (%d/%d готово, %.0f%%)", j.CompletedTracks, len(j.Tracks), j.Progress)
	} else {
		j.CurrentStep = step
	}
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

// SetStepAndStatus одновременно обновляет статус и шаг.
func (j *Job) SetStepAndStatus(step string, status JobStatus) {
	j.mu.Lock()
	j.Status = status
	if len(j.Tracks) > 1 && (status == StatusDownloading || status == StatusConverting || status == StatusTagging) {
		j.CurrentStep = fmt.Sprintf("Обработка треков (%d/%d готово, %.0f%%)", j.CompletedTracks, len(j.Tracks), j.Progress)
	} else {
		j.CurrentStep = step
	}
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

// SetReady фиксирует успешное завершение всех треков задачи.
func (j *Job) SetReady(completedTracks, total int) {
	j.mu.Lock()
	j.Status = StatusReady
	j.CompletedTracks = completedTracks
	j.TotalTracks = total
	j.Progress = 100.0
	j.CurrentStep = fmt.Sprintf("Готово к проверке тегов (%d/%d скачано)", completedTracks, total)
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

// SetPartialReady фиксирует частичное завершение задачи.
func (j *Job) SetPartialReady(completedTracks, total int) {
	j.mu.Lock()
	j.Status = StatusPartialReady
	j.CompletedTracks = completedTracks
	j.TotalTracks = total
	j.Progress = 100.0
	j.CurrentStep = fmt.Sprintf("Готов частично (%d/%d готово, требуется выбор источника)", completedTracks, total)
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

// IncrementCompleted увеличивает счетчик завершенных треков.
func (j *Job) IncrementCompleted(total int) {
	j.mu.Lock()
	j.CompletedTracks++
	calcProgress := 10.0 + (float64(j.CompletedTracks)/float64(total))*85.0
	if calcProgress > j.Progress {
		j.Progress = calcProgress
	}
	if total > 1 {
		j.CurrentStep = fmt.Sprintf("Загрузка треков (%d/%d готово, %.0f%%)", j.CompletedTracks, total, j.Progress)
	}
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

// SetMetadata сохраняет альбомные метаданные и дорожки задачи.
func (j *Job) SetMetadata(meta resolver.ReleaseMetadata, tracks []resolver.ResolvedTrack) {
	j.mu.Lock()
	j.AlbumMetadata = meta
	j.Tracks = tracks
	j.TotalTracks = len(tracks)
	j.trackProgress = make([]float64, len(tracks))
	if meta.AlbumTitle != "" {
		aTitle := meta.AlbumTitle
		j.AlbumTitle = &aTitle
	}
	if meta.AlbumArtist != "" {
		aArtist := meta.AlbumArtist
		j.ArtistName = &aArtist
	}
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

// SetCoverPath сохраняет относительный путь к обложке.
func (j *Job) SetCoverPath(relCover string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.CoverRelativePath = relCover
	j.UpdatedAt = time.Now().UTC()
}

// SetCancelFunc привязывает функцию отмены контекста к задаче.
func (j *Job) SetCancelFunc(cancel func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cancelFunc = cancel
}

// Cancel вызывает отмену контекста задачи.
func (j *Job) Cancel() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cancelFunc != nil {
		j.cancelFunc()
	}
}

// UpdateTrack модифицирует трек под блокировкой.
func (j *Job) UpdateTrack(index int, updateFn func(t *resolver.ResolvedTrack)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if index >= 0 && index < len(j.Tracks) {
		updateFn(&j.Tracks[index])
	}
}

// SetSelectedTrackIndices сохраняет список выбранных индексов треков для выборочного скачивания.
func (j *Job) SetSelectedTrackIndices(indices []int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.SelectedTrackIndices = make([]int, len(indices))
	copy(j.SelectedTrackIndices, indices)
	j.UpdatedAt = time.Now().UTC()
}

// GetSelectedTrackIndices возвращает копию списка выбранных индексов треков.
func (j *Job) GetSelectedTrackIndices() []int {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if len(j.SelectedTrackIndices) == 0 {
		return nil
	}
	res := make([]int, len(j.SelectedTrackIndices))
	copy(res, j.SelectedTrackIndices)
	return res
}

// --- Request DTOs ---

// CreateJobRequest — тело запроса для постановки одиночной ссылки в очередь.
type CreateJobRequest struct {
	URL                  string `json:"url"`
	Source               string `json:"source"`
	SelectedTrackIndices []int  `json:"selected_track_indices,omitempty"`
	SkipExisting         bool   `json:"skip_existing,omitempty"`
}

// PreflightRequest — запрос предполетной проверки релиза.
type PreflightRequest struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}

// PreflightTrackStatus определяет статус трека относительно локальной медиатеки.
type PreflightTrackStatus string

const (
	TrackStatusNew               PreflightTrackStatus = "NEW"
	TrackStatusExistsHighQuality PreflightTrackStatus = "EXISTS_HIGH_QUALITY"
	TrackStatusExistsUpgrade     PreflightTrackStatus = "EXISTS_UPGRADE_AVAILABLE"
)

// ExistingTrackInfo содержит сведения о треке, уже найденном в медиатеке.
type ExistingTrackInfo struct {
	TrackID     int64  `json:"track_id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	FilePath    string `json:"file_path"`
	Bitrate     int    `json:"bitrate"`
	QualityTier string `json:"quality_tier"` // "true_320k" или "upscaled_128k"
}

// PreflightTrackItem представляет трек в предпросмотре релиза.
type PreflightTrackItem struct {
	Index         int                  `json:"index"`
	TrackNumber   int                  `json:"track_number"`
	DiscNumber    int                  `json:"disc_number"`
	Title         string               `json:"title"`
	Artist        string               `json:"artist"`
	Duration      int                  `json:"duration"`
	Status        PreflightTrackStatus `json:"status"`
	ExistingTrack *ExistingTrackInfo   `json:"existing_track,omitempty"`
	DefaultSelect bool                 `json:"default_selected"`
}

// PreflightReleaseInfo содержит сводные метаданные релиза для предполетного окна.
type PreflightReleaseInfo struct {
	Title         string `json:"title"`
	Artist        string `json:"artist"`
	Year          int    `json:"year"`
	Genre         string `json:"genre"`
	CoverURL      string `json:"cover_url"`
	TotalTracks   int    `json:"total_tracks"`
	FetchedTracks int    `json:"fetched_tracks,omitempty"`
	SkippedTracks int    `json:"skipped_tracks,omitempty"`
	IsPlaylist    bool   `json:"is_playlist,omitempty"`
}

// PreflightSummary содержит статистику релиза для быстрых фильтров.
type PreflightSummary struct {
	TotalTracks              int `json:"total_tracks"`
	NewTracksCount           int `json:"new_tracks_count"`
	ExistingHighQualityCount int `json:"existing_high_quality_count"`
	UpgradeAvailableCount    int `json:"upgrade_available_count"`
	FetchedTracksCount       int `json:"fetched_tracks_count,omitempty"`
	SkippedTracksCount       int `json:"skipped_tracks_count,omitempty"`
}

// PreflightResponse — ответ эндпоинта POST /api/downloads/preflight.
type PreflightResponse struct {
	Release PreflightReleaseInfo `json:"release"`
	Tracks  []PreflightTrackItem `json:"tracks"`
	Summary PreflightSummary     `json:"summary"`
}

// CreateBatchJobRequest — тело запроса для пакетного добавления ссылок.
type CreateBatchJobRequest struct {
	URLs   []string `json:"urls"`
	Source string   `json:"source"`
}

// BatchJobRequest — алиас CreateBatchJobRequest.
type BatchJobRequest = CreateBatchJobRequest

// SwitchVersionRequest — запрос на переключение источника трека.
type SwitchVersionRequest struct {
	VersionPolicy resolver.VersionPolicy `json:"version_policy"`
	TargetURL     string                 `json:"target_url"`
}

// BindBackfillSourceRequest — запрос на привязку источника к недостающему треку бэкфилла.
type BindBackfillSourceRequest struct {
	VersionPolicy resolver.VersionPolicy `json:"version_policy"`
	Duration      int                    `json:"duration"`
	Path          string                 `json:"path"`
	AlbumTitle    string                 `json:"album_title"`
	ArtistName    string                 `json:"artist_name"`
	TrackTitle    string                 `json:"track_title"`
	TrackNumber   int                    `json:"track_number"`
	TargetURL     string                 `json:"target_url"`
	DiscNumber    int                    `json:"disc_number"`
	DeezerID      string                 `json:"deezer_id,omitempty"`
}

// --- Response DTOs ---

// BatchJobResponse — ответ сервера на пакетное создание задач.
type BatchJobResponse struct {
	JobIDs      []string `json:"job_ids"`
	Jobs        []*Job   `json:"jobs"`
	TotalQueued int      `json:"total_queued"`
}

// JobResponse — алиас Job.
type JobResponse = Job

// QueueSummaryResponse — агрегированная сводка по состоянию очереди.
type QueueSummaryResponse struct {
	ActiveJobs      int     `json:"active_jobs"`
	QueuedJobs      int     `json:"queued_jobs"`
	CompletedJobs   int     `json:"completed_jobs"`
	ErrorJobs       int     `json:"error_jobs"`
	TotalTracks     int     `json:"total_tracks"`
	CompletedTracks int     `json:"completed_tracks"`
	OverallProgress float64 `json:"overall_progress"`
	IsPaused        bool    `json:"is_paused"`
}

// DownloadTracksResponse — детальная информация о метаданных и треках задачи.
type DownloadTracksResponse struct {
	JobID           string                   `json:"job_id"`
	AlbumArtist     string                   `json:"album_artist"`
	Album           string                   `json:"album"`
	Year            *int                     `json:"year"`
	Genre           *string                  `json:"genre"`
	DiscTotal       int                      `json:"disc_total"`
	TrackTotal      int                      `json:"track_total"`
	CoverURL        *string                  `json:"cover_url"`
	CoverPath       *string                  `json:"cover_path"`
	Tracks          []resolver.ResolvedTrack `json:"tracks"`
	IsBackfill      bool                     `json:"is_backfill,omitempty"`
	TargetAlbumPath *string                  `json:"target_album_path,omitempty"`
}

// JobTracksResponse — алиас DownloadTracksResponse.
type JobTracksResponse = DownloadTracksResponse

// DeleteResponse — ответ на удаление задачи.
type DeleteResponse struct {
	Status string `json:"status"`
	JobID  string `json:"job_id"`
}

// DeleteJobResponse — алиас DeleteResponse.
type DeleteJobResponse = DeleteResponse

// ClearResponse — ответ на очистку очереди.
type ClearResponse struct {
	Status  string `json:"status"`
	Cleared int    `json:"cleared"`
	Count   int    `json:"count"`
}

// ClearJobsResponse — алиас ClearResponse.
type ClearJobsResponse = ClearResponse

// PauseResponse — ответ на паузу или возобновление очереди.
type PauseResponse struct {
	Status   string `json:"status"`
	IsPaused bool   `json:"is_paused"`
}

// RetryFailedResponse — ответ на массовый перезапуск упавших задач.
type RetryFailedResponse struct {
	Status       string   `json:"status"`
	Count        int      `json:"count"`
	RetriedCount int      `json:"retried_count"`
	JobIDs       []string `json:"job_ids"`
}

// SwitchVersionResult — результат привязки альтернативного источника трека.
type SwitchVersionResult struct {
	TrackID   string `json:"track_id"`
	TargetURL string `json:"target_url"`
	Status    string `json:"status"` // "manual"
	Duration  int    `json:"duration"`
	FilePath  string `json:"file_path"`
}

// SwitchVersionResponse — алиас SwitchVersionResult.
type SwitchVersionResponse = SwitchVersionResult

// CandidatesResponse — кандидаты поиска альтернативных источников трека.
type CandidatesResponse struct {
	VersionPolicy resolver.VersionPolicy        `json:"version_policy"`
	Selection     string                        `json:"selection"`
	Artist        string                        `json:"artist"`
	Title         string                        `json:"title"`
	Query         string                        `json:"query"`
	Candidates    []resolver.CandidateTrackItem `json:"candidates"`
	Total         int                           `json:"total"`
}

// DiscographyResponse — агрегированная дискография артиста.
type DiscographyResponse struct {
	ArtistName    string               `json:"artist_name"`
	ArtistID      int64                `json:"artist_id"`
	ArtistAvatar  string               `json:"artist_avatar,omitempty"`
	Source        string               `json:"source,omitempty"`
	TotalReleases int                  `json:"total_releases"`
	Releases      []DiscographyRelease `json:"releases"`
}

// DiscographyRelease — отдельный релиз дискографии артиста.
type DiscographyRelease struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Type       string `json:"type"`
	Year       int    `json:"year"`
	URL        string `json:"url"`
	CoverURL   string `json:"cover_url"`
	TrackCount int    `json:"track_count"`
}

// BackfillRequest stores the immutable selection for initial execution and retries.
type BackfillRequest struct {
	AlbumID      string                 `json:"deezer_album_id,omitempty"`
	Positions    []db.BackfillSelection `json:"positions,omitempty"`
	TrackURLs    []string               `json:"track_urls,omitempty"`
	TrackNumbers []int                  `json:"track_numbers,omitempty"`
}

// MissingTrackInfo is a catalog gap and its optional manually pinned audio source.
type MissingTrackInfo struct {
	db.CatalogTrack
	PinnedURL string
}

// PinnedTrackSourceDTO — DTO для сохранения и чтения ручной привязки трека.
type PinnedTrackSourceDTO struct {
	CacheKey   string    `json:"cache_key"`
	ArtistName string    `json:"artist_name"`
	TrackTitle string    `json:"track_title"`
	ISRC       *string   `json:"isrc,omitempty"`
	PinnedURL  string    `json:"pinned_url"`
	SourceType string    `json:"source_type"`
	Duration   int       `json:"duration"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SoundCloudCacheDTO — запись кэша альтернативных версий SoundCloud.
type SoundCloudCacheDTO struct {
	CacheKey       string                       `json:"cache_key"`
	TrackTitle     string                       `json:"track_title"`
	ArtistName     string                       `json:"artist_name"`
	SelectedURL    string                       `json:"selected_url"`
	Status         string                       `json:"status"`
	Duration       int                          `json:"duration"`
	IsManual       bool                         `json:"is_manual"`
	CandidatesJSON string                       `json:"candidates_json"`
	Candidates     []db.SoundCloudCandidateItem `json:"candidates,omitempty"`
	CheckedAt      time.Time                    `json:"checked_at"`
	UpdatedAt      time.Time                    `json:"updated_at"`
}

// ReleaseCandidatesResponse — ответ поиска цельных источников релиза.
type ReleaseCandidatesResponse struct {
	VersionPolicy     resolver.VersionPolicy          `json:"version_policy"`
	Selection         string                          `json:"selection"`
	Artist            string                          `json:"artist"`
	Album             string                          `json:"album"`
	IsCyrillic        bool                            `json:"is_cyrillic"`
	TargetTracks      []resolver.ResolvedTrack        `json:"target_tracks"`
	Candidates        []resolver.ReleaseCandidateItem `json:"candidates"`
	HasMoreSoundCloud bool                            `json:"has_more_soundcloud"`
	AlbumOffset       int                             `json:"album_offset"`
	PlaylistOffset    int                             `json:"playlist_offset"`
	AlbumToken        string                          `json:"album_token,omitempty"`
	PlaylistToken     string                          `json:"playlist_token,omitempty"`
}

// ParseReleaseURLRequest — запрос разбора произвольной ссылки на релиз.
type ParseReleaseURLRequest struct {
	URL          string                   `json:"url"`
	Artist       string                   `json:"artist"`
	Album        string                   `json:"album"`
	TargetTracks []resolver.ResolvedTrack `json:"target_tracks"`
}

// SwitchReleaseSourceRequest — запрос применения источника цельного релиза.
type SwitchReleaseSourceRequest struct {
	MissingOnly   bool                     `json:"missing_only,omitempty"`
	Backfill      []db.BackfillSelection   `json:"-"`
	TargetTracks  []resolver.ResolvedTrack `json:"target_tracks,omitempty"`
	VersionPolicy resolver.VersionPolicy   `json:"version_policy"`
	JobID         string                   `json:"job_id"`
	TargetPath    string                   `json:"target_path,omitempty"`
	SourceURL     string                   `json:"source_url,omitempty"`
	TrackMapping  map[int]string           `json:"track_mapping"` // targetIdx -> audioURL
}

// SwitchReleaseSourceResult — результат переключения источника релиза.
type SwitchReleaseSourceResult struct {
	JobID        string `json:"job_id"`
	UpdatedCount int    `json:"updated_count"`
	TotalTracks  int    `json:"total_tracks"`
	Status       string `json:"status"`
}
