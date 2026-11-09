package downloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
	"music-manager/internal/testkit"
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
	svc := NewService(context.Background(), jobMgr, nil, tmpSb, musicSb, "", 1)
	defer svc.Close()

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

func TestService_CreateBackfillJob(t *testing.T) {
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
	svc := NewService(context.Background(), jobMgr, nil, tmpSb, musicSb, "", 1)
	defer svc.Close()

	svc.PauseQueue()

	// Create backfill job
	job, err := svc.CreateBackfillJob(
		"Test Album - Cool Artist",
		"Test Album",
		"Cool Artist",
		"302127",
		[]string{"https://www.deezer.com/track/123"},
		[]int{2, 4},
	)
	if err != nil {
		t.Fatalf("unexpected error creating backfill job: %v", err)
	}

	snapshot := job.Snapshot()
	if !snapshot.IsBackfill {
		t.Errorf("expected IsBackfill true, got false")
	}
	if snapshot.TargetAlbumPath == nil || *snapshot.TargetAlbumPath != "Test Album - Cool Artist" {
		t.Errorf("expected TargetAlbumPath 'Test Album - Cool Artist', got %v", snapshot.TargetAlbumPath)
	}
	if snapshot.AlbumTitle == nil || *snapshot.AlbumTitle != "Test Album" {
		t.Errorf("expected AlbumTitle 'Test Album', got %v", snapshot.AlbumTitle)
	}
	if snapshot.ArtistName == nil || *snapshot.ArtistName != "Cool Artist" {
		t.Errorf("expected ArtistName 'Cool Artist', got %v", snapshot.ArtistName)
	}
	job.mu.Lock()
	status := job.Status
	job.mu.Unlock()
	if status != StatusQueued && status != StatusResolving && status != StatusError {
		t.Errorf("unexpected initial status: %s", status)
	}
}

