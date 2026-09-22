package downloader

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/services/resolver"
)

// Service orchestrates audio downloading, transcoding and ID3 tagging.
type Service struct {
	jobs         *JobManager
	resolver     *resolver.SourceResolver
	tmpSandbox   *sandbox.PathSandbox
	musicSandbox *sandbox.PathSandbox
	cookiesPath  string
	sem          chan struct{}
	client       *http.Client
}

// NewService creates a new DownloadService.
func NewService(
	jobs *JobManager,
	res *resolver.SourceResolver,
	tmpSandbox *sandbox.PathSandbox,
	musicSandbox *sandbox.PathSandbox,
	cookiesPath string,
	maxConcurrent int,
) *Service {
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
	}
	go svc.startCleaner(24 * time.Hour)
	return svc
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
	if source == "" || source == "auto" {
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
		_ = os.RemoveAll(jobDir)
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
		_ = os.RemoveAll(jobDir)
	}
	return nil
}
