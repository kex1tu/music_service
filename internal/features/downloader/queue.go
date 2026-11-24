package downloader

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"music-manager/internal/db"
	"music-manager/internal/services/downloadstaging"
	"music-manager/internal/services/resolver"
)

// PauseQueue приостанавливает взятие новых задач воркерами.
func (s *Service) PauseQueue() {
	s.pauseMu.Lock()
	s.isPaused = true
	s.pauseMu.Unlock()
}

// ResumeQueue возобновляет работу воркеров.
func (s *Service) ResumeQueue() {
	s.pauseMu.Lock()
	s.isPaused = false
	s.pauseMu.Unlock()
	s.wakeWorker()
}

// IsQueuePaused возвращает статус паузы очереди.
func (s *Service) IsQueuePaused() bool {
	s.pauseMu.RLock()
	defer s.pauseMu.RUnlock()
	return s.isPaused
}

func generateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b)
}

// JobOptions содержит дополнительные параметры постановки задачи на загрузку.
type JobOptions struct {
	PreflightID          string
	SelectedTrackIndices []int
	SkipExisting         bool
}

// CreateJobWithOptions создает и ставит в очередь новую задачу загрузки с дополнительными опциями.
func (s *Service) CreateJobWithOptions(ctx context.Context, rawURL, source string, opts JobOptions) (*Job, error) {
	job, err := s.newQueuedJob(rawURL, source, opts)
	if err != nil {
		return nil, err
	}
	if len(job.SelectedTrackIndices) > 0 {
		if err := s.freezePreflightSelection(job, opts.PreflightID); err != nil {
			return nil, err
		}
	}
	return s.enqueueNewJob(ctx, job)
}

func (s *Service) newQueuedJob(rawURL, source string, opts JobOptions) (*Job, error) {
	cleanURL := resolver.ExtractURLFromInput(rawURL)
	if cleanURL == "" {
		return nil, errors.New("URL cannot be empty")
	}

	if !resolver.IsAllowedURL(cleanURL) {
		return nil, fmt.Errorf("domain is not in allowed sources whitelist: %s", cleanURL)
	}

	var detectedSource string
	if s.resolver != nil {
		det, err := s.resolver.DetectSource(cleanURL)
		if err != nil {
			return nil, err
		}
		detectedSource = det
	} else {
		detectedSource = "auto"
	}

	if source == "" || source == "auto" || source == "radar" {
		source = detectedSource
	}

	jobID := generateUUID()
	return &Job{
		ID:                   jobID,
		Status:               StatusQueued,
		Source:               source,
		URL:                  cleanURL,
		Progress:             0.0,
		CurrentStep:          "Задача ожидает очереди",
		CreatedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:            time.Now().UTC(),
		SelectedTrackIndices: slices.Clone(opts.SelectedTrackIndices),
		SkipExisting:         opts.SkipExisting,
	}, nil
}