func TestService_CleanerStopsOnContextCancel(t *testing.T) {
	tmpDir := t.TempDir()
	tmpSb, err := sandbox.NewPathSandbox(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	musicSb, err := sandbox.NewPathSandbox(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	svc := NewService(ctx, nil, nil, tmpSb, musicSb, "", 1)

	// Cancel the context and verify Close stops without deadlock or hang
	cancel()
	svc.Close()

	// Create another service with Close() directly
	svc2 := NewService(nil, nil, nil, tmpSb, musicSb, "", 1)
	svc2.Close()
}

func TestService_PauseResumeQueue(t *testing.T) {
	tmpDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(tmpDir)

	svc := NewService(context.Background(), nil, nil, tmpSb, musicSb, "", 1)
	defer svc.Close()

	if svc.IsQueuePaused() {
		t.Errorf("expected queue not paused initially")
	}

	svc.PauseQueue()
	if !svc.IsQueuePaused() {
		t.Errorf("expected queue to be paused after PauseQueue()")
	}

	svc.ResumeQueue()
	if svc.IsQueuePaused() {
		t.Errorf("expected queue not to be paused after ResumeQueue()")
	}
}

func TestService_ClearCompletedJobs(t *testing.T) {
	tmpDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(tmpDir)

	jobMgr := NewJobManager(nil)
	svc := NewService(context.Background(), jobMgr, nil, tmpSb, musicSb, "", 1)
	defer svc.Close()

	job1 := &DownloadJob{ID: "job-ready-1", Status: StatusReady}
	job2 := &DownloadJob{ID: "job-err-1", Status: StatusError}
	job3 := &DownloadJob{ID: "job-active-1", Status: StatusDownloading}
	job4 := &DownloadJob{ID: "job-queued-1", Status: StatusQueued}

	jobMgr.Add(job1)
	jobMgr.Add(job2)
	jobMgr.Add(job3)
	jobMgr.Add(job4)

	cleared := svc.ClearCompletedJobs()
	if cleared != 2 {
		t.Errorf("expected 2 cleared jobs (ready + error), got %d", cleared)
	}

	if _, ok := jobMgr.Get("job-ready-1"); ok {
		t.Errorf("expected job-ready-1 to be deleted")
	}
	if _, ok := jobMgr.Get("job-err-1"); ok {
		t.Errorf("expected job-err-1 to be deleted")
	}
	if _, ok := jobMgr.Get("job-active-1"); !ok {
		t.Errorf("expected job-active-1 to be retained")
	}
	if _, ok := jobMgr.Get("job-queued-1"); !ok {
		t.Errorf("expected job-queued-1 to be retained")
	}
}

func TestService_RetryAllFailedJobs(t *testing.T) {
	tmpDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(tmpDir)

	jobMgr := NewJobManager(nil)
	svc := NewService(context.Background(), jobMgr, nil, tmpSb, musicSb, "", 1)
	defer svc.Close()

	// Pause queue so retried jobs stay queued during test assertions
	svc.PauseQueue()

	errMsg := "some network error"
	job1 := &DownloadJob{ID: "err-job-1", Status: StatusError, URL: "https://www.deezer.com/track/1", Error: &errMsg}
	job2 := &DownloadJob{ID: "err-job-2", Status: StatusError, URL: "https://www.deezer.com/track/2", Error: &errMsg}
	job3 := &DownloadJob{ID: "active-job", Status: StatusDownloading, URL: "https://www.deezer.com/track/3"}

	jobMgr.Add(job1)
	jobMgr.Add(job2)
	jobMgr.Add(job3)

	retriedIDs, err := svc.RetryAllFailedJobs()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(retriedIDs) != 2 {
		t.Errorf("expected 2 retried jobs, got %d (%v)", len(retriedIDs), retriedIDs)
	}

	j1, _ := jobMgr.Get("err-job-1")
	j1.mu.Lock()
	status1 := j1.Status
	err1 := j1.Error
	j1.mu.Unlock()

	if status1 != StatusQueued {
		t.Errorf("expected err-job-1 to be StatusQueued, got %s", status1)
	}
	if err1 != nil {
		t.Errorf("expected err-job-1 error to be nil, got %v", *err1)
	}
}

func TestDomainWhitelist_SoundcloudIO(t *testing.T) {
	tmpDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(tmpDir)

	res := resolver.NewSourceResolver(resolver.NewDeezerClient(testkit.NewMockTransport()), nil, "")
	svc := NewService(context.Background(), NewJobManager(nil), res, tmpSb, musicSb, "", 1)
	defer svc.Close()

	_, err := svc.CreateJob("https://soundcloud.io/artist/track", "")
	if err == nil {
		t.Fatalf("expected validation error for disallowed domain soundcloud.io, got nil")
	}
	if !strings.Contains(err.Error(), "whitelist") && !strings.Contains(err.Error(), "allowed") {
		t.Errorf("expected whitelist rejection error, got: %v", err)
	}
}

func TestStartCleaner_NoLeakAfterClose(t *testing.T) {
	tmpDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(tmpDir)

	before := runtime.NumGoroutine()
	svc := NewService(context.Background(), nil, nil, tmpSb, musicSb, "", 1)
	svc.Close()

	success := false
	for i := 0; i < 20; i++ {
		time.Sleep(20 * time.Millisecond)
		if runtime.NumGoroutine() <= before {
			success = true
			break
		}
	}
	if !success {
		t.Errorf("goroutine leak: before=%d, after=%d", before, runtime.NumGoroutine())
	}
}

func TestSyncJob_Debounce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_debounce.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mgr := NewJobManager(database)
	job := &DownloadJob{
		ID:          "debounce-job",
		Status:      StatusDownloading,
		URL:         "https://www.deezer.com/album/123",
		Progress:    0.0,
		CurrentStep: "Step 0",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:   time.Now().UTC(),
	}
	mgr.Add(job)

	// Fire 5 rapid progress updates (non-terminal state) within 50ms
	for i := 1; i <= 5; i++ {
		job.UpdateProgress(float64(i*10), fmt.Sprintf("Step %d", i))
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for debounce timer (500ms) to settle and sync to SQLite
	time.Sleep(650 * time.Millisecond)

	ctx := context.Background()
	saved, err := database.GetJob(ctx, "debounce-job")
	if err != nil {
		t.Fatalf("failed to get job from DB: %v", err)
	}
	if saved.Progress != 50.0 {
		t.Errorf("expected final debounced progress 50.0, got %f", saved.Progress)
	}
}

func TestJob_FilesInTmpDir(t *testing.T) {
	tmpDir := filepath.Join(t.TempDir(), "tmp_downloads")
	musicDir := filepath.Join(t.TempDir(), "music")

	tmpSb, err := sandbox.NewPathSandbox(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	musicSb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}

	jobID := "job-isolation-test-123"
	rawDir, err := tmpSb.Resolve(filepath.Join(jobID, "raw"), true)
	if err != nil {
		t.Fatalf("unexpected error resolving rawDir: %v", err)
	}
	convertedDir, err := tmpSb.Resolve(filepath.Join(jobID, "converted"), true)
	if err != nil {
		t.Fatalf("unexpected error resolving convertedDir: %v", err)
	}

	// Verify paths are strictly inside tmpSandbox and contain jobID
	if !strings.Contains(rawDir, jobID) || !strings.HasPrefix(rawDir, tmpSb.Root()) {
		t.Errorf("rawDir %s is not inside tmpSandbox or does not contain jobID", rawDir)
	}
	if !strings.Contains(convertedDir, jobID) || !strings.HasPrefix(convertedDir, tmpSb.Root()) {
		t.Errorf("convertedDir %s is not inside tmpSandbox or does not contain jobID", convertedDir)
	}

	// Verify files are completely isolated from musicSandbox
	if strings.HasPrefix(rawDir, musicSb.Root()) || strings.HasPrefix(convertedDir, musicSb.Root()) {
		t.Errorf("temporary job directories leak into musicSandbox!")
	}
}

func TestService_CleanOldTempDirs(t *testing.T) {
	tmpDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(tmpDir)

	svc := NewService(context.Background(), nil, nil, tmpSb, musicSb, "", 1)
	defer svc.Close()

	// 1. Old directory (should be cleaned)
	oldDir := filepath.Join(tmpDir, "old-job-dir")
	_ = os.MkdirAll(oldDir, 0755)
	pastTime := time.Now().Add(-5 * time.Hour)
	_ = os.Chtimes(oldDir, pastTime, pastTime)

	// 2. Recent directory (should be kept)
	recentDir := filepath.Join(tmpDir, "recent-job-dir")
	_ = os.MkdirAll(recentDir, 0755)

	svc.cleanOldTempDirs(2 * time.Hour)

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Errorf("expected old-job-dir to be deleted by cleaner")
	}
	if _, err := os.Stat(recentDir); err != nil {
		t.Errorf("expected recent-job-dir to still exist, got: %v", err)
	}
}

func TestService_DeleteJob(t *testing.T) {
	tmpDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(tmpDir)

	mgr := NewJobManager(nil)
	svc := NewService(context.Background(), mgr, nil, tmpSb, musicSb, "", 1)
	defer svc.Close()

	job := &DownloadJob{
		ID:     "delete-me",
		Status: StatusReady,
		URL:    "https://example.com",
	}
	mgr.Add(job)

	// Create job directory in tmpSandbox
	jobDir, _ := tmpSb.Resolve("delete-me", true)
	_ = os.WriteFile(filepath.Join(jobDir, "file.txt"), []byte("data"), 0644)

	err := svc.DeleteJob("delete-me")
	if err != nil {
		t.Fatalf("unexpected error deleting job: %v", err)
	}

	if _, ok := mgr.Get("delete-me"); ok {
		t.Errorf("expected job to be deleted from manager")
	}
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Errorf("expected jobDir to be deleted from disk")
	}
}

