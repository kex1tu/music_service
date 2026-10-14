package radar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"music-manager/internal/config"
	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
)

func setupTestDB(t *testing.T) (*db.DB, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "radar-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to open test db: %v", err)
	}

	cleanup := func() {
		database.Close()
		os.RemoveAll(tempDir)
	}

	return database, cleanup
}

func TestRadarService(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// 1. Создаем мок Deezer API
	deezerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/search/artist" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": 42, "name": "Radiohead"},
				},
			})
			return
		}
		if r.URL.Path == "/artist/42/albums" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"id":           1001,
						"title":        "OK Computer",
						"release_date": "1997-05-21",
						"record_type":  "album",
						"cover_xl":     "https://example.com/ok_computer.jpg",
						"nb_tracks":    12,
					},
					{
						"id":           1002,
						"title":        "A Moon Shaped Pool",
						"release_date": time.Now().Format("2006-01-02"), // Свежий релиз
						"record_type":  "album",
						"cover_xl":     "https://example.com/amsp.jpg",
						"nb_tracks":    11,
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer deezerServer.Close()

	deezerClient := resolver.NewDeezerClient()
	deezerClient.SetBaseURL(deezerServer.URL)

	aggregator := resolver.NewDiscographyAggregator(deezerClient, nil, nil, database)
	cfg := &config.Config{
		RadarScanIntervalHours: 24,
		RadarAutoDownload:      false,
	}

	service := NewService(database, aggregator, nil, cfg)

	// Добавляем артиста
	err := database.UpsertTrackedArtist(ctx, "Radiohead")
	if err != nil {
		t.Fatalf("UpsertTrackedArtist failed: %v", err)
	}

	// 2. Тестируем ScanArtist
	res, err := service.ScanArtist(ctx, "Radiohead")
	if err != nil {
		t.Fatalf("ScanArtist failed: %v", err)
	}

	if res.ArtistName != "Radiohead" {
		t.Errorf("expected artist name Radiohead, got %q", res.ArtistName)
	}
	if res.TotalDiscovered != 2 {
		t.Errorf("expected 2 discovered albums, got %d", res.TotalDiscovered)
	}

	// Проверяем сохранение в radar_releases
	releases, err := database.GetArtistDiscography(ctx, "Radiohead")
	if err != nil {
		t.Fatalf("GetArtistDiscography failed: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("expected 2 releases in db, got %d", len(releases))
	}

	// Проверяем ленту Радара
	radarFeed, total, err := database.GetRadarFeed(ctx, "all", "", 10, 0)
	if err != nil {
		t.Fatalf("GetRadarFeed failed: %v", err)
	}
	if total < 1 || len(radarFeed) < 1 {
		t.Fatalf("expected at least 1 radar release, got %d", total)
	}

	// 3. Тестируем Follow/AutoDownload/CustomLinks
	err = service.FollowArtist(ctx, "Radiohead", true)
	if err != nil {
		t.Fatalf("FollowArtist failed: %v", err)
	}

	err = service.SetAutoDownload(ctx, "Radiohead", true)
	if err != nil {
		t.Fatalf("SetAutoDownload failed: %v", err)
	}

	err = service.UpdateArtistCustomLinks(ctx, "Radiohead", "RH", "42", "mb-123", "")
	if err != nil {
		t.Fatalf("UpdateArtistCustomLinks failed: %v", err)
	}

	tracked, err := database.GetTrackedArtistByName(ctx, "Radiohead")
	if err != nil {
		t.Fatalf("GetTrackedArtistByName failed: %v", err)
	}
	if !tracked.IsFollowed {
		t.Error("expected artist to be followed")
	}
	if !tracked.AutoDownload {
		t.Error("expected auto_download to be true")
	}
	if tracked.CustomSearchAlias != "RH" {
		t.Errorf("expected alias 'RH', got %q", tracked.CustomSearchAlias)
	}
	if tracked.DeezerID != "42" {
		t.Errorf("expected DeezerID '42', got %q", tracked.DeezerID)
	}

	// 4. Тестируем ScanAll и GetProgress
	err = service.ScanAll(ctx, "manual_test")
	if err != nil {
		t.Fatalf("ScanAll failed: %v", err)
	}

	prog := service.GetProgress()
	if !prog.IsRunning {
		t.Log("progress may already have finished or running")
	}

	service.CancelScan()
	progAfter := service.GetProgress()
	if progAfter.IsRunning {
		t.Error("expected progress to not be running after CancelScan")
	}
}
