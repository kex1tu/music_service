package downloader

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/censorcheck"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/resolver"
)

var (
	// ErrInvalidURL возвращается при недопустимом или потенциально опасном URL.
	ErrInvalidURL = errors.New("untrusted or invalid target_url")

	progressRegex = regexp.MustCompile(`(?:\[download\]|\bdownload:)?\s*(\d+(?:\.\d+)?)%`)
	rolloffRegex  = regexp.MustCompile(`lavfi\.aspectralstats(?:\.\d+)?\.rolloff=([0-9.]+)`)
)

// JobStarter определяет потребительский интерфейс для постановки задач другими модулями (radar, audit).
type JobStarter interface {
	CreateJob(ctx context.Context, rawURL, source string) (*Job, error)
}

// ServiceConfig определяет зависимости и параметры конфигурации сервиса загрузок.
type ServiceConfig struct {
	Repo            *Repository
	Resolver        *resolver.SourceResolver
	Matcher         *resolver.AudioMatcher
	Lyrics          *lyrics.Service
	TmpSandbox      *sandbox.PathSandbox
	MusicSandbox    *sandbox.PathSandbox
	CookiesPath     string
	MaxConcurrent   int
	CleanerMaxAge   time.Duration
	CleanerInterval time.Duration
}

// Service координирует жизненный цикл задач загрузки, воркер-пул, дебаунс-синхронизацию с SQLite и файловую песочницу.
type Service struct {
	repo         *Repository
	resolver     *resolver.SourceResolver
	matcher      *resolver.AudioMatcher
	lyrics       *lyrics.Service
	tmpSandbox   *sandbox.PathSandbox
	musicSandbox *sandbox.PathSandbox
	cookiesPath  string

	// Bounded worker pool
	workersCount int
	workQueue    chan string
	wg           sync.WaitGroup
	ctx          context.Context
	cancel       context.CancelFunc
	stopped      int32

	// Управление паузой очереди
	pauseMu   sync.RWMutex
	isPaused  bool
	pauseCond *sync.Cond

	// In-memory реестр задач
	jobsMu sync.RWMutex
	jobs   map[string]*Job

	// Дебаунс синхронизации с базой данных
	timersMu   sync.Mutex
	syncTimers map[string]*time.Timer

	client          *http.Client
	cleanerMaxAge   time.Duration
	cleanerInterval time.Duration
}

// NewService создает новый экземпляр Service без запуска фоновых горутин.
func NewService(cfg ServiceConfig) *Service {
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	cleanerAge := cfg.CleanerMaxAge
	if cleanerAge <= 0 {
		cleanerAge = 24 * time.Hour
	}
	cleanerInt := cfg.CleanerInterval
	if cleanerInt <= 0 {
		cleanerInt = 1 * time.Hour
	}

	matcher := cfg.Matcher
	if matcher == nil && cfg.Resolver != nil {
		matcher = cfg.Resolver.Matcher()
	}

	svc := &Service{
		repo:            cfg.Repo,
		resolver:        cfg.Resolver,
		matcher:         matcher,
		lyrics:          cfg.Lyrics,
		tmpSandbox:      cfg.TmpSandbox,
		musicSandbox:    cfg.MusicSandbox,
		cookiesPath:     cfg.CookiesPath,
		workersCount:    maxConcurrent,
		workQueue:       make(chan string, 1024),
		jobs:            make(map[string]*Job),
		syncTimers:      make(map[string]*time.Timer),
		client:          &http.Client{Timeout: 30 * time.Second},
		cleanerMaxAge:   cleanerAge,
		cleanerInterval: cleanerInt,
	}
	svc.pauseCond = sync.NewCond(&svc.pauseMu)
	return svc
}

// SetLyricsService переопределяет сервис текстов песен (для тестирования или отложенного внедрения).
func (s *Service) SetLyricsService(l *lyrics.Service) {
	s.lyrics = l
}

