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
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/services/downloader"
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

func TestRadar_GetRadarFeed_ExcludesDownloadedAndInLibrary(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// 1. Release: status = 'new', is_radar = true (Should appear in feed)
	relNew := &db.RadarReleaseEntity{
		ArtistName:  "Daft Punk",
		Title:       "New Single",
		ReleaseType: "single",
		ReleaseDate: time.Now().Format("2006-01-02"),
		Year:        time.Now().Year(),
		SourceURL:   "https://example.com/new",
		Status:      "new",
		IsRadar:     true,
	}
	if err := database.UpsertRadarRelease(ctx, relNew); err != nil {
		t.Fatalf("UpsertRadarRelease failed: %v", err)
	}

	// 2. Release: status = 'downloaded', is_radar = false (Should NOT appear in feed)
	relDownloaded := &db.RadarReleaseEntity{
		ArtistName:  "Daft Punk",
		Title:       "Downloaded Single",
		ReleaseType: "single",
		ReleaseDate: time.Now().Format("2006-01-02"),
		Year:        time.Now().Year(),
		SourceURL:   "https://example.com/downloaded",
		Status:      "downloaded",
		IsRadar:     false,
	}
	if err := database.UpsertRadarRelease(ctx, relDownloaded); err != nil {
		t.Fatalf("UpsertRadarRelease failed: %v", err)
	}

	// 3. Release: status = 'in_library', is_radar = false (Should NOT appear in feed)
	relLibrary := &db.RadarReleaseEntity{
		ArtistName:  "Daft Punk",
		Title:       "In Library Album",
		ReleaseType: "album",
		ReleaseDate: time.Now().Format("2006-01-02"),
		Year:        time.Now().Year(),
		SourceURL:   "https://example.com/in_library",
		Status:      "in_library",
		IsRadar:     false,
	}
	if err := database.UpsertRadarRelease(ctx, relLibrary); err != nil {
		t.Fatalf("UpsertRadarRelease failed: %v", err)
	}

	feed, total, err := database.GetRadarFeed(ctx, "all", "", 10, 0)
	if err != nil {
		t.Fatalf("GetRadarFeed failed: %v", err)
	}

	if total != 1 || len(feed) != 1 {
		t.Fatalf("expected exactly 1 item in feed, got total=%d, len=%d", total, len(feed))
	}
	if feed[0].Title != "New Single" {
		t.Errorf("expected 'New Single' in feed, got %q", feed[0].Title)
	}
}

func TestRadar_MultiArtist_Deduplication(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Release 1 from Artist One
	rel1 := &db.RadarReleaseEntity{
		ArtistName:  "Artist One",
		Title:       "Collab Track",
		ReleaseType: "single",
		ReleaseDate: time.Now().Format("2006-01-02"),
		Year:        time.Now().Year(),
		DeezerID:    "999888",
		SourceURL:   "https://example.com/collab",
		Status:      "new",
		IsRadar:     true,
	}
	if err := database.UpsertRadarRelease(ctx, rel1); err != nil {
		t.Fatalf("UpsertRadarRelease rel1 failed: %v", err)
	}

	// Release 2 for same album with full contributors
	rel2 := &db.RadarReleaseEntity{
		ArtistName:  "Artist One, Artist Two",
		Title:       "Collab Track",
		ReleaseType: "single",
		ReleaseDate: time.Now().Format("2006-01-02"),
		Year:        time.Now().Year(),
		DeezerID:    "999888",
		SourceURL:   "https://example.com/collab",
		Status:      "new",
		IsRadar:     true,
	}
	if err := database.UpsertRadarRelease(ctx, rel2); err != nil {
		t.Fatalf("UpsertRadarRelease rel2 failed: %v", err)
	}

	feed, total, err := database.GetRadarFeed(ctx, "all", "", 10, 0)
	if err != nil {
		t.Fatalf("GetRadarFeed failed: %v", err)
	}

	if total != 1 || len(feed) != 1 {
		t.Fatalf("expected exactly 1 deduplicated release in feed, got %d", total)
	}
	if feed[0].ArtistName != "Artist One, Artist Two" {
		t.Errorf("expected merged artist 'Artist One, Artist Two', got %q", feed[0].ArtistName)
	}
}

