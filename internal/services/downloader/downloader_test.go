package downloader

import (
	"testing"

	"music-manager/internal/core/sandbox"
)

func TestJobManager(t *testing.T) {
	mgr := NewJobManager(nil)

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

func TestService_RetryJob(t *testing.T) {
	tmpDir := t.TempDir()
	tmpSb, err := sandbox.NewPathSandbox(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	musicSb, err := sandbox.NewPathSandbox(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	jobMgr := NewJobManager(nil)
	svc := NewService(jobMgr, nil, tmpSb, musicSb, "", 1)

	// 1. Non-existent job
	_, err = svc.RetryJob("non-existent")
	if err == nil {
		t.Errorf("expected error for non-existent job, got nil")
	}

	// 2. Active job (cannot retry while active)
	activeJob := &DownloadJob{
		ID:     "job-active",
		Status: StatusDownloading,
		URL:    "https://example.com/audio",
	}
	jobMgr.Add(activeJob)
	_, err = svc.RetryJob(activeJob.ID)
	if err == nil {
		t.Errorf("expected error when retrying active job, got nil")
	}

	// 3. Failed job
	failedJob := &DownloadJob{
		ID:     "job-failed",
		Status: StatusError,
		URL:    "https://example.com/audio",
	}
	errMsg := "download failed"
	failedJob.Error = &errMsg
	jobMgr.Add(failedJob)

	// Retry should reset status to queued (even if resolver is nil and pipeline ends)
	retried, err := svc.RetryJob(failedJob.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if retried.ID != failedJob.ID {
		t.Errorf("expected job ID %s, got %s", failedJob.ID, retried.ID)
	}
}