func TestService_CreateBatchJobs(t *testing.T) {
	tmpDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(tmpDir)

	res := resolver.NewSourceResolver(resolver.NewDeezerClient(testkit.NewMockTransport()), nil, "")
	mgr := NewJobManager(nil)
	svc := NewService(context.Background(), mgr, res, tmpSb, musicSb, "", 1)
	defer svc.Close()
	svc.PauseQueue()

	// Error case: contains invalid domain
	_, err := svc.CreateBatchJobs([]string{"https://www.deezer.com/track/1", "https://unsupported.com/track"}, "")
	if err == nil {
		t.Errorf("expected error for unsupported domain in batch, got nil")
	}

	// Success case: valid deezer URLs
	created, err := svc.CreateBatchJobs([]string{"https://www.deezer.com/track/1", "https://www.deezer.com/track/2"}, "")
	if err != nil {
		t.Fatalf("unexpected error creating batch jobs: %v", err)
	}
	if len(created) != 2 {
		t.Errorf("expected 2 created jobs, got %d", len(created))
	}
}

func TestDownloadJob_Methods(t *testing.T) {
	job := &DownloadJob{
		ID:     "method-test",
		Status: StatusQueued,
	}

	cancelled := false
	job.SetCancelFunc(func() {
		cancelled = true
	})
	job.Cancel()
	if !cancelled {
		t.Errorf("expected cancel func to be called")
	}
	job.SetStatus(StatusCancelled)
	if job.Status != StatusCancelled {
		t.Errorf("expected StatusCancelled, got %s", job.Status)
	}

	job.SetStepAndStatus("Downloading audio", StatusDownloading)
	if job.CurrentStep != "Downloading audio" || job.Status != StatusDownloading {
		t.Errorf("SetStepAndStatus failed: status=%s, step=%s", job.Status, job.CurrentStep)
	}

	job.SetCurrentStep("Processing tags")
	if job.CurrentStep != "Processing tags" {
		t.Errorf("SetCurrentStep failed: %s", job.CurrentStep)
	}

	job.SetReady(5, 5)
	if job.Status != StatusReady || job.Progress != 100.0 {
		t.Errorf("SetReady failed: status=%s, progress=%f", job.Status, job.Progress)
	}

	meta := resolver.ReleaseMetadata{
		AlbumTitle:  "Test Release",
		AlbumArtist: "Test Artist",
		Year:        2024,
	}
	tracks := []resolver.ResolvedTrack{{Title: "Track 1"}}
	job.SetMetadata(meta, tracks)
	if job.AlbumTitle == nil || *job.AlbumTitle != "Test Release" {
		t.Errorf("SetMetadata failed: albumTitle=%v", job.AlbumTitle)
	}
	if job.ArtistName == nil || *job.ArtistName != "Test Artist" {
		t.Errorf("SetMetadata failed: artistName=%v", job.ArtistName)
	}
	if job.TotalTracks != 1 {
		t.Errorf("expected 1 total track, got %d", job.TotalTracks)
	}

	job.SetCoverPath("cover.jpg")
	if job.CoverRelativePath != "cover.jpg" {
		t.Errorf("SetCoverPath failed: %s", job.CoverRelativePath)
	}
}

