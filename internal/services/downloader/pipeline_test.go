package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/services/resolver"
)

func TestProcessTracksPipeline_ZeroTracks(t *testing.T) {
	tmpDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(tmpDir)

	svc := NewService(context.Background(), NewJobManager(nil), nil, tmpSb, musicSb, "", 1)
	defer svc.Close()

	job := &DownloadJob{
		ID:     "zero-tracks-job",
		Status: StatusResolving,
	}

	rawDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "raw"), true)
	convertedDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "converted"), true)

	svc.processTracksPipeline(context.Background(), job, nil, []resolver.ResolvedTrack{}, rawDir, convertedDir)

	if job.Status != StatusError {
		t.Errorf("expected StatusError for zero tracks, got %s", job.Status)
	}
	if job.Error == nil || *job.Error != "Релиз не содержит доступных аудиотреков" {
		t.Errorf("unexpected error message: %v", job.Error)
	}
}

func TestProcessTracksPipeline_SingleTrackNormalization(t *testing.T) {
	tmpDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(tmpDir)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("mock-album-cover-bytes"))
	}))
	defer mockServer.Close()

	svc := NewService(context.Background(), NewJobManager(nil), nil, tmpSb, musicSb, "", 1)
	svc.client = mockServer.Client()
	defer svc.Close()

	job := &DownloadJob{
		ID:     "single-norm-job",
		Status: StatusResolving,
	}

	rawDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "raw"), true)
	convertedDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "converted"), true)
	_ = os.MkdirAll(rawDir, 0755)
	_ = os.MkdirAll(convertedDir, 0755)

	genre := "Rock"
	year := 2023
	track := resolver.ResolvedTrack{
		TrackID:  "t1",
		Title:    "Solo Hit",
		Artist:   "Solo Artist",
		Album:    "single",
		Genre:    &genre,
		Year:     &year,
		Duration: 180,
	}

	meta := &resolver.ReleaseMetadata{
		AlbumTitle: "single",
		CoverURL:   mockServer.URL + "/cover.jpg",
	}

	// Use active context so cover download succeeds; track will fail quickly since AudioSourceURL is empty
	ctx := context.Background()

	svc.processTracksPipeline(ctx, job, meta, []resolver.ResolvedTrack{track}, rawDir, convertedDir)

	// Check single normalization
	if meta.AlbumArtist != "Solo Artist" {
		t.Errorf("expected AlbumArtist 'Solo Artist', got %q", meta.AlbumArtist)
	}
	if meta.AlbumTitle != "Solo Hit" {
		t.Errorf("expected AlbumTitle 'Solo Hit', got %q", meta.AlbumTitle)
	}
	if meta.Genre != "Rock" {
		t.Errorf("expected Genre 'Rock', got %q", meta.Genre)
	}
	if meta.Year != 2023 {
		t.Errorf("expected Year 2023, got %d", meta.Year)
	}

	// Check cover downloaded
	rawCover := filepath.Join(rawDir, "cover.jpg")
	if _, err := os.Stat(rawCover); os.IsNotExist(err) {
		t.Errorf("expected raw cover to be saved")
	}
	if job.CoverRelativePath == "" {
		t.Errorf("expected CoverRelativePath to be set on job")
	}
}
