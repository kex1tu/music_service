package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDB_OpenAndQueries(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "db_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	// 1. Тест создания и сохранения задачи
	job := DownloadJobEntity{
		ID:          "job-123",
		Status:      "QUEUED",
		Source:      "deezer",
		URL:         "https://www.deezer.com/album/123",
		Progress:    0.0,
		CurrentStep: "В очереди",
	}

	if err := database.SaveJob(ctx, job); err != nil {
		t.Fatalf("failed to save job: %v", err)
	}

	retrieved, err := database.GetJob(ctx, "job-123")
	if err != nil {
		t.Fatalf("failed to get job: %v", err)
	}
	if retrieved == nil || retrieved.ID != "job-123" || retrieved.Status != "QUEUED" {
		t.Errorf("unexpected retrieved job: %+v", retrieved)
	}

	// 2. Тест обновления задачи
	job.Status = "DOWNLOADING"
	job.Progress = 45.5
	if err := database.SaveJob(ctx, job); err != nil {
		t.Fatalf("failed to update job: %v", err)
	}

	allJobs, err := database.GetAllJobs(ctx)
	if err != nil {
		t.Fatalf("failed to get all jobs: %v", err)
	}
	if len(allJobs) != 1 || allJobs[0].Progress != 45.5 {
		t.Errorf("unexpected jobs list: %+v", allJobs)
	}
}
