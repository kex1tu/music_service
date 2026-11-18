package downloader

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/services/downloadstaging"
	"music-manager/internal/services/resolver"
)

func (s *Service) waitIfPaused(ctx context.Context) error {
	s.pauseMu.Lock()
	defer s.pauseMu.Unlock()
	for s.isPaused {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if s.ctx != nil && s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		s.pauseCond.Wait()
	}
	return nil
}

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
	if s.pauseCond != nil {
		s.pauseCond.Broadcast()
	}
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
	SelectedTrackIndices []int
	SkipExisting         bool
}

// CreateJobWithOptions создает и ставит в очередь новую задачу загрузки с дополнительными опциями.
func (s *Service) CreateJobWithOptions(ctx context.Context, rawURL, source string, opts JobOptions) (*Job, error) {
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
	job := &Job{
		ID:                   jobID,
		Status:               StatusQueued,
		Source:               source,
		URL:                  cleanURL,
		Progress:             0.0,
		CurrentStep:          "Задача ожидает очереди",
		CreatedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:            time.Now().UTC(),
		SelectedTrackIndices: opts.SelectedTrackIndices,
	}

	if s.repo != nil {
		_ = s.repo.CreateJob(ctx, job)
	}
	s.AddJob(job)

	select {
	case s.workQueue <- jobID:
	default:
		// Очередь заполнена; задача останется со статусом QUEUED в БД
	}

	return job, nil
}

// CreateJob создает и ставит в очередь новую задачу загрузки.
func (s *Service) CreateJob(ctx context.Context, rawURL, source string) (*Job, error) {
	return s.CreateJobWithOptions(ctx, rawURL, source, JobOptions{})
}

// CreateBatchJobs создает пачку задач загрузки.
func (s *Service) CreateBatchJobs(ctx context.Context, urls []string, source string) ([]*Job, error) {
	var created []*Job
	for _, u := range urls {
		j, err := s.CreateJob(ctx, u, source)
		if err != nil {
			return nil, err
		}
		created = append(created, j)
	}
	return created, nil
}

// CreateBackfillJob создает задачу дозагрузки недостающих треков релиза.
func (s *Service) CreateBackfillJob(
	ctx context.Context,
	targetAlbumPath, albumTitle, artistName, deezerAlbumID string,
	trackURLs []string,
	trackNumbers []int,
) (*Job, error) {
	if targetAlbumPath == "" && albumTitle == "" {
		return nil, errors.New("target album path or title required")
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
		TargetAlbumPath: &tPath,
		AlbumTitle:      &aTitle,
		ArtistName:      &aArtist,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:       time.Now().UTC(),
	}

	s.AddJob(job)
	if s.repo != nil {
		_ = s.repo.CreateJob(ctx, job)
	}

	select {
	case s.workQueue <- jobID:
	default:
	}

	return job, nil
}

// RetryJob сбрасывает и перезапускает упавшую или отмененную задачу.
func (s *Service) RetryJob(ctx context.Context, jobID string) (*Job, error) {
	job, ok := s.GetJob(jobID)
	if !ok {
		return nil, fmt.Errorf("задача %s не найдена", jobID)
	}

	snap := job.Snapshot()
	if snap.Status == StatusResolving || snap.Status == StatusDownloading || snap.Status == StatusConverting || snap.Status == StatusTagging {
		return nil, errors.New("задача уже выполняется")
	}

	if s.tmpSandbox != nil {
		if jobDir, err := s.tmpSandbox.Resolve(jobID, false); err == nil {
			_ = safefs.RemoveAll(jobDir)
		}
	}

	job.mu.Lock()
	job.Status = StatusQueued
	job.Progress = 0.0
	job.CurrentStep = "Задача перезапущена, ожидает очереди"
	job.Error = nil
	job.CompletedTracks = 0
	job.UpdatedAt = time.Now().UTC()
	job.mu.Unlock()
	job.notifyUpdate()

	if s.repo != nil {
		_ = s.repo.SaveJob(ctx, job)
	}

	select {
	case s.workQueue <- jobID:
	default:
	}

	return job, nil
}

// RetryAllFailedJobs перезапускает все упавшие задачи.
func (s *Service) RetryAllFailedJobs(ctx context.Context) ([]string, error) {
	all := s.GetAllJobs()
	var retried []string
	for _, j := range all {
		snap := j.Snapshot()
		if snap.Status == StatusError || snap.Status == StatusCancelled {
			if _, err := s.RetryJob(ctx, snap.ID); err == nil {
				retried = append(retried, snap.ID)
			}
		}
	}
	return retried, nil
}

// DeleteJob отменяет задачу и удаляет её из памяти, БД и песочницы.
func (s *Service) DeleteJob(ctx context.Context, id string) error {
	return s.deleteJob(ctx, id, true)
}

// FinishImport drops a ready job from the queue once its staged tracks have been consumed.
func (s *Service) FinishImport(ctx context.Context, id string) (bool, error) {
	job, ok := s.GetJob(id)
	if !ok {
		return true, nil
	}
	status := job.Snapshot().Status
	if status != StatusReady && status != StatusPartialReady {
		return false, nil
	}
	pending, err := downloadstaging.HasPendingImport(s.tmpSandbox, id)
	if err != nil || pending {
		return false, err
	}
	if err := s.deleteJob(ctx, id, false); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) deleteJob(ctx context.Context, id string, removeStaging bool) error {
	s.jobsMu.Lock()
	job, ok := s.jobs[id]
	if ok {
		delete(s.jobs, id)
	}
	s.jobsMu.Unlock()

	if ok && job != nil && removeStaging {
		job.Cancel()
	}

	var err error
	if s.repo != nil {
		err = s.repo.DeleteJob(ctx, id)
	}

	if removeStaging && s.tmpSandbox != nil {
		if jobDir, err := s.tmpSandbox.Resolve(id, false); err == nil {
			_ = safefs.RemoveAll(jobDir)
		}
	}

	s.timersMu.Lock()
	if tm, exists := s.syncTimers[id]; exists {
		tm.Stop()
		delete(s.syncTimers, id)
	}
	s.timersMu.Unlock()

	return err
}

// ClearCompletedJobs очищает завершенные, упавшие и отмененные задачи.
func (s *Service) ClearCompletedJobs(ctx context.Context) int {
	all := s.GetAllJobs()
	count := 0
	for _, j := range all {
		snap := j.Snapshot()
		if snap.Status == StatusReady || snap.Status == StatusPartialReady || snap.Status == StatusError || snap.Status == StatusCancelled {
			_ = s.DeleteJob(ctx, snap.ID)
			count++
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
	s.jobs[job.ID] = job
	s.jobsMu.Unlock()

	job.SetOnUpdate(func() {
		s.syncJobDebounced(job)
	})
}

// GetJob возвращает задачу по ID.
func (s *Service) GetJob(id string) (*Job, bool) {
	s.jobsMu.RLock()
	j, ok := s.jobs[id]
	s.jobsMu.RUnlock()
	if ok {
		return j, true
	}

	if s.repo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if fromDB, err := s.repo.GetJob(ctx, id); err == nil && fromDB != nil {
			s.AddJob(fromDB)
			return fromDB, true
		}
	}

	return nil, false
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
	isTerminal := snap.Status == StatusReady || snap.Status == StatusPartialReady || snap.Status == StatusError || snap.Status == StatusCancelled

	saveFn := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.repo.SaveJob(ctx, job.Snapshot())
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