// Start запускает воркер-пул, фоновую очистку и сбрасывает зависшие задачи в БД.
func (s *Service) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.ctx, s.cancel = context.WithCancel(ctx)

	// 1. Сброс прерванных задач в SQLite
	if s.repo != nil {
		if _, err := s.repo.ResetInterruptedJobs(s.ctx); err != nil {
			log.Printf("[Downloader] Warning: failed to reset interrupted jobs: %v", err)
		}
		// Загрузка сохраненных задач в память
		savedJobs, err := s.repo.GetAllJobs(s.ctx)
		if err == nil {
			s.jobsMu.Lock()
			for _, j := range savedJobs {
				st := j.Status
				curStep := j.CurrentStep
				if st == StatusQueued || st == StatusResolving || st == StatusDownloading || st == StatusConverting || st == StatusTagging {
					st = StatusError
					curStep = "Прервано перезапуском сервера. Нажмите «Повторить»"
					j.Status = st
					j.CurrentStep = curStep
				}
				s.jobs[j.ID] = j
				j.SetOnUpdate(func() {
					s.syncJobDebounced(j)
				})
			}
			s.jobsMu.Unlock()
		}
	}

	// 2. Запуск фиксированного пула воркеров
	for i := 0; i < s.workersCount; i++ {
		s.wg.Add(1)
		go s.workerLoop(s.ctx, i)
	}

	// 3. Запуск демона очистки старых временных директорий
	s.wg.Add(1)
	go s.cleanerLoop(s.ctx)

	return nil
}

// Stop выполняет graceful shutdown: отменяет контекст, пробуждает воркеры и ожидает их завершения.
func (s *Service) Stop(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&s.stopped, 0, 1) {
		return nil
	}

	if s.cancel != nil {
		s.cancel()
	}

	s.pauseMu.Lock()
	s.isPaused = false
	s.pauseMu.Unlock()
	if s.pauseCond != nil {
		s.pauseCond.Broadcast()
	}

	// Ожидаем завершения всех горутин пула
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		return fmt.Errorf("downloader: stop timed out: %w", ctx.Err())
	}

	// Останавливаем все висящие таймеры дебаунса и сбрасываем состояние
	s.timersMu.Lock()
	for id, tm := range s.syncTimers {
		tm.Stop()
		delete(s.syncTimers, id)
	}
	s.timersMu.Unlock()

	return nil
}

// Close реализует io.Closer для совместимости с defer.
func (s *Service) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.Stop(ctx)
}

func (s *Service) workerLoop(ctx context.Context, workerID int) {
	defer s.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case jobID, ok := <-s.workQueue:
			if !ok {
				return
			}
			if ctx.Err() != nil {
				return
			}
			if err := s.waitIfPaused(ctx); err != nil {
				return
			}
			s.executeJob(ctx, jobID)
		}
	}
}

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

