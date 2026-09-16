package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
)

// JobStatus represents the state of a download job.
type JobStatus string

const (
	StatusQueued     JobStatus = "QUEUED"
	StatusResolving  JobStatus = "RESOLVING"
	StatusDownloading JobStatus = "DOWNLOADING"
	StatusConverting JobStatus = "CONVERTING"
	StatusTagging    JobStatus = "TAGGING"
	StatusReady      JobStatus = "READY"
	StatusError      JobStatus = "ERROR"
	StatusCancelled  JobStatus = "CANCELLED"
)

// DownloadJob represents an active or completed download task.
type DownloadJob struct {
	mu                sync.RWMutex
	ID                string                   `json:"id"`
	Status            JobStatus                `json:"status"`
	Source            string                   `json:"source"`
	URL               string                   `json:"url"`
	Progress          float64                  `json:"progress"`
	CurrentStep       string                   `json:"current_step"`
	TotalTracks       int                      `json:"total_tracks"`
	CompletedTracks   int                      `json:"completed_tracks"`
	Error             *string                  `json:"error,omitempty"`
	TargetAlbumPath   *string                  `json:"target_album_path,omitempty"`
	IsBackfill        bool                     `json:"is_backfill"`
	AlbumTitle        *string                  `json:"album_title,omitempty"`
	ArtistName        *string                  `json:"artist_name,omitempty"`
	CreatedAt         string                   `json:"created_at"`
	UpdatedAt         time.Time                `json:"-"`
	CoverRelativePath string                   `json:"-"`
	AlbumMetadata     resolver.ReleaseMetadata `json:"-"`
	Tracks            []resolver.ResolvedTrack `json:"-"`
	trackProgress     []float64                `json:"-"`
	cancelFunc        func()                   `json:"-"`
	onUpdate          func()                   `json:"-"`
}

// MarshalJSON safely marshals DownloadJob under a read lock to prevent data races.
func (j *DownloadJob) MarshalJSON() ([]byte, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()

	type Alias DownloadJob
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(j),
	})
}