func TestService_Getters(t *testing.T) {
	tmpDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(tmpDir)

	mgr := NewJobManager(nil)
	res := resolver.NewSourceResolver(resolver.NewDeezerClient(testkit.NewMockTransport()), nil, "cookies.txt")
	svc := NewService(context.Background(), mgr, res, tmpSb, musicSb, "cookies.txt", 2)
	defer svc.Close()

	if svc.Jobs() != mgr {
		t.Errorf("Jobs() getter failed")
	}
	if svc.Resolver() != res {
		t.Errorf("Resolver() getter failed")
	}
	if svc.CookiesPath() != "cookies.txt" {
		t.Errorf("CookiesPath() getter failed")
	}
	if svc.TmpSandbox() != tmpSb {
		t.Errorf("TmpSandbox() getter failed")
	}
	if svc.MusicSandbox() != musicSb {
		t.Errorf("MusicSandbox() getter failed")
	}
	if svc.Database() != nil {
		t.Errorf("Database() getter failed")
	}
}

func TestService_WaitIfPaused_ContextCancel(t *testing.T) {
	tmpDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(tmpDir)

	svc := NewService(context.Background(), nil, nil, tmpSb, musicSb, "", 1)
	defer svc.Close()

	svc.PauseQueue()
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.waitIfPaused(ctx)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	svc.pauseCond.Broadcast()

	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("waitIfPaused timed out")
	}

	svc.ResumeQueue()
}