// CreateJob создает и ставит в очередь новую задачу загрузки.
func (s *Service) CreateJob(ctx context.Context, rawURL, source string) (*Job, error) {
	cleanURL := strings.TrimSpace(rawURL)
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
		ID:          jobID,
		Status:      StatusQueued,
		Source:      source,
		URL:         cleanURL,
		Progress:    0.0,
		CurrentStep: "Задача ожидает очереди",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:   time.Now().UTC(),
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
			_ = os.RemoveAll(jobDir)
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
	s.jobsMu.Lock()
	job, ok := s.jobs[id]
	if ok {
		delete(s.jobs, id)
	}
	s.jobsMu.Unlock()

	if ok && job != nil {
		job.Cancel()
	}

	if s.repo != nil {
		_ = s.repo.DeleteJob(ctx, id)
	}

	if s.tmpSandbox != nil {
		if jobDir, err := s.tmpSandbox.Resolve(id, false); err == nil {
			_ = os.RemoveAll(jobDir)
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
	all := s.GetAllJobs()
	count := 0
	for _, j := range all {
		snap := j.Snapshot()
		if snap.Status == StatusReady || snap.Status == StatusError || snap.Status == StatusCancelled {
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

// executeJob запускает конвейер обработки задачи.
func (s *Service) executeJob(parentCtx context.Context, jobID string) {
	job, ok := s.GetJob(jobID)
	if !ok {
		return
	}

	ctx, cancel := context.WithCancel(parentCtx)
	job.SetCancelFunc(cancel)
	defer cancel()

	if s.waitIfPaused(ctx) != nil {
		return
	}

	if job.IsBackfill {
		s.executeBackfill(ctx, job)
	} else {
		s.executeRegular(ctx, job)
	}
}

func (s *Service) executeRegular(ctx context.Context, job *Job) {
	job.SetStatus(StatusResolving)
	job.UpdateProgress(5.0, "Определение источника и метаданных")

	if s.resolver == nil {
		// Режим без внешнего резолвера (например, минимальные тесты)
		job.SetReady(job.CompletedTracks, job.TotalTracks)
		return
	}

	meta, tracks, err := s.resolver.Resolve(ctx, job.URL, job.Source)
	if err != nil {
		job.SetError(fmt.Sprintf("Ошибка разрешения: %v", err))
		return
	}

	if meta == nil {
		meta = &resolver.ReleaseMetadata{}
	}
	job.SetMetadata(*meta, tracks)
	job.UpdateProgress(10.0, "Метаданные получены, запуск загрузки")

	if len(tracks) == 0 {
		job.SetError("Релиз не содержит доступных треков")
		return
	}

	rawDir := ""
	convertedDir := ""
	if s.tmpSandbox != nil {
		var err error
		rawDir, err = s.tmpSandbox.Resolve(filepath.Join(job.ID, "raw"), true)
		if err != nil {
			job.SetError(fmt.Sprintf("Песочница: %v", err))
			return
		}
		convertedDir, err = s.tmpSandbox.Resolve(filepath.Join(job.ID, "converted"), true)
		if err != nil {
			job.SetError(fmt.Sprintf("Песочница: %v", err))
			return
		}
		_ = os.MkdirAll(rawDir, 0755)
		_ = os.MkdirAll(convertedDir, 0755)
	}

	s.processTracks(ctx, job, meta, tracks, rawDir, convertedDir)
}

func (s *Service) executeBackfill(ctx context.Context, job *Job) {
	job.SetStatus(StatusResolving)
	job.UpdateProgress(5.0, "Определение недостающих треков для дозагрузки")

	var meta *resolver.ReleaseMetadata
	var tracks []resolver.ResolvedTrack

	if s.repo != nil && job.TargetAlbumPath != nil && *job.TargetAlbumPath != "" {
		missing, err := s.repo.GetMissingTracksForAlbum(ctx, *job.TargetAlbumPath)
		if err == nil {
			for _, m := range missing {
				tracks = append(tracks, resolver.ResolvedTrack{
					TrackNumber:    m.TrackNumber,
					Title:          m.Title,
					Artist:         m.Artist,
					Duration:       m.ExpectedDuration,
					AudioSourceURL: m.DeezerURL,
					Status:         "pending",
				})
			}
		}
	}

	if meta == nil {
		meta = &resolver.ReleaseMetadata{}
	}
	if job.AlbumTitle != nil {
		meta.AlbumTitle = *job.AlbumTitle
	}
	if job.ArtistName != nil {
		meta.AlbumArtist = *job.ArtistName
	}

	if len(tracks) == 0 {
		job.SetError("Не удалось найти недостающие треки")
		return
	}

	job.SetMetadata(*meta, tracks)

	rawDir := ""
	convertedDir := ""
	if s.tmpSandbox != nil {
		var err error
		rawDir, err = s.tmpSandbox.Resolve(filepath.Join(job.ID, "raw"), true)
		if err != nil {
			job.SetError(fmt.Sprintf("Песочница: %v", err))
			return
		}
		convertedDir, err = s.tmpSandbox.Resolve(filepath.Join(job.ID, "converted"), true)
		if err != nil {
			job.SetError(fmt.Sprintf("Песочница: %v", err))
			return
		}
		_ = os.MkdirAll(rawDir, 0755)
		_ = os.MkdirAll(convertedDir, 0755)
	}

	s.processTracks(ctx, job, meta, tracks, rawDir, convertedDir)
}

func (s *Service) processTracks(
	ctx context.Context,
	job *Job,
	meta *resolver.ReleaseMetadata,
	tracks []resolver.ResolvedTrack,
	rawDir, convertedDir string,
) {
	total := len(tracks)
	if total == 0 {
		job.SetReady(0, 0)
		return
	}

	// Загружаем обложку
	var coverBytes []byte
	if meta.CoverURL != "" {
		if cBytes, err := s.downloadCover(ctx, meta.CoverURL); err == nil && len(cBytes) > 0 {
			coverBytes = cBytes
			if rawDir != "" {
				_ = os.WriteFile(filepath.Join(rawDir, "cover.jpg"), cBytes, 0644)
			}
			if convertedDir != "" {
				_ = os.WriteFile(filepath.Join(convertedDir, "cover.jpg"), cBytes, 0644)
				job.SetCoverPath(filepath.Join(job.ID, "converted", "cover.jpg"))
			}
		}
	}

	completedCount := 0
	hasErrors := false

	// Параллельная обработка треков альбома с ограничением
	concurrency := runtime.NumCPU() / 2
	if concurrency < 2 {
		concurrency = 2
	}
	if concurrency > 4 {
		concurrency = 4
	}
	sem := make(chan struct{}, concurrency)

	var wg sync.WaitGroup
	var mu sync.Mutex

	for idx := range tracks {
		if ctx.Err() != nil {
			job.SetError("Загрузка отменена")
			break
		}

		wg.Add(1)
		go func(trackIdx int, t resolver.ResolvedTrack) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			success := s.processSingleTrack(ctx, job, meta, trackIdx, t, rawDir, convertedDir, coverBytes, total)
			mu.Lock()
			if success {
				completedCount++
				job.IncrementCompleted(total)
			} else {
				hasErrors = true
			}
			mu.Unlock()
		}(idx+1, tracks[idx])
	}

	wg.Wait()

	if ctx.Err() != nil {
		job.SetError("Загрузка отменена")
		return
	}

	if completedCount == total {
		job.SetReady(completedCount, total)
	} else if completedCount > 0 {
		job.SetPartialReady(completedCount, total)
	} else if hasErrors {
		job.SetError("Ни один трек не удалось загрузить")
	}
}

func (s *Service) processSingleTrack(
	ctx context.Context,
	job *Job,
	meta *resolver.ReleaseMetadata,
	trackIdx int,
	track resolver.ResolvedTrack,
	rawDir, convertedDir string,
	coverBytes []byte,
	total int,
) bool {
	streamURL := track.AudioSourceURL

	// SoundCloud-First сопоставление, если ссылка отсутствует или является мета-ссылкой Deezer
	if streamURL == "" || strings.Contains(streamURL, "deezer.com") {
		streamURL = ""
		trackISRC := ""
		if track.ISRC != nil {
			trackISRC = *track.ISRC
		}
		cacheKey := resolver.BuildCacheKey(trackISRC, track.Artist, track.Title)

		// 1. Проверяем ручной пин
		if s.repo != nil {
			if pin, err := s.repo.GetPinnedSource(ctx, cacheKey); err == nil && pin != nil && pin.PinnedURL != "" {
				if !strings.Contains(pin.PinnedURL, "deezer.com") {
					streamURL = pin.PinnedURL
				}
			}
		}

		// 2. Проверяем кэш SoundCloud
		if streamURL == "" && s.repo != nil {
			if sc, err := s.repo.GetSoundCloudCache(ctx, cacheKey, 720); err == nil && sc != nil && sc.SelectedURL != "" {
				streamURL = sc.SelectedURL
			}
		}

		// 3. Вызываем AudioMatcher (SoundCloud с фоллбэком на YouTube)
		if streamURL == "" && s.matcher != nil {
			match, err := s.matcher.FindBestMatchWithDetails(ctx, track.Title, track.Artist, trackISRC, track.Duration)
			if err == nil && match != nil && match.URL != "" {
				streamURL = match.URL
				track.AudioSourceURL = match.URL
				track.VersionStatus = match.Status
				track.Candidates = match.Candidates
				job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
					tr.AudioSourceURL = match.URL
					tr.VersionStatus = match.Status
					tr.Candidates = match.Candidates
				})
			}
		}
	}

	if streamURL == "" {
		errStr := "Не найден подходящий аудиопоток"
		job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
			tr.Status = "error"
			tr.Error = &errStr
		})
		return false
	}

	job.SetStepAndStatus(fmt.Sprintf("Скачивание трека %d/%d: %s", trackIdx, total, track.Title), StatusDownloading)

	outTemplate := filepath.Join(rawDir, fmt.Sprintf("%02d_track.%%(ext)s", trackIdx))
	rawAudioFile, err := s.downloadRawStream(ctx, streamURL, outTemplate, func(pct float64) {
		job.UpdateTrackProgress(trackIdx-1, pct, fmt.Sprintf("Скачивание %d/%d: %.0f%%", trackIdx, total, pct))
	})
	if err != nil && s.matcher != nil {
		errLower := strings.ToLower(err.Error())
		isDRM := strings.Contains(errLower, "drm")
		isSC := strings.Contains(streamURL, "soundcloud.com")
		isYT := strings.Contains(streamURL, "youtube.com") || strings.Contains(streamURL, "youtu.be")

		trackISRC := ""
		if track.ISRC != nil {
			trackISRC = *track.ISRC
		}
		cacheKey := resolver.BuildCacheKey(trackISRC, track.Artist, track.Title)

		if isSC {
			log.Printf("[Downloader] Track '%s - %s' SoundCloud stream failed (%s, err: %v). Rotating candidate...", track.Artist, track.Title, streamURL, err)
			if s.repo != nil {
				if isDRM {
					_ = s.repo.DeletePinnedSource(ctx, cacheKey)
				}
				invEntry, _ := s.repo.InvalidateSoundCloudURL(ctx, cacheKey, streamURL)
				if invEntry != nil && invEntry.SelectedURL != "" && invEntry.SelectedURL != streamURL {
					log.Printf("[Downloader] Rotating to next SoundCloud candidate for '%s - %s': %s", track.Artist, track.Title, invEntry.SelectedURL)
					streamURL = invEntry.SelectedURL
					track.AudioSourceURL = invEntry.SelectedURL
					track.VersionStatus = invEntry.Status
					job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
						tr.AudioSourceURL = invEntry.SelectedURL
						tr.VersionStatus = invEntry.Status
					})
					rawAudioFile, err = s.downloadRawStream(ctx, invEntry.SelectedURL, outTemplate, func(pct float64) {
						job.UpdateTrackProgress(trackIdx-1, pct, fmt.Sprintf("Скачивание (SoundCloud) %d/%d: %.0f%%", trackIdx, total, pct))
					})
				}
			}

			// Если SoundCloud всё ещё возвращает ошибку — фоллбэк на YouTube
			if err != nil {
				job.SetStepAndStatus(fmt.Sprintf("Резервный поиск YouTube %d/%d: %s", trackIdx, total, track.Title), StatusDownloading)
				if ytURL, ytErr := s.matcher.FindYouTubeMatch(ctx, track.Title, track.Artist, track.Duration); ytErr == nil && ytURL != "" && ytURL != streamURL {
					log.Printf("[Downloader] Found YouTube fallback for '%s - %s': %s", track.Artist, track.Title, ytURL)
					streamURL = ytURL
					track.AudioSourceURL = ytURL
					track.VersionStatus = ""
					job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
						tr.AudioSourceURL = ytURL
						tr.VersionStatus = ""
					})
					rawAudioFile, err = s.downloadRawStream(ctx, ytURL, outTemplate, func(pct float64) {
						job.UpdateTrackProgress(trackIdx-1, pct, fmt.Sprintf("Скачивание (YouTube) %d/%d: %.0f%%", trackIdx, total, pct))
					})
				}
			}
		} else {
			// Если упал YouTube или мета-ссылка (Deezer / DRM) — фоллбэк на SoundCloud
			log.Printf("[Downloader] Track '%s - %s' stream failed (%s, err: %v). Attempting SoundCloud fallback...", track.Artist, track.Title, streamURL, err)
			if s.repo != nil && isDRM {
				_ = s.repo.DeletePinnedSource(ctx, cacheKey)
			}
			job.SetStepAndStatus(fmt.Sprintf("Резервный поиск SoundCloud %d/%d: %s", trackIdx, total, track.Title), StatusDownloading)
			if scRes, scErr := s.matcher.FindUncensoredSoundCloudMatch(ctx, track.Title, track.Artist, trackISRC, track.Duration); scErr == nil && scRes != nil && scRes.URL != "" && scRes.URL != streamURL {
				log.Printf("[Downloader] Found SoundCloud fallback for '%s - %s': %s", track.Artist, track.Title, scRes.URL)
				streamURL = scRes.URL
				track.AudioSourceURL = scRes.URL
				track.VersionStatus = scRes.Status
				track.Candidates = scRes.Candidates
				job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
					tr.AudioSourceURL = scRes.URL
					tr.VersionStatus = scRes.Status
					tr.Candidates = scRes.Candidates
				})
				rawAudioFile, err = s.downloadRawStream(ctx, scRes.URL, outTemplate, func(pct float64) {
					job.UpdateTrackProgress(trackIdx-1, pct, fmt.Sprintf("Скачивание (SoundCloud) %d/%d: %.0f%%", trackIdx, total, pct))
				})
			}

			// Если SoundCloud не дал результат и исходная ссылка была не YouTube — пробуем YouTube
			if err != nil && !isYT {
				job.SetStepAndStatus(fmt.Sprintf("Резервный поиск YouTube %d/%d: %s", trackIdx, total, track.Title), StatusDownloading)
				if ytURL, ytErr := s.matcher.FindYouTubeMatch(ctx, track.Title, track.Artist, track.Duration); ytErr == nil && ytURL != "" && ytURL != streamURL {
					log.Printf("[Downloader] Found YouTube fallback for '%s - %s': %s", track.Artist, track.Title, ytURL)
					streamURL = ytURL
					track.AudioSourceURL = ytURL
					track.VersionStatus = ""
					job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
						tr.AudioSourceURL = ytURL
						tr.VersionStatus = ""
					})
					rawAudioFile, err = s.downloadRawStream(ctx, ytURL, outTemplate, func(pct float64) {
						job.UpdateTrackProgress(trackIdx-1, pct, fmt.Sprintf("Скачивание (YouTube) %d/%d: %.0f%%", trackIdx, total, pct))
					})
				}
			}
		}
	}

	if err != nil {
		errStr := err.Error()
		job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
			tr.Status = "error"
			tr.Error = &errStr
		})
		return false
	}

	// Анализ аудио-качества (telemetry)
	quality := s.analyzeAudioQuality(ctx, rawAudioFile)
	job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
		tr.AudioQuality = quality
	})

	// Конвертация в MP3 320 kbps CBR
	job.SetStepAndStatus(fmt.Sprintf("Конвертация трека %d/%d: %s в MP3 320k", trackIdx, total, track.Title), StatusConverting)
	cleanTitle := id3.CleanTitle(track.Title, fmt.Sprintf("Track_%02d", trackIdx))
	safeTitle := sandbox.SanitizeFilename(cleanTitle, fmt.Sprintf("Track_%02d", trackIdx))
	mp3Filename := fmt.Sprintf("%02d. %s.mp3", track.TrackNumber, safeTitle)
	mp3Path := filepath.Join(convertedDir, mp3Filename)

	if err := s.transcodeToMP3(ctx, rawAudioFile, mp3Path); err != nil {
		errStr := err.Error()
		job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
			tr.Status = "error"
			tr.Error = &errStr
		})
		return false
	}

	// Запись ID3v2.4 тегов
	job.SetStepAndStatus(fmt.Sprintf("Запись ID3v2.4 тегов %d/%d: %s", trackIdx, total, track.Title), StatusTagging)

	art := strings.TrimSpace(track.Artist)
	albArt := strings.TrimSpace(track.AlbumArtist)
	alb := strings.TrimSpace(track.Album)
	if alb == "" {
		alb = meta.AlbumTitle
	}
	if albArt == "" {
		albArt = meta.AlbumArtist
	}
	if albArt == "" && art != "" {
		albArt = art
	}

	tagData := id3.TrackTags{
		Path:        mp3Path,
		Title:       track.Title,
		Artist:      art,
		Album:       alb,
		AlbumArtist: albArt,
		TrackNumber: track.TrackNumber,
		DiscNumber:  track.DiscNumber,
		Duration:    float64(track.Duration),
	}
	if track.TrackTotal != nil {
		tagData.TrackTotal = *track.TrackTotal
	} else if total > 0 {
		tagData.TrackTotal = total
	}
	if track.DiscTotal != nil {
		tagData.DiscTotal = *track.DiscTotal
	}
	if track.Year != nil {
		tagData.Year = *track.Year
	} else if meta.Year > 0 {
		tagData.Year = meta.Year
	}
	if track.Genre != nil {
		tagData.Genre = *track.Genre
	} else if meta.Genre != "" {
		tagData.Genre = meta.Genre
	}

	var cover *id3.CoverInfo
	if len(coverBytes) > 0 {
		mime := "image/jpeg"
		if bytes.HasPrefix(coverBytes, []byte("\x89PNG")) {
			mime = "image/png"
		}
		cover = &id3.CoverInfo{
			MimeType: mime,
			Data:     coverBytes,
		}
	}

	_ = id3.WriteTags(mp3Path, tagData, cover)

	// Поиск синхронизированного текста через LRCLIB и извлечение контрольных точек отслушивания
	var spots []censorcheck.QuickListenSpot
	if s.lyrics != nil {
		lrcRes, _ := s.lyrics.SearchLyrics(ctx, track.Artist, track.Title, meta.AlbumTitle, track.Duration)
		if lrcRes != nil && lrcRes.Synced && lrcRes.Lyrics != "" {
			lrcFilename := fmt.Sprintf("%02d. %s.lrc", track.TrackNumber, safeTitle)
			lrcPath := filepath.Join(convertedDir, lrcFilename)
			_ = os.WriteFile(lrcPath, []byte(lrcRes.Lyrics), 0644)

			spots = censorcheck.ExtractSpots(lrcRes.Lyrics, 3)
		}
	}

	job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
		tr.Status = "ready"
		convPath := filepath.Join(job.ID, "converted", mp3Filename)
		tr.ConvertedPath = &convPath
		if len(spots) > 0 {
			tr.QuickListenSpots = spots
		}
	})

	return true
}

