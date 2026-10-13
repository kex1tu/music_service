package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/services/downloader"
	"music-manager/internal/services/resolver"
)

func TestDownloadHandler_ListDownloads_DeterministicSort(t *testing.T) {
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

	sameTimestamp := time.Now().UTC().Format(time.RFC3339)

	// Add multiple jobs with the EXACT same timestamp
	jobIDs := []string{"job-d", "job-a", "job-c", "job-b", "job-e"}
	for _, id := range jobIDs {
		job := &downloader.DownloadJob{
			ID:        id,
			Status:    downloader.StatusQueued,
			CreatedAt: sameTimestamp,
		}
		jobMgr.Add(job)
	}

	// Repeat ListDownloads multiple times: order must be 100% identical every time
	var firstOrder []string

	for iteration := 0; iteration < 20; iteration++ {
		req := httptest.NewRequest(http.MethodGet, "/downloads", nil)
		rec := httptest.NewRecorder()

		handler.ListDownloads(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}

		var returnedJobs []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &returnedJobs); err != nil {
			t.Fatalf("failed to decode json: %v", err)
		}

		if len(returnedJobs) != len(jobIDs) {
			t.Fatalf("expected %d jobs, got %d", len(jobIDs), len(returnedJobs))
		}

		var currentOrder []string
		for _, j := range returnedJobs {
			currentOrder = append(currentOrder, j.ID)
		}

		if iteration == 0 {
			firstOrder = currentOrder
			// Because all CreatedAt are equal, tie-breaker sorts ID ascending: job-a, job-b, job-c, job-d, job-e
			expectedOrder := []string{"job-a", "job-b", "job-c", "job-d", "job-e"}
			for i, exp := range expectedOrder {
				if firstOrder[i] != exp {
					t.Fatalf("expected index %d to be %s, got %s", i, exp, firstOrder[i])
				}
			}
		} else {
			for i, id := range currentOrder {
				if id != firstOrder[i] {
					t.Fatalf("iteration %d: sorting shuffled at index %d (expected %s, got %s)", iteration, i, firstOrder[i], id)
				}
			}
		}
	}
}

func TestDownloadHandler_ListDownloads_StatusPriority(t *testing.T) {
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

	// Add jobs with different statuses
	jobs := []*downloader.DownloadJob{
		{ID: "job-ready", Status: downloader.StatusReady, CreatedAt: "2026-09-16T12:00:00Z"},
		{ID: "job-queued", Status: downloader.StatusQueued, CreatedAt: "2026-09-16T12:00:00Z"},
		{ID: "job-downloading", Status: downloader.StatusDownloading, CreatedAt: "2026-09-16T12:00:00Z"},
		{ID: "job-converting", Status: downloader.StatusConverting, CreatedAt: "2026-09-16T12:00:00Z"},
		{ID: "job-error", Status: downloader.StatusError, CreatedAt: "2026-09-16T12:00:00Z"},
	}
	for _, j := range jobs {
		jobMgr.Add(j)
	}

	req := httptest.NewRequest(http.MethodGet, "/downloads", nil)
	rec := httptest.NewRecorder()

	handler.ListDownloads(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var returnedJobs []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &returnedJobs); err != nil {
		t.Fatalf("failed to decode json: %v", err)
	}

	if len(returnedJobs) != len(jobs) {
		t.Fatalf("expected %d jobs, got %d", len(jobs), len(returnedJobs))
	}

	// 1. Queued (Ожидают)
	if returnedJobs[0].ID != "job-queued" {
		t.Errorf("expected 1st job to be job-queued, got %s (%s)", returnedJobs[0].ID, returnedJobs[0].Status)
	}
	// 2 & 3. Downloading / Converting (Скачиваются)
	if returnedJobs[1].Status != "DOWNLOADING" && returnedJobs[1].Status != "CONVERTING" {
		t.Errorf("expected 2nd job to be downloading/converting, got %s", returnedJobs[1].Status)
	}
	if returnedJobs[2].Status != "DOWNLOADING" && returnedJobs[2].Status != "CONVERTING" {
		t.Errorf("expected 3rd job to be downloading/converting, got %s", returnedJobs[2].Status)
	}
	// 4. Ready (Скачаны)
	if returnedJobs[3].ID != "job-ready" {
		t.Errorf("expected 4th job to be job-ready, got %s (%s)", returnedJobs[3].ID, returnedJobs[3].Status)
	}
	// 5. Error (Ошибки)
	if returnedJobs[4].ID != "job-error" {
		t.Errorf("expected 5th job to be job-error, got %s (%s)", returnedJobs[4].ID, returnedJobs[4].Status)
	}
}