func (s *Service) enqueueNewJob(ctx context.Context, job *Job) (*Job, error) {
	if err := s.enqueueNewJobs(ctx, []*Job{job}); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *Service) enqueueNewJobs(ctx context.Context, jobs []*Job) error {
	if len(jobs) == 0 {
		return nil
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.repo != nil {
		if err := s.repo.createJobs(ctx, jobs); err != nil {
			return fmt.Errorf("%w: %w", errQueueStorage, err)
		}
	}
	for _, job := range jobs {
		s.registerJobLocked(job)
	}
	s.wakeWorker()
	return nil
}

// CreateJob создает и ставит в очередь новую задачу загрузки.
func (s *Service) CreateJob(ctx context.Context, rawURL, source string) (*Job, error) {
	return s.CreateJobWithOptions(ctx, rawURL, source, JobOptions{})
}

// CreateBatchJobs создает пачку задач загрузки.
func (s *Service) CreateBatchJobs(ctx context.Context, urls []string, source string) ([]*Job, error) {
	created := make([]*Job, 0, len(urls))
	for _, rawURL := range urls {
		job, err := s.newQueuedJob(rawURL, source, JobOptions{})
		if err != nil {
			return nil, err
		}
		created = append(created, job)
	}
	if err := s.enqueueNewJobs(ctx, created); err != nil {
		return nil, err
	}
	return created, nil
}

// CreateBackfillJob создает задачу дозагрузки недостающих треков релиза.
func (s *Service) CreateBackfillJob(
	ctx context.Context,
	targetAlbumPath, albumTitle, artistName, deezerAlbumID string,
	trackURLs []string,
	trackNumbers []int,
	positions ...db.BackfillSelection,
) (*Job, error) {
	if targetAlbumPath == "" && albumTitle == "" {
		return nil, errors.New("target album path or title required")
	}

	if targetAlbumPath != "" {
		targetAlbumPath = db.AlbumRoot(filepath.ToSlash(targetAlbumPath))
	}
	selection := &BackfillRequest{AlbumID: deezerAlbumID, Positions: slices.Clone(positions),
		TrackURLs: slices.Clone(trackURLs), TrackNumbers: slices.Clone(trackNumbers)}
	if err := s.freezeBackfillSelection(ctx, targetAlbumPath, selection); err != nil {
		return nil, err
	}

	jobID := generateUUID()
	primaryURL := ""
	if deezerAlbumID != "" {
		primaryURL = fmt.Sprintf("https://www.deezer.com/album/%s", deezerAlbumID)
	} else if len(trackURLs) > 0 {
		primaryURL = trackURLs[0]
	}

	aTitle := albumTitle
	aArtist := artistName
	tPath := targetAlbumPath

	job := &Job{
		ID:              jobID,
		Status:          StatusQueued,
		Source:          "deezer",
		URL:             primaryURL,
		Progress:        0.0,
		CurrentStep:     "Задача дозагрузки ожидает очереди",
		IsBackfill:      true,
		Backfill:        selection,
		TargetAlbumPath: &tPath,
		AlbumTitle:      &aTitle,
		ArtistName:      &aArtist,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:       time.Now().UTC(),
	}

	if s.repo != nil && targetAlbumPath != "" {
		id, err := s.repo.albumArtistForFolder(ctx, targetAlbumPath)
		if err != nil {
			return nil, err
		}
		job.ArtistID = id
	}
	return s.enqueueNewJob(ctx, job)
}

// RetryJob сбрасывает и перезапускает упавшую или отмененную задачу.
func (s *Service) RetryJob(ctx context.Context, jobID string) (*Job, error) {
	job, ok := s.GetJob(jobID)
	if !ok {
		return nil, fmt.Errorf("задача %s не найдена", jobID)
	}

	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.jobs[jobID] != job {
		return nil, ErrJobNotFound
	}
	draft := job.Snapshot()
	if draft.CleanupPending != "" {
		if draft.CleanupPending != cleanupRetry {
			return nil, errCleanupPending
		}
		if err := s.completeCleanupLocked(ctx, job); err != nil {
			return nil, err
		}
		return job, nil
	}
	if draft.selectionCorrupt {
		return nil, ErrCorruptJobSelection
	}
	switch draft.Status {
	case StatusResolving, StatusDownloading, StatusConverting, StatusTagging:
		return nil, errors.New("задача уже выполняется")
	case StatusQueued:
		return job, nil
	}

	if draft.IsRedownload && draft.manualTrackIndex == nil {
		for _, target := range draft.RedownloadTargets {
			if target.Confirmed || target.ResultHash != "" {
				return nil, errors.New("для оставшихся дорожек выберите источник отдельно")
			}
		}
		if draft.Status == StatusWaitingSource && draft.URL == "" && !hasManualSource(draft.Tracks) {
			return nil, errors.New("сначала выберите источник")
		}
	}

	if len(draft.SelectedTrackIndices) > 0 && len(draft.Tracks) == 0 {
		return nil, errPreflightExpired
	}
	if s.tmpSandbox != nil && draft.manualTrackIndex == nil {
		pending, err := downloadstaging.HasPendingImport(s.tmpSandbox, jobID)
		if err != nil {
			return nil, err
		}
		if pending {
			return nil, errors.New("сначала импортируйте готовые файлы задачи")
		}
	}

	previous := draft.Status
	if s.tmpSandbox != nil && draft.manualTrackIndex == nil {
		if err := s.beginCleanupLocked(ctx, job, cleanupRetry); err != nil {
			return nil, err
		}
		if err := s.completeCleanupLocked(ctx, job); err != nil {
			return nil, err
		}
		return job, nil
	}
	draft.resetForQueue("Задача перезапущена, ожидает очереди")
	if s.repo != nil {
		if err := s.repo.updateJobWithSelection(ctx, draft, previous); err != nil {
			// Jobs registered by compatibility callers may exist only in memory.
			if !errors.Is(err, ErrJobNotFound) {
				return nil, fmt.Errorf("%w: %w", errQueueStorage, err)
			}
			if err := s.repo.CreateJob(ctx, draft); err != nil {
				return nil, fmt.Errorf("%w: %w", errQueueStorage, err)
			}
		}
	}
	job.applyQueueSnapshot(draft)
	s.wakeWorker()
	return job, nil
}

// RetryAllFailedJobs перезапускает все упавшие задачи.
func (s *Service) RetryAllFailedJobs(ctx context.Context) ([]string, error) {
	all := s.GetAllJobs()
	var retried []string
	for _, j := range all {
		snap := j.Snapshot()
		if snap.Status == StatusError || snap.Status == StatusCancelled {
			if _, err := s.RetryJob(ctx, snap.ID); err != nil {
				if errors.Is(err, ErrCorruptJobSelection) {
					continue
				}
				return retried, err
			}
			retried = append(retried, snap.ID)
		}
	}
	return retried, nil
}

// DeleteJob отменяет задачу и удаляет её из памяти, БД и песочницы.
func (s *Service) DeleteJob(ctx context.Context, id string) error {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	return s.deleteJobLocked(ctx, id, true)
}

// FinishImport drops a ready job from the queue once its staged tracks have been consumed.
func (s *Service) FinishImport(ctx context.Context, id string) (bool, error) {
	if _, ok := s.GetJob(id); !ok {
		return true, nil
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	job := s.jobs[id]
	if job == nil {
		return true, nil
	}
	status := job.Snapshot().Status
	if job.Snapshot().CleanupPending != "" {
		return false, errCleanupPending
	}
	if status != StatusReady && status != StatusPartialReady {
		return false, nil
	}
	pending, err := downloadstaging.HasPendingImport(s.tmpSandbox, id)
	if err != nil || pending {
		return false, err
	}
	if err := s.deleteJobLocked(ctx, id, false); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) deleteJobLocked(ctx context.Context, id string, removeStaging bool) error {
	if removeStaging && s.tmpSandbox != nil {
		job := s.jobs[id]
		if job == nil && s.repo != nil {
			var err error
			job, err = s.repo.GetJob(ctx, id)
			if err != nil {
				return fmt.Errorf("%w: %w", errQueueStorage, err)
			}
		}
		if job == nil {
			// Do not materialize an unknown, invalid path as a queue entry.
			if _, err := s.tmpSandbox.Resolve(id, false); err != nil {
				return fmt.Errorf("%w: %w", errCleanupPending, err)
			}
			job = &Job{ID: id, Status: StatusError}
		}
		if err := s.beginCleanupLocked(ctx, job, cleanupDelete); err != nil {
			return err
		}
		return s.completeCleanupLocked(ctx, job)
	}
	if s.repo != nil {
		if err := s.repo.DeleteJob(ctx, id); err != nil {
			return fmt.Errorf("%w: %w", errQueueStorage, err)
		}
	}
	if job := s.jobs[id]; job != nil {
		delete(s.jobs, id)
		if removeStaging {
			job.Cancel()
		}
	}

	s.timersMu.Lock()
	if tm, exists := s.syncTimers[id]; exists {
		tm.Stop()
		delete(s.syncTimers, id)
	}
	s.timersMu.Unlock()

	return nil
}

// ClearCompletedJobs очищает завершенные, упавшие и отмененные задачи.
func (s *Service) ClearCompletedJobs(ctx context.Context) int {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	count := 0
	for id, job := range s.jobs {
		if job.Snapshot().CleanupPending != "" {
			continue
		}
		switch job.Snapshot().Status {
		case StatusReady, StatusPartialReady, StatusError, StatusCancelled:
			pending, err := downloadstaging.HasPendingImport(s.tmpSandbox, id)
			if err == nil && !pending && s.deleteJobLocked(ctx, id, true) == nil {
				count++
			}
		}
	}
	return count
}

// AddJob регистрирует задачу в памяти.
func (s *Service) AddJob(job *Job) {
	if job == nil {
		return
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	s.registerJobLocked(job)
}

func (s *Service) registerJobLocked(job *Job) {
	s.jobs[job.ID] = job
	job.SetOnUpdate(func() { s.syncJobDebounced(job) })
}

// GetJob возвращает задачу по ID.
func (s *Service) GetJob(id string) (*Job, bool) {
	s.jobsMu.RLock()
	job := s.jobs[id]
	s.jobsMu.RUnlock()
	if job != nil || s.repo == nil {
		return job, job != nil
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if job := s.jobs[id]; job != nil {
		return job, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	job, err := s.repo.GetJob(ctx, id)
	if err != nil || job == nil {
		return nil, false
	}
	s.registerJobLocked(job)
	return job, true
}

// GetAllJobs возвращает снимок всех задач из памяти.
func (s *Service) GetAllJobs() []*Job {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()

	res := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		res = append(res, j)
	}
	return res
}

// GetSortedJobSnapshots возвращает детерминированно отсортированные снимки всех задач.
// Гарантирует строгий слабый порядок (strict weak ordering) и полную потокобезопасность
// даже при активных фоновых воркерах, модифицирующих состояние задач.
// Порядок сортировки:
// 1. Приоритет статуса: 1 (QUEUED) -> 2 (IN PROGRESS) -> 3 (READY) -> 4 (ERROR) -> 5 (CANCELLED)
// 2. Время создания (CreatedAt): DESC (более новые сначала)
// 3. Идентификатор (ID): ASC (лексикографический tie-breaker)
func (s *Service) GetSortedJobSnapshots() []*Job {
	s.jobsMu.RLock()
	liveJobs := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		liveJobs = append(liveJobs, j)
	}
	s.jobsMu.RUnlock()

	snapshots := make([]*Job, len(liveJobs))
	for i, j := range liveJobs {
		snapshots[i] = j.Snapshot()
	}

	sort.Slice(snapshots, func(i, j int) bool {
		pI := snapshots[i].Status.Priority()
		pJ := snapshots[j].Status.Priority()
		if pI != pJ {
			return pI < pJ
		}
		if snapshots[i].CreatedAt != snapshots[j].CreatedAt {
			return snapshots[i].CreatedAt > snapshots[j].CreatedAt
		}
		return snapshots[i].ID < snapshots[j].ID
	})

	return snapshots
}

// GetSummary рассчитывает сводку очереди задач.
func (s *Service) GetSummary() QueueSummaryResponse {
	all := s.GetAllJobs()

	var summary QueueSummaryResponse
	totalProg := 0.0

	for _, j := range all {
		snap := j.Snapshot()
		switch snap.Status {
		case StatusQueued:
			summary.QueuedJobs++
		case StatusResolving, StatusDownloading, StatusConverting, StatusTagging:
			summary.ActiveJobs++
		case StatusReady, StatusPartialReady:
			summary.CompletedJobs++
		case StatusError:
			summary.ErrorJobs++
		}

		summary.TotalTracks += snap.TotalTracks
		summary.CompletedTracks += snap.CompletedTracks
		totalProg += snap.Progress
	}

	if len(all) > 0 {
		summary.OverallProgress = totalProg / float64(len(all))
	}
	summary.IsPaused = s.IsQueuePaused()

	return summary
}

// syncJobDebounced синхронизирует состояние задачи с SQLite с дебаунсом.
func (s *Service) syncJobDebounced(job *Job) {
	if s.repo == nil || job == nil {
		return
	}

	snap := job.Snapshot()
	isTerminal := snap.Status == StatusReady || snap.Status == StatusPartialReady || snap.Status == StatusError || snap.Status == StatusCancelled || snap.Status == StatusWaitingSource

	saveFn := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.jobsMu.Lock()
		defer s.jobsMu.Unlock()
		if s.jobs[job.ID] != job {
			return
		}
		current := job.Snapshot()
		if current.Status == StatusQueued || current.selectionCorrupt || current.CleanupPending != "" {
			return
		}
		if err := s.repo.updateProgressSnapshot(ctx, current); err != nil && !errors.Is(err, ErrJobNotFound) {
			log.Printf("[Downloader] failed to persist job %s: %v", job.ID, err)
		}
	}

	if isTerminal {
		s.timersMu.Lock()
		if tm, exists := s.syncTimers[snap.ID]; exists {
			tm.Stop()
			delete(s.syncTimers, snap.ID)
		}
		s.timersMu.Unlock()
		saveFn()
		return
	}

	s.timersMu.Lock()
	defer s.timersMu.Unlock()

	if prev, exists := s.syncTimers[snap.ID]; exists {
		prev.Stop()
	}

	s.syncTimers[snap.ID] = time.AfterFunc(400*time.Millisecond, func() {
		s.timersMu.Lock()
		delete(s.syncTimers, snap.ID)
		s.timersMu.Unlock()
		saveFn()
	})
}

// JobRegistry предоставляет фасад доступа к реестру задач в стиле Jobs().
type JobRegistry struct {
	svc *Service
}

// Jobs возвращает фасад JobRegistry.
func (s *Service) Jobs() *JobRegistry {
	return &JobRegistry{svc: s}
}

func (r *JobRegistry) Get(id string) (*Job, bool) {
	return r.svc.GetJob(id)
}

func (r *JobRegistry) GetAll() []*Job {
	return r.svc.GetAllJobs()
}

func (r *JobRegistry) Add(job *Job) {
	r.svc.AddJob(job)
}

func (r *JobRegistry) Delete(id string) {
	_ = r.svc.DeleteJob(context.Background(), id)
}

func (r *JobRegistry) GetSummary() QueueSummaryResponse {
	return r.svc.GetSummary()
}

func (r *JobRegistry) ClearCompleted() int {
	return r.svc.ClearCompletedJobs(context.Background())
}