func (s *Service) downloadRawStream(ctx context.Context, sourceURL, outputTemplate string, onProgress func(float64)) (string, error) {
	args := []string{
		"--ignore-config",
		"--no-download-archive",
		"-f", "ba/b",
		"-o", outputTemplate,
		"--no-playlist",
		"--newline",
		"--no-colors",
		"--progress",
		"--progress-template", "download:%(progress._percent_str)s",
		"--progress-delta", "0.2",
		"--no-warnings",
		"--concurrent-fragments", "4",
		"--buffer-size", "64k",
		"--extractor-args", "youtube:player_client=tv_embedded,web_embedded,android,ios",
		"--add-header", "Accept-Language:ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7",
	}

	useCookies := resolver.IsValidNetscapeCookiesFile(s.cookiesPath)
	if useCookies {
		args = append(args, "--cookies", s.cookiesPath)
	}
	args = append(args, "--", sourceURL)

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start yt-dlp: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		match := progressRegex.FindStringSubmatch(line)
		if len(match) >= 2 && onProgress != nil {
			if val, pErr := strconv.ParseFloat(match[1], 64); pErr == nil {
				onProgress(val)
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		if useCookies && strings.Contains(stderr.String(), "does not look like a Netscape format cookies file") {
			argsNoCookies := make([]string, 0, len(args))
			for i := 0; i < len(args); i++ {
				if args[i] == "--cookies" {
					i++
					continue
				}
				argsNoCookies = append(argsNoCookies, args[i])
			}
			cmdRetry := exec.CommandContext(ctx, "yt-dlp", argsNoCookies...)
			stdoutRetry, pErr := cmdRetry.StdoutPipe()
			if pErr == nil {
				var stderrRetry bytes.Buffer
				cmdRetry.Stderr = &stderrRetry
				if sErr := cmdRetry.Start(); sErr == nil {
					scannerRetry := bufio.NewScanner(stdoutRetry)
					for scannerRetry.Scan() {
						line := scannerRetry.Text()
						match := progressRegex.FindStringSubmatch(line)
						if len(match) >= 2 && onProgress != nil {
							if val, valErr := strconv.ParseFloat(match[1], 64); valErr == nil {
								onProgress(val)
							}
						}
					}
					if wErr := cmdRetry.Wait(); wErr == nil {
						return s.findDownloadedFile(outputTemplate)
					}
					stderr = stderrRetry
				}
			}
		}
		return "", fmt.Errorf("yt-dlp download failed: %w: %s", err, stderr.String())
	}

	return s.findDownloadedFile(outputTemplate)
}

func (s *Service) findDownloadedFile(outputTemplate string) (string, error) {
	dir := filepath.Dir(outputTemplate)
	pattern := filepath.Base(strings.Replace(outputTemplate, "%(ext)s", "*", 1))
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("downloaded audio file not found matching template %s", outputTemplate)
	}
	return matches[0], nil
}

