package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/config"
	"music-manager/internal/db"
	"music-manager/internal/services/radar"
	"music-manager/internal/services/resolver"
)

func setupTestRadarHandler(t *testing.T) (*RadarHandler, *db.DB, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "radar-handler-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to open test db: %v", err)
	}

	cfg := &config.Config{
		RadarScanIntervalHours: 24,
		RadarAutoDownload:      false,
	}

	aggregator := resolver.NewDiscographyAggregator(nil, nil, nil, database)
	radarService := radar.NewService(database, aggregator, nil, cfg)
	handler := NewRadarHandler(radarService, database)

	cleanup := func() {
		database.Close()
		os.RemoveAll(tempDir)
	}

	return handler, database, cleanup
}

func TestRadarHandler_Endpoints(t *testing.T) {
	handler, database, cleanup := setupTestRadarHandler(t)
	defer cleanup()

	ctx := context.Background()

	// 1. POST /artists (Добавление артиста)
	body, _ := json.Marshal(map[string]string{"name": "Daft Punk"})
	req := httptest.NewRequest("POST", "/api/radar/artists", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler.AddTrackedArtist(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("AddTrackedArtist failed with status %d", w.Code)
	}

	// 2. GET /artists
	req = httptest.NewRequest("GET", "/api/radar/artists", nil)
	w = httptest.NewRecorder()
	handler.GetTrackedArtists(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetTrackedArtists failed with status %d", w.Code)
	}
	var artistsResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &artistsResp)
	if artistsResp["total"].(float64) != 1 {
		t.Errorf("expected 1 tracked artist, got %v", artistsResp["total"])
	}

	// 3. POST /artists/{name}/follow
	r := chi.NewRouter()
	r.Post("/api/radar/artists/{name}/follow", handler.FollowArtist)
	followBody, _ := json.Marshal(map[string]bool{"followed": false})
	req = httptest.NewRequest("POST", "/api/radar/artists/Daft%20Punk/follow", bytes.NewReader(followBody))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("FollowArtist failed with status %d", w.Code)
	}

	tracked, err := database.GetTrackedArtistByName(ctx, "Daft Punk")
	if err != nil || tracked.IsFollowed {
		t.Errorf("expected artist not to be followed")
	}

	// 4. Добавляем релиз и проверяем GetFeed и SetReleaseStatus
	rel := &db.RadarReleaseEntity{
		ArtistName:  "Daft Punk",
		Title:       "Discovery",
		ReleaseType: "album",
		Year:        2001,
		SourceURL:   "https://example.com/discovery",
		Status:      "new",
		IsRadar:     true,
	}
	if err := database.UpsertRadarRelease(ctx, rel); err != nil {
		t.Fatalf("failed to insert radar release: %v", err)
	}

	req = httptest.NewRequest("GET", "/api/radar/feed", nil)
	w = httptest.NewRecorder()
	handler.GetFeed(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetFeed failed with status %d", w.Code)
	}
	var feedResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &feedResp)
	if feedResp["total"].(float64) < 1 {
		t.Errorf("expected at least 1 feed item, got %v", feedResp["total"])
	}

	// 5. GET /progress and POST /cancel
	req = httptest.NewRequest("GET", "/api/radar/progress", nil)
	w = httptest.NewRecorder()
	handler.GetScanProgress(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetScanProgress failed: %d", w.Code)
	}

	req = httptest.NewRequest("POST", "/api/radar/cancel", nil)
	w = httptest.NewRecorder()
	handler.CancelScan(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("CancelScan failed: %d", w.Code)
	}
}
