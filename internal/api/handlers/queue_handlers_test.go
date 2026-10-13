package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/services/downloader"
)

func TestQueueHandlers_PauseResumeSummary(t *testing.T) {
	tempDir := t.TempDir()
	tmpSb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	musicSb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	jobMgr := downloader.NewJobManager(nil)
	svc := downloader.NewService(context.Background(), jobMgr, nil, tmpSb, musicSb, "", 2)
	defer svc.Close()
	handler := NewDownloadHandler(svc, nil)

	// 1. Initial Summary (not paused)
	req := httptest.NewRequest(http.MethodGet, "/downloads/summary", nil)
	rec := httptest.NewRecorder()
	handler.GetQueueSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	var sum downloader.QueueSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &sum); err != nil {
		t.Fatalf("failed to decode summary: %v", err)
	}
	if sum.IsPaused {
		t.Errorf("expected is_paused false initially, got true")
	}

	// 2. Pause queue
	reqPause := httptest.NewRequest(http.MethodPost, "/downloads/pause", nil)
	recPause := httptest.NewRecorder()
	handler.PauseQueue(recPause, reqPause)

	if recPause.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from pause, got %d", recPause.Code)
	}
	var pauseResp map[string]any
	if err := json.Unmarshal(recPause.Body.Bytes(), &pauseResp); err != nil {
		t.Fatalf("failed to decode pause response: %v", err)
	}
	if pauseResp["is_paused"] != true {
		t.Errorf("expected is_paused true, got %v", pauseResp["is_paused"])
	}

	// 3. Summary while paused
	rec2 := httptest.NewRecorder()
	handler.GetQueueSummary(rec2, req)
	var sum2 downloader.QueueSummaryResponse
	_ = json.Unmarshal(rec2.Body.Bytes(), &sum2)
	if !sum2.IsPaused {
		t.Errorf("expected is_paused true in summary, got false")
	}

	// 4. Resume queue
	reqResume := httptest.NewRequest(http.MethodPost, "/downloads/resume", nil)
	recResume := httptest.NewRecorder()
	handler.ResumeQueue(recResume, reqResume)

	if recResume.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from resume, got %d", recResume.Code)
	}
	var resumeResp map[string]any
	if err := json.Unmarshal(recResume.Body.Bytes(), &resumeResp); err != nil {
		t.Fatalf("failed to decode resume response: %v", err)
	}
	if resumeResp["is_paused"] != false {
		t.Errorf("expected is_paused false, got %v", resumeResp["is_paused"])
	}
}

func TestQueueHandlers_ClearAndRetry(t *testing.T) {
	tempDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tempDir)
	musicSb, _ := sandbox.NewPathSandbox(tempDir)

	jobMgr := downloader.NewJobManager(nil)
	svc := downloader.NewService(context.Background(), jobMgr, nil, tmpSb, musicSb, "", 2)
	defer svc.Close()
	handler := NewDownloadHandler(svc, nil)

	// Pause queue so retried jobs stay queued
	svc.PauseQueue()

	// Add 1 completed and 1 failed job
	errMsg := "failed to fetch"
	jobMgr.Add(&downloader.DownloadJob{ID: "j-done", Status: downloader.StatusReady})
	jobMgr.Add(&downloader.DownloadJob{ID: "j-err", Status: downloader.StatusError, URL: "https://www.deezer.com/track/1", Error: &errMsg})
	jobMgr.Add(&downloader.DownloadJob{ID: "j-wait", Status: downloader.StatusQueued})

	// Test RetryAllFailed
	reqRetry := httptest.NewRequest(http.MethodPost, "/downloads/retry-failed", nil)
	recRetry := httptest.NewRecorder()
	handler.RetryAllFailed(recRetry, reqRetry)

	if recRetry.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from retry-failed, got %d", recRetry.Code)
	}
	var retryResp map[string]any
	if err := json.Unmarshal(recRetry.Body.Bytes(), &retryResp); err != nil {
		t.Fatalf("failed to decode retry response: %v", err)
	}
	if retryResp["count"] != float64(1) {
		t.Errorf("expected 1 retried job, got %v", retryResp["count"])
	}

	// Make j-done ready again and clear completed
	reqClear := httptest.NewRequest(http.MethodPost, "/downloads/clear-completed", nil)
	recClear := httptest.NewRecorder()
	handler.ClearCompleted(recClear, reqClear)

	if recClear.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from clear-completed, got %d", recClear.Code)
	}
	var clearResp map[string]any
	if err := json.Unmarshal(recClear.Body.Bytes(), &clearResp); err != nil {
		t.Fatalf("failed to decode clear response: %v", err)
	}
	if clearResp["cleared"] != float64(1) {
		t.Errorf("expected 1 cleared job, got %v", clearResp["cleared"])
	}
}