// Snapshot creates an independent thread-safe copy of the job.
func (j *DownloadJob) Snapshot() DownloadJob {
	j.mu.RLock()
	defer j.mu.RUnlock()

	snap := DownloadJob{
		ID:                j.ID,
		Status:            j.Status,
		Source:            j.Source,
		URL:               j.URL,
		Progress:          j.Progress,
		CurrentStep:       j.CurrentStep,
		TotalTracks:       j.TotalTracks,
		CompletedTracks:   j.CompletedTracks,
		TargetAlbumPath:   j.TargetAlbumPath,
		IsBackfill:        j.IsBackfill,
		AlbumTitle:        j.AlbumTitle,
		ArtistName:        j.ArtistName,
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
	if len(j.Tracks) > 0 {
		snap.Tracks = make([]resolver.ResolvedTrack, len(j.Tracks))
		copy(snap.Tracks, j.Tracks)
	}
	return snap
}

func (j *DownloadJob) SetOnUpdate(fn func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.onUpdate = fn
}

func (j *DownloadJob) notifyUpdate() {
	j.mu.RLock()
	fn := j.onUpdate
	j.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

func (j *DownloadJob) UpdateProgress(progress float64, step string) {
	j.mu.Lock()
	j.Progress = progress
	j.CurrentStep = step
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

func (j *DownloadJob) UpdateTrackProgress(trackIdx int, pct float64, step string) {
	j.mu.Lock()
	if trackIdx >= 0 && trackIdx < len(j.trackProgress) {
		j.trackProgress[trackIdx] = pct
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
	j.CurrentStep = step
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()

	j.notifyUpdate()
}

func (j *DownloadJob) SetStatus(status JobStatus) {
	j.mu.Lock()
	j.Status = status
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

func (j *DownloadJob) SetError(errMsg string) {
	j.mu.Lock()
	j.Status = StatusError
	j.Error = &errMsg
	j.CurrentStep = errMsg
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

func (j *DownloadJob) SetCurrentStep(step string) {
	j.mu.Lock()
	j.CurrentStep = step
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

func (j *DownloadJob) SetStepAndStatus(step string, status JobStatus) {
	j.mu.Lock()
	j.CurrentStep = step
	j.Status = status
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

func (j *DownloadJob) SetReady(completedTracks, total int) {
	j.mu.Lock()
	j.Status = StatusReady
	j.Progress = 100.0
	j.CurrentStep = fmt.Sprintf("Готово к проверке тегов (%d/%d скачано)", completedTracks, total)
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

func (j *DownloadJob) IncrementCompleted(total int) {
	j.mu.Lock()
	j.CompletedTracks++
	calcProgress := 10.0 + (float64(j.CompletedTracks)/float64(total))*85.0
	if calcProgress > j.Progress {
		j.Progress = calcProgress
	}
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

func (j *DownloadJob) SetMetadata(meta resolver.ReleaseMetadata, tracks []resolver.ResolvedTrack) {
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

func (j *DownloadJob) SetCoverPath(relCover string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.CoverRelativePath = relCover
	j.UpdatedAt = time.Now().UTC()
}

func (j *DownloadJob) SetCancelFunc(cancel func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cancelFunc = cancel
}

func (j *DownloadJob) Cancel() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cancelFunc != nil {
		j.cancelFunc()
	}
}

func (j *DownloadJob) UpdateTrack(index int, updateFn func(t *resolver.ResolvedTrack)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if index >= 0 && index < len(j.Tracks) {
		updateFn(&j.Tracks[index])
	}
}

// QueueSummaryResponse provides an overview of the download queue.
type QueueSummaryResponse struct {
	ActiveJobs      int     `json:"active_jobs"`
	QueuedJobs      int     `json:"queued_jobs"`
	CompletedJobs   int     `json:"completed_jobs"`
	ErrorJobs       int     `json:"error_jobs"`
	TotalTracks     int     `json:"total_tracks"`
	CompletedTracks int     `json:"completed_tracks"`
	OverallProgress float64 `json:"overall_progress"`
}

// JobManager manages concurrent download jobs with SQLite persistence.
type JobManager struct {
	mu       sync.RWMutex
	jobs     map[string]*DownloadJob
	database *db.DB
}

// NewJobManager creates a new JobManager and restores existing jobs from SQLite.
func NewJobManager(database *db.DB) *JobManager {
	jm := &JobManager{
		jobs:     make(map[string]*DownloadJob),
		database: database,
	}

	if database != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		entities, err := database.GetAllJobs(ctx)
		if err == nil {
			for _, e := range entities {
				st := JobStatus(e.Status)
				curStep := e.CurrentStep
				// If interrupted during active state, transition to error so it can be retried
				if st == StatusQueued || st == StatusResolving || st == StatusDownloading || st == StatusConverting || st == StatusTagging {
					st = StatusError
					curStep = "Прервано перезапуском сервера. Нажмите «Повторить»"
				}
				job := &DownloadJob{
					ID:              e.ID,
					Status:          st,
					Source:          e.Source,
					URL:             e.URL,
					Progress:        e.Progress,
					CurrentStep:     curStep,
					TotalTracks:     e.TotalTracks,
					CompletedTracks: e.CompletedTracks,
					Error:           e.Error,
					TargetAlbumPath: e.TargetAlbumPath,
					IsBackfill:      e.IsBackfill,
					AlbumTitle:      e.AlbumTitle,
					ArtistName:      e.ArtistName,
					CreatedAt:       e.CreatedAt.UTC().Format(time.RFC3339),
					UpdatedAt:       e.UpdatedAt,
				}
				job.SetOnUpdate(func() {
					jm.syncJob(job)
				})
				jm.jobs[job.ID] = job
			}
		}
	}

	return jm
}

func (m *JobManager) syncJob(job *DownloadJob) {
	if m == nil || m.database == nil || job == nil {
		return
	}
	snap := job.Snapshot()
	entity := db.DownloadJobEntity{
		ID:              snap.ID,
		Status:          string(snap.Status),
		Source:          snap.Source,
		URL:             snap.URL,
		Progress:        snap.Progress,
		CurrentStep:     snap.CurrentStep,
		TotalTracks:     snap.TotalTracks,
		CompletedTracks: snap.CompletedTracks,
		Error:           snap.Error,
		TargetAlbumPath: snap.TargetAlbumPath,
		IsBackfill:      snap.IsBackfill,
		AlbumTitle:      snap.AlbumTitle,
		ArtistName:      snap.ArtistName,
		UpdatedAt:       snap.UpdatedAt,
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = m.database.SaveJob(ctx, entity)
	}()
}

// Add adds a new job to the manager and syncs to SQLite.
func (m *JobManager) Add(job *DownloadJob) {
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()

	job.SetOnUpdate(func() {
		m.syncJob(job)
	})
	m.syncJob(job)
}

// Get retrieves a job by its ID.
func (m *JobManager) Get(id string) (*DownloadJob, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	return j, ok
}

// GetAll returns a copy of all jobs.
func (m *JobManager) GetAll() []*DownloadJob {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]*DownloadJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		res = append(res, j)
	}
	return res
}

// ClearCompleted removes READY, ERROR and CANCELLED jobs from RAM and SQLite.
func (m *JobManager) ClearCompleted() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for id, j := range m.jobs {
		j.mu.RLock()
		st := j.Status
		j.mu.RUnlock()
		if st == StatusReady || st == StatusError || st == StatusCancelled {
			delete(m.jobs, id)
			count++
			if m.database != nil {
				delID := id
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					_ = m.database.DeleteJob(ctx, delID)
				}()
			}
		}
	}
	return count
}

// Delete removes a job by its ID, cancelling it if running and deleting from SQLite.
func (m *JobManager) Delete(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if ok {
		j.Cancel()
		delete(m.jobs, id)
		if m.database != nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = m.database.DeleteJob(ctx, id)
			}()
		}
		return true
	}
	return false
}

// GetSummary calculates aggregated queue metrics.
func (m *JobManager) GetSummary() QueueSummaryResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var summary QueueSummaryResponse
	totalProg := 0.0

	for _, j := range m.jobs {
		j.mu.RLock()
		st := j.Status
		tCount := len(j.Tracks)
		cTracks := j.CompletedTracks
		prog := j.Progress
		j.mu.RUnlock()

		switch st {
		case StatusResolving, StatusDownloading, StatusConverting, StatusTagging:
			summary.ActiveJobs++
		case StatusQueued:
			summary.QueuedJobs++
		case StatusReady:
			summary.CompletedJobs++
		case StatusError:
			summary.ErrorJobs++
		}

		if tCount == 0 && st != StatusError {
			tCount = 1
		}
		summary.TotalTracks += tCount
		summary.CompletedTracks += cTracks
		totalProg += prog
	}

	if len(m.jobs) > 0 {
		summary.OverallProgress = totalProg / float64(len(m.jobs))
		if summary.OverallProgress > 100 {
			summary.OverallProgress = 100
		}
	}

	return summary
}