func TestDownloadHandler_SwitchTrackVersion(t *testing.T) {
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

	// 1. Missing target_url
	req := httptest.NewRequest(http.MethodPost, "/downloads/tracks/tr-1/switch-version", strings.NewReader(`{"target_url":""}`))
	rec := httptest.NewRecorder()
	handler.SwitchTrackVersion(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for empty target_url, got %d", rec.Code)
	}

	// 2. Track not found
	req = httptest.NewRequest(http.MethodPost, "/downloads/tracks/nonexistent/switch-version", strings.NewReader(`{"target_url":"https://soundcloud.com/artist/track"}`))
	// Set chi url param
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("trackId", "nonexistent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec = httptest.NewRecorder()
	handler.SwitchTrackVersion(rec, req)
	if rec.Code != http.StatusInternalServerError && rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 or 500 when track is not found, got %d", rec.Code)
	}

	// 3. Untrusted/Malicious target_url (SSRF / Cloud Metadata)
	req = httptest.NewRequest(http.MethodPost, "/downloads/tracks/tr-1/switch-version", strings.NewReader(`{"target_url":"http://169.254.169.254/latest/meta-data"}`))
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("trackId", "tr-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec = httptest.NewRecorder()
	handler.SwitchTrackVersion(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for untrusted SSRF target_url, got %d", rec.Code)
	}

	// 4. CLI Argument injection
	req = httptest.NewRequest(http.MethodPost, "/downloads/tracks/tr-1/switch-version", strings.NewReader(`{"target_url":"--exec=cat"}`))
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("trackId", "tr-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec = httptest.NewRecorder()
	handler.SwitchTrackVersion(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for flag injection target_url, got %d", rec.Code)
	}
}


func TestDownloadHandler_GetDownloadTracks_SingleTrackFallback(t *testing.T) {
	tempDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tempDir)
	musicSb, _ := sandbox.NewPathSandbox(tempDir)

	jobMgr := downloader.NewJobManager(nil)
	svc := downloader.NewService(context.Background(), jobMgr, nil, tmpSb, musicSb, "", 2)
	defer svc.Close()
	handler := NewDownloadHandler(svc, nil)

	genre := "Rock"
	year := 2024
	job := &downloader.DownloadJob{
		ID:     "job-single-1",
		Status: downloader.StatusReady,
		Tracks: []resolver.ResolvedTrack{
			{
				TrackID:     "tr-1",
				Title:       "Awesome Song",
				Artist:      "Solo Artist feat. Buddy",
				Album:       "",
				AlbumArtist: "",
				Genre:       &genre,
				Year:        &year,
				Status:      "ready",
			},
		},
	}
	jobMgr.Add(job)

	req := httptest.NewRequest(http.MethodGet, "/downloads/job-single-1/tracks", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "job-single-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	handler.GetDownloadTracks(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var res struct {
		AlbumArtist string `json:"album_artist"`
		Album       string `json:"album"`
		Year        *int   `json:"year"`
		Genre       string `json:"genre"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if res.AlbumArtist != "Solo Artist feat. Buddy" {
		t.Errorf("expected album_artist 'Solo Artist feat. Buddy', got %q", res.AlbumArtist)
	}
	if res.Album != "Awesome Song" {
		t.Errorf("expected album 'Awesome Song', got %q", res.Album)
	}
	if res.Year == nil || *res.Year != 2024 {
		t.Errorf("expected year 2024, got %v", res.Year)
	}
	if res.Genre != "Rock" {
		t.Errorf("expected genre 'Rock', got %q", res.Genre)
	}
}


