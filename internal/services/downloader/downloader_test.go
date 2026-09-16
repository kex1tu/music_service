package downloader

import (
	"testing"
)

func TestJobManager(t *testing.T) {
	mgr := NewJobManager()

	job := &DownloadJob{
		ID:       "test-job-1",
		Status:   StatusQueued,
		Source:   "deezer",
		URL:      "https://www.deezer.com/album/302127",
		Progress: 0.0,
	}

	mgr.Add(job)

	retrieved, ok := mgr.Get("test-job-1")
	if !ok || retrieved.ID != "test-job-1" {
		t.Fatalf("failed to retrieve job")
	}

	summary := mgr.GetSummary()
	if summary.QueuedJobs != 1 {
		t.Errorf("expected 1 queued job, got %d", summary.QueuedJobs)
	}

	job.Status = StatusReady
	job.Progress = 100.0

	summary2 := mgr.GetSummary()
	if summary2.CompletedJobs != 1 {
		t.Errorf("expected 1 completed job, got %d", summary2.CompletedJobs)
	}

	cleared := mgr.ClearCompleted()
	if cleared != 1 {
		t.Errorf("expected 1 cleared job, got %d", cleared)
	}

	if len(mgr.GetAll()) != 0 {
		t.Errorf("expected 0 jobs after clearing, got %d", len(mgr.GetAll()))
	}
}