func TestRadar_Reconcile_RemovesExistingReleases(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// 1. Создаем существующий трек в библиотеке
	_, err := database.Conn().ExecContext(ctx, `
		INSERT INTO artists (id, name, clean_name) VALUES (1, 'Skrillex', 'skrillex');
		INSERT INTO albums (id, artist_id, title, clean_title, folder_path, is_complete)
		VALUES (1, 1, 'Bangarang', 'bangarang', 'Skrillex/Bangarang', 1);
		INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, mtime)
		VALUES (1, 1, 1, 'Bangarang', 'bangarang', 'Skrillex/Bangarang/01. Bangarang.mp3', 1000, 100.0);
	`)
	if err != nil {
		t.Fatalf("failed to insert test tracks: %v", err)
	}

	// 2. Вставляем релиз Радара с тем же названием
	rel := &db.RadarReleaseEntity{
		ArtistName:  "Skrillex",
		Title:       "Bangarang",
		ReleaseType: "album",
		ReleaseDate: time.Now().Format("2006-01-02"),
		Year:        time.Now().Year(),
		SourceURL:   "https://example.com/bangarang",
		Status:      "new",
		IsRadar:     true,
	}
	if err := database.UpsertRadarRelease(ctx, rel); err != nil {
		t.Fatalf("UpsertRadarRelease failed: %v", err)
	}

	// До reconcile релиз виден в ленте
	feedBefore, totalBefore, _ := database.GetRadarFeed(ctx, "all", "", 10, 0)
	if totalBefore != 1 || len(feedBefore) != 1 {
		t.Fatalf("expected 1 release before reconcile, got %d", totalBefore)
	}

	// Запускаем Reconcile
	hidden, err := database.ReconcileAllRadarReleases(ctx)
	if err != nil {
		t.Fatalf("ReconcileAllRadarReleases failed: %v", err)
	}
	if hidden != 1 {
		t.Errorf("expected 1 reconciled release hidden from radar, got %d", hidden)
	}

	// После reconcile релиз скрыт из ленты
	feedAfter, totalAfter, _ := database.GetRadarFeed(ctx, "all", "", 10, 0)
	if totalAfter != 0 || len(feedAfter) != 0 {
		t.Errorf("expected 0 releases in feed after reconcile, got %d", totalAfter)
	}
}

func TestRadar_ValidationAndConcurrency(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	aggregator := resolver.NewDiscographyAggregator(nil, nil, nil, database)
	service := NewService(database, aggregator, nil, nil)

	// 1. Empty artist name error
	if _, err := service.ScanArtist(ctx, ""); err == nil {
		t.Error("expected error for empty artist name, got nil")
	}

	// 2. Start lifecycle test
	startCtx, cancel := context.WithCancel(context.Background())
	service.Start(startCtx)
	cancel() // Cancel background cron loop
}

