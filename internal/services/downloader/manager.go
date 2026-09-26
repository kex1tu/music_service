package downloader

import (
	"context"
	"sync"
	"time"

	"music-manager/internal/db"
)

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
	mu         sync.RWMutex
	jobs       map[string]*DownloadJob
	database   *db.DB
	syncTimers map[string]*time.Timer
}

// NewJobManager creates a new JobManager and restores existing jobs from SQLite.
func NewJobManager(database *db.DB) *JobManager {
	jm := &JobManager{
		jobs:       make(map[string]*DownloadJob),
		database:   database,
		syncTimers: make(map[string]*time.Timer),
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
					CreatedAt:       e.CreatedAt.UTC().Format(time.RFC3339Nano),
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

	saveFn := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = m.database.SaveJob(ctx, entity)
	}

	// For terminal states, sync immediately to avoid losing final status
	if snap.Status == StatusReady || snap.Status == StatusError || snap.Status == StatusCancelled {
		m.mu.Lock()
		if m.syncTimers != nil {
			if prev, ok := m.syncTimers[snap.ID]; ok {
				prev.Stop()
				delete(m.syncTimers, snap.ID)
			}
		}
		m.mu.Unlock()
		go saveFn()
		return
	}

	m.mu.Lock()
	if m.syncTimers == nil {
		m.syncTimers = make(map[string]*time.Timer)
	}
	if prev, ok := m.syncTimers[snap.ID]; ok {
		prev.Stop()
	}
	m.syncTimers[snap.ID] = time.AfterFunc(500*time.Millisecond, func() {
		m.mu.Lock()
		delete(m.syncTimers, snap.ID)
		m.mu.Unlock()
		saveFn()
	})
	m.mu.Unlock()
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

// Database returns the associated SQLite database or nil.
func (m *JobManager) Database() *db.DB {
	if m == nil {
		return nil
	}
	return m.database
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
			if timer, ok := m.syncTimers[id]; ok {
				timer.Stop()
				delete(m.syncTimers, id)
			}
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
		if timer, ok := m.syncTimers[id]; ok {
			timer.Stop()
			delete(m.syncTimers, id)
		}
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
	weightedProg := 0.0
	weightedTotal := 0.0

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

		weight := float64(tCount)
		if weight <= 0 {
			weight = 1.0
		}
		weightedProg += prog * weight
		weightedTotal += weight
	}

	if weightedTotal > 0 {
		summary.OverallProgress = weightedProg / weightedTotal
		if summary.OverallProgress > 100 {
			summary.OverallProgress = 100
		}
	}

	return summary
}