func (s *Service) transcodeToMP3(ctx context.Context, inputRaw, outputMP3 string) error {
	args := []string{
		"-y",
		"-threads", "0",
		"-i", inputRaw,
		"-vn",
		"-codec:a", "libmp3lame",
		"-b:a", "320k",
		"-q:a", "0",
		"-ar", "44100",
		"-ac", "2",
		"-map_metadata", "-1",
		outputMP3,
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg transcoding error: %w: %s", err, stderr.String())
	}

	info, err := os.Stat(outputMP3)
	if err != nil || info.Size() == 0 {
		return errors.New("transcoded MP3 is empty or missing")
	}

	return nil
}

func (s *Service) downloadCover(ctx context.Context, coverURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download cover: status %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func (s *Service) analyzeAudioQuality(ctx context.Context, inputRaw string) *resolver.AudioQualityTelemetry {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, "ffprobe",
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=codec_name,bit_rate:format=bit_rate",
		"-of", "json",
		inputRaw,
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run()

	codec := "audio"
	bitrate := 0
	var out struct {
		Streams []struct {
			CodecName string `json:"codec_name"`
			BitRate   string `json:"bit_rate"`
		} `json:"streams"`
		Format struct {
			BitRate string `json:"bit_rate"`
		} `json:"format"`
	}
	if json.Unmarshal(stdout.Bytes(), &out) == nil {
		if len(out.Streams) > 0 {
			codec = strings.ToLower(out.Streams[0].CodecName)
			if b, err := strconv.Atoi(out.Streams[0].BitRate); err == nil {
				bitrate = b
			}
		}
		if bitrate == 0 && out.Format.BitRate != "" {
			if b, err := strconv.Atoi(out.Format.BitRate); err == nil {
				bitrate = b
			}
		}
	}

	cutoffHz := 18500
	if strings.EqualFold(codec, "opus") || bitrate >= 160000 {
		cutoffHz = 19500
	} else if bitrate < 140000 && bitrate > 0 {
		cutoffHz = 15500
	}

	telemetry := &resolver.AudioQualityTelemetry{
		SourceCodec:   codec,
		SourceBitrate: bitrate,
		CutoffHz:      cutoffHz,
	}

	if cutoffHz >= 19500 {
		telemetry.QualityTier = "true_320k"
		telemetry.Description = fmt.Sprintf("Честные 320k (%s) • Срез ~%.1f kHz", strings.ToUpper(codec), float64(cutoffHz)/1000.0)
	} else {
		telemetry.QualityTier = "hq_opus"
		telemetry.Description = fmt.Sprintf("Высокое качество (%s) • Срез ~%.1f kHz", strings.ToUpper(codec), float64(cutoffHz)/1000.0)
	}

	return telemetry
}

// SwitchTrackVersion выполняет ручную смену источника трека.
func (s *Service) SwitchTrackVersion(ctx context.Context, trackID, targetURL string) (*SwitchVersionResult, error) {
	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		return nil, fmt.Errorf("%w: target_url cannot be empty", ErrInvalidURL)
	}
	if strings.HasPrefix(targetURL, "-") {
		return nil, fmt.Errorf("%w: target_url cannot start with '-'", ErrInvalidURL)
	}

	parsedURL, pErr := url.Parse(targetURL)
	if pErr != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return nil, fmt.Errorf("%w: invalid url scheme", ErrInvalidURL)
	}

	if !resolver.IsAllowedURL(targetURL) {
		return nil, fmt.Errorf("%w: domain %q is not allowed", ErrInvalidURL, parsedURL.Hostname())
	}

	// Фиксируем пин в репозитории
	if s.repo != nil {
		_ = s.repo.SavePinnedSource(ctx, PinnedTrackSourceDTO{
			CacheKey:   trackID,
			PinnedURL:  targetURL,
			SourceType: "manual",
			UpdatedAt:  time.Now().UTC(),
		})
	}

	return &SwitchVersionResult{
		TrackID:   trackID,
		TargetURL: targetURL,
		Status:    "manual",
	}, nil
}

