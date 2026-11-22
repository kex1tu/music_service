package downloader

import (
	"sync"

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
	IsPaused        bool    `json:"is_paused"`
}

// JobManager holds jobs for the compatibility service. The database is only
// used for library lookups. Persistent downloads belong to the feature queue.
type JobManager struct {
	mu       sync.RWMutex
	jobs     map[string]*DownloadJob
	database *db.DB
}

func NewJobManager(database *db.DB) *JobManager {
	return &JobManager{jobs: make(map[string]*DownloadJob), database: database}
}

func (m *JobManager) Add(job *DownloadJob) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs[job.ID] = job
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

// ClearCompleted removes READY, ERROR and CANCELLED jobs from memory.
func (m *JobManager) ClearCompleted() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for id, j := range m.jobs {
		j.mu.RLock()
		st := j.Status
		j.mu.RUnlock()
		if st == StatusReady || st == StatusPartialReady || st == StatusError || st == StatusCancelled {
			delete(m.jobs, id)
			count++
		}
	}
	return count
}

// Delete removes a job by its ID, cancelling it if running.
func (m *JobManager) Delete(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if ok {
		j.Cancel()
		delete(m.jobs, id)

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
		case StatusReady, StatusPartialReady:
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