func TestRadar_ScanArtist_AutoDownload(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	deezerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/search/artist" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": 77, "name": "AutoArtist"},
				},
			})
			return
		}
		if r.URL.Path == "/artist/77/albums" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"id":           2001,
						"title":        "Brand New Release",
						"release_date": time.Now().Format("2006-01-02"),
						"record_type":  "single",
						"cover_xl":     "https://example.com/cover.jpg",
						"nb_tracks":    1,
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer deezerServer.Close()

	deezerClient := resolver.NewDeezerClient()
	deezerClient.SetBaseURL(deezerServer.URL)

	aggregator := resolver.NewDiscographyAggregator(deezerClient, nil, nil, database)
	cfg := &config.Config{
		RadarScanIntervalHours: 24,
		RadarAutoDownload:      true,
	}

	tmpDir := t.TempDir()
	tmpSb, err := sandbox.NewPathSandbox(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	musicSb, err := sandbox.NewPathSandbox(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	jobMgr := downloader.NewJobManager(database)
	dlService := downloader.NewService(ctx, jobMgr, nil, tmpSb, musicSb, "", 2)

	service := NewService(database, aggregator, dlService, cfg)

	if err := database.UpsertTrackedArtist(ctx, "AutoArtist"); err != nil {
		t.Fatalf("UpsertTrackedArtist failed: %v", err)
	}
	if err := service.SetAutoDownload(ctx, "AutoArtist", true); err != nil {
		t.Fatalf("SetAutoDownload failed: %v", err)
	}

	res, err := service.ScanArtist(ctx, "AutoArtist")
	if err != nil {
		t.Fatalf("ScanArtist failed: %v", err)
	}
	if res.NewReleases != 1 {
		t.Errorf("expected 1 new release, got %d", res.NewReleases)
	}

	// Verify download job was automatically scheduled
	jobs := jobMgr.GetAll()
	if len(jobs) != 1 {
		t.Errorf("expected 1 auto-download job to be created, got %d", len(jobs))
	}
}

func TestRadar_ScanAll_AlreadyRunning(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	aggregator := resolver.NewDiscographyAggregator(nil, nil, nil, database)
	service := NewService(database, aggregator, nil, nil)

	// Add 1 artist so ScanAll has work
	_ = database.UpsertTrackedArtist(ctx, "ArtistSlow")

	err := service.ScanAll(ctx, "first_run")
	if err != nil {
		t.Fatalf("first ScanAll failed: %v", err)
	}

	// Second concurrent ScanAll must return error
	err2 := service.ScanAll(ctx, "second_run")
	if err2 == nil {
		t.Errorf("expected error for already running scan, got nil")
	}

	service.CancelScan()
}

func TestRadar_ScanAll_NoArtists(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	aggregator := resolver.NewDiscographyAggregator(nil, nil, nil, database)
	service := NewService(database, aggregator, nil, nil)

	// No tracked artists
	if err := service.ScanAll(ctx, "empty_run"); err != nil {
		t.Fatalf("ScanAll failed: %v", err)
	}

	// Wait for goroutine to exit
	for i := 0; i < 50; i++ {
		if !service.GetProgress().IsRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if service.GetProgress().IsRunning {
		t.Errorf("expected ScanAll to complete quickly when no artists exist")
	}
}

func TestRadar_ScanAll_ProgressUpdate(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	deezerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/search/artist" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": 99, "name": "ActiveArtist"},
				},
			})
			return
		}
		if r.URL.Path == "/artist/99/albums" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"id":           3001,
						"title":        "Progress Album",
						"release_date": time.Now().Format("2006-01-02"),
						"record_type":  "album",
						"cover_xl":     "https://example.com/cover.jpg",
						"nb_tracks":    5,
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer deezerServer.Close()

	deezerClient := resolver.NewDeezerClient()
	deezerClient.SetBaseURL(deezerServer.URL)

	aggregator := resolver.NewDiscographyAggregator(deezerClient, nil, nil, database)
	service := NewService(database, aggregator, nil, nil)

	if err := database.UpsertTrackedArtist(ctx, "ActiveArtist"); err != nil {
		t.Fatalf("UpsertTrackedArtist failed: %v", err)
	}

	if err := service.ScanAll(ctx, "progress_test"); err != nil {
		t.Fatalf("ScanAll failed: %v", err)
	}

	// Wait until ActiveArtist is scanned
	for i := 0; i < 50; i++ {
		prog := service.GetProgress()
		if prog.CurrentIndex >= 1 && prog.CurrentArtist == "ActiveArtist" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	prog := service.GetProgress()
	if prog.TotalArtists != 1 {
		t.Errorf("expected TotalArtists = 1, got %d", prog.TotalArtists)
	}
	if prog.CurrentIndex != 1 {
		t.Errorf("expected CurrentIndex = 1, got %d", prog.CurrentIndex)
	}

	service.CancelScan()
}