// GetCandidates выполняет поиск кандидатов для ручной привязки.
func (s *Service) GetCandidates(ctx context.Context, artist, title string, duration int, query string) ([]resolver.CandidateTrackItem, error) {
	if s.matcher == nil {
		return nil, errors.New("audio matcher is not available")
	}
	return s.matcher.SearchMultiCandidates(ctx, artist, title, duration, query)
}

func (s *Service) cleanerLoop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(s.cleanerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanOldTempDirs()
		}
	}
}

func (s *Service) cleanOldTempDirs() {
	if s.tmpSandbox == nil {
		return
	}
	root := s.tmpSandbox.Root()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-s.cleanerMaxAge)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			dirPath := filepath.Join(root, entry.Name())
			_ = os.RemoveAll(dirPath)
		}
	}
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

// Resolver возвращает SourceResolver.
func (s *Service) Resolver() *resolver.SourceResolver {
	return s.resolver
}

// Repo возвращает Repository.
func (s *Service) Repo() *Repository {
	return s.repo
}

// TmpSandbox возвращает песочницу временных файлов.
func (s *Service) TmpSandbox() *sandbox.PathSandbox {
	return s.tmpSandbox
}

// MusicSandbox возвращает песочницу библиотеки музыки.
func (s *Service) MusicSandbox() *sandbox.PathSandbox {
	return s.musicSandbox
}

// CookiesPath возвращает путь к файлу cookies.
func (s *Service) CookiesPath() string {
	return s.cookiesPath
}
