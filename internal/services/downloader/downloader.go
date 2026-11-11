package downloader

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"

	"strings"
	"sync"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/resolver"
)

// Service orchestrates audio downloading, transcoding and ID3 tagging.
type Service struct {
	jobs         *JobManager
	resolver     *resolver.SourceResolver
	tmpSandbox   *sandbox.PathSandbox
	musicSandbox *sandbox.PathSandbox
	cookiesPath  string
	lyrics       *lyrics.Service
	sem          chan struct{}
	client       *http.Client
	ctx          context.Context
	cancel       context.CancelFunc

	pauseMu   sync.RWMutex
	isPaused  bool
	pauseCond *sync.Cond
}

// NewService creates a new DownloadService.
func NewService(
	ctx context.Context,
	jobs *JobManager,
	res *resolver.SourceResolver,
	tmpSandbox *sandbox.PathSandbox,
	musicSandbox *sandbox.PathSandbox,
	cookiesPath string,
	maxConcurrent int,
) *Service {
	if ctx == nil {
		ctx = context.Background()
	}
	serviceCtx, cancel := context.WithCancel(ctx)
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	svc := &Service{
		jobs:         jobs,
		resolver:     res,
		tmpSandbox:   tmpSandbox,
		musicSandbox: musicSandbox,
		cookiesPath:  cookiesPath,
		sem:          make(chan struct{}, maxConcurrent),
		client:       &http.Client{Timeout: 30 * time.Second},
		ctx:          serviceCtx,
		cancel:       cancel,
	}
	svc.pauseCond = sync.NewCond(&svc.pauseMu)
	go svc.startCleaner(serviceCtx, 24*time.Hour)
	return svc
}

// Close cancels the service context, terminating the background cleaner goroutine.
func (s *Service) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.pauseMu.Lock()
	s.isPaused = false
	s.pauseMu.Unlock()
	if s.pauseCond != nil {
		s.pauseCond.Broadcast()
	}
}

// Jobs returns the underlying JobManager.
func (s *Service) Jobs() *JobManager {
	return s.jobs
}

// Resolver returns the underlying SourceResolver.
func (s *Service) Resolver() *resolver.SourceResolver {
	return s.resolver
}

// CookiesPath returns the configured cookies file path.
func (s *Service) CookiesPath() string {
	return s.cookiesPath
}

// TmpSandbox returns the temporary buffer PathSandbox.
func (s *Service) TmpSandbox() *sandbox.PathSandbox {
	return s.tmpSandbox
}

// MusicSandbox returns the permanent music library PathSandbox.
func (s *Service) MusicSandbox() *sandbox.PathSandbox {
	return s.musicSandbox
}

// SetLyricsService injects the lyrics service into the downloader.
func (s *Service) SetLyricsService(l *lyrics.Service) {
	s.lyrics = l
}

// Database returns the underlying SQLite database via JobManager.
func (s *Service) Database() *db.DB {
	if s.jobs != nil {
		return s.jobs.Database()
	}
	return nil
}

func generateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b)
}

// CreateJob creates and queues a new download task.
func (s *Service) CreateJob(rawURL, source string) (*DownloadJob, error) {
	cleanURL := strings.TrimSpace(rawURL)
	if cleanURL == "" {
		return nil, fmt.Errorf("URL cannot be empty")
	}

	// Always enforce domain whitelist via DetectSource
	detectedSource, err := s.resolver.DetectSource(cleanURL)
	if err != nil {
		return nil, err
	}
	if source == "" || source == "auto" || source == "radar" {
		source = detectedSource
	}

	jobID := generateUUID()
	job := &DownloadJob{
		ID:          jobID,
		Status:      StatusQueued,
		Source:      source,
		URL:         cleanURL,
		Progress:    0.0,
		CurrentStep: "Задача ожидает очереди",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:   time.Now().UTC(),
	}

	s.jobs.Add(job)
	go s.runPipeline(jobID)
	return job, nil
}

// CreateBackfillJob creates and queues a consolidated backfill download task for missing tracks.
func (s *Service) CreateBackfillJob(
	targetAlbumPath string,
	albumTitle, artistName string,
	deezerAlbumID string,
	trackURLs []string,
	trackNumbers []int,
) (*DownloadJob, error) {
	if targetAlbumPath == "" && albumTitle == "" {
		return nil, fmt.Errorf("target album path or title required")
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

	job := &DownloadJob{
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

	s.jobs.Add(job)
	go s.runBackfillPipeline(jobID, deezerAlbumID, trackURLs, trackNumbers)
	return job, nil
}

// CreateBatchJobs creates multiple download jobs.
func (s *Service) CreateBatchJobs(urls []string, source string) ([]*DownloadJob, error) {
	var created []*DownloadJob
	for _, u := range urls {
		j, err := s.CreateJob(u, source)
		if err != nil {
			return nil, err
		}
		created = append(created, j)
	}
	return created, nil
}

// RetryJob resets and restarts a failed or cancelled download task.
func (s *Service) RetryJob(jobID string) (*DownloadJob, error) {
	job, ok := s.jobs.Get(jobID)
	if !ok {
		return nil, fmt.Errorf("задача %s не найдена", jobID)
	}

	snap := job.Snapshot()
	if snap.Status == StatusResolving || snap.Status == StatusDownloading || snap.Status == StatusConverting || snap.Status == StatusTagging {
		return nil, fmt.Errorf("задача уже выполняется")
	}

	// Clean up old temporary files
	jobDir, err := s.tmpSandbox.Resolve(jobID, false)
	if err == nil {
		_ = safefs.RemoveAll(jobDir)
	}

	// Reset job state
	job.mu.Lock()
	job.Status = StatusQueued
	job.Progress = 0.0
	job.CurrentStep = "Задача перезапущена, ожидает очереди"
	job.Error = nil
	job.CompletedTracks = 0
	job.UpdatedAt = time.Now().UTC()
	job.mu.Unlock()
	job.notifyUpdate()

	go s.runPipeline(jobID)
	return job, nil
}

// DeleteJob deletes a job and removes its temporary directory.
func (s *Service) DeleteJob(id string) error {
	s.jobs.Delete(id)
	jobDir, err := s.tmpSandbox.Resolve(id, false)
	if err == nil {
		_ = safefs.RemoveAll(jobDir)
	}
	return nil
}

// PauseQueue приостанавливает запуск новых задач в очереди. Активные задачи завершают текущий шаг.
func (s *Service) PauseQueue() {
	s.pauseMu.Lock()
	s.isPaused = true
	s.pauseMu.Unlock()
}

// ResumeQueue возобновляет работу очереди и пробуждает ожидающие задачи.
func (s *Service) ResumeQueue() {
	s.pauseMu.Lock()
	s.isPaused = false
	s.pauseMu.Unlock()
	if s.pauseCond != nil {
		s.pauseCond.Broadcast()
	}
}

// IsQueuePaused возвращает текущий статус паузы очереди.
func (s *Service) IsQueuePaused() bool {
	s.pauseMu.RLock()
	defer s.pauseMu.RUnlock()
	return s.isPaused
}

// waitIfPaused блокирует выполнение, если очередь на паузе, до возобновления или отмены контекста.
func (s *Service) waitIfPaused(ctx context.Context) error {
	s.pauseMu.Lock()
	defer s.pauseMu.Unlock()
	for s.isPaused {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		s.pauseCond.Wait()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.ctx.Err()
}

// ClearCompletedJobs очищает завершенные, ошибочные и отмененные задачи из памяти, БД и удаляет временные директории.
func (s *Service) ClearCompletedJobs() int {
	allJobs := s.jobs.GetAll()
	clearedIDs := make([]string, 0)
	for _, j := range allJobs {
		snap := j.Snapshot()
		if snap.Status == StatusReady || snap.Status == StatusError || snap.Status == StatusCancelled {
			clearedIDs = append(clearedIDs, snap.ID)
		}
	}

	count := s.jobs.ClearCompleted()
	for _, id := range clearedIDs {
		jobDir, err := s.tmpSandbox.Resolve(id, false)
		if err == nil {
			_ = safefs.RemoveAll(jobDir)
		}
	}
	return count
}

// RetryAllFailedJobs находит все задачи со статусом ERROR или CANCELLED и перезапускает их.
func (s *Service) RetryAllFailedJobs() ([]string, error) {
	allJobs := s.jobs.GetAll()
	var retriedIDs []string
	for _, j := range allJobs {
		snap := j.Snapshot()
		if snap.Status == StatusError || snap.Status == StatusCancelled {
			if _, err := s.RetryJob(snap.ID); err == nil {
				retriedIDs = append(retriedIDs, snap.ID)
			}
		}
	}
	return retriedIDs, nil
}
