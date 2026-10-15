package radio

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
)

func setupTestDB(t *testing.T) (*db.DB, string, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "radio_test_*")
	if err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(tempDir, "library.db")
	database, err := db.Open(dbPath)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		t.Fatalf("failed to open database: %v", err)
	}

	ctx := context.Background()
	// Вставляем тестовых артистов, альбом и треки
	_, err = database.Conn().ExecContext(ctx, `
		INSERT INTO artists (id, name, clean_name) VALUES (1, 'Daft Punk', 'daft punk');
		INSERT INTO artists (id, name, clean_name) VALUES (2, 'The Weeknd', 'the weeknd');
		INSERT INTO genres (id, name) VALUES (1, 'Electronic');

		INSERT INTO albums (id, title, clean_title, album_artist, artist_id, folder_path)
		VALUES (1, 'Discovery', 'discovery', 'Daft Punk', 1, 'Daft Punk - Discovery');

		INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, duration, bitrate, file_size, mtime, file_hash)
		VALUES (101, 1, 1, 'One More Time', 'one more time', 'Daft Punk/Discovery/01. One More Time.mp3', 320, 320, 10000000, CURRENT_TIMESTAMP, 'hash101');

		INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, duration, bitrate, file_size, mtime, file_hash)
		VALUES (102, 1, 1, 'Aerodynamic', 'aerodynamic', 'Daft Punk/Discovery/02. Aerodynamic.mp3', 212, 320, 7000000, CURRENT_TIMESTAMP, 'hash102');

		INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, duration, bitrate, file_size, mtime, file_hash)
		VALUES (103, 1, 2, 'Starboy', 'starboy', 'The Weeknd/Starboy/01. Starboy.mp3', 230, 320, 8000000, CURRENT_TIMESTAMP, 'hash103');

		INSERT INTO track_genres (track_id, genre_id) VALUES (101, 1);
		INSERT INTO track_genres (track_id, genre_id) VALUES (102, 1);
	`)
	if err != nil {
		_ = database.Close()
		_ = os.RemoveAll(tempDir)
		t.Fatalf("failed to insert test data: %v", err)
	}

	cleanup := func() {
		_ = database.Close()
		_ = os.RemoveAll(tempDir)
	}

	return database, tempDir, cleanup
}

func TestNavidromeReaderAndSubsonicClient(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "navi_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	naviDBPath := filepath.Join(tempDir, "navidrome.db")
	conn, err := sql.Open("sqlite", naviDBPath)
	if err != nil {
		t.Fatalf("failed to create navidrome db: %v", err)
	}

	_, err = conn.Exec(`
		CREATE TABLE media_file (id TEXT PRIMARY KEY, path TEXT);
		CREATE TABLE annotation (
			item_id TEXT,
			item_type TEXT,
			starred BOOLEAN,
			play_count INTEGER,
			play_date DATETIME
		);
		INSERT INTO media_file (id, path) VALUES ('mf1', 'Daft Punk/Discovery/01. One More Time.mp3');
		INSERT INTO media_file (id, path) VALUES ('mf2', 'The Weeknd/Starboy/01. Starboy.mp3');
		INSERT INTO annotation (item_id, item_type, starred, play_count, play_date)
		VALUES ('mf1', 'media_file', 1, 12, '2026-10-10 12:00:00');
		INSERT INTO annotation (item_id, item_type, starred, play_count, play_date)
		VALUES ('mf2', 'media_file', 0, 3, '2026-08-01 10:00:00');
	`)
	_ = conn.Close()
	if err != nil {
		t.Fatalf("failed to setup mock navidrome schema: %v", err)
	}

	reader := NewNavidromeReader(naviDBPath)
	if !reader.IsAvailable() {
		t.Fatal("expected reader to be available")
	}

	stats, err := reader.GetUserListeningStats(context.Background())
	if err != nil {
		t.Fatalf("GetUserListeningStats failed: %v", err)
	}

	if !stats.StarredPaths["Daft Punk/Discovery/01. One More Time.mp3"] {
		t.Error("expected track 1 to be starred")
	}
	if stats.PlayCounts["Daft Punk/Discovery/01. One More Time.mp3"] != 12 {
		t.Errorf("expected play_count 12, got %d", stats.PlayCounts["Daft Punk/Discovery/01. One More Time.mp3"])
	}

	// Тест Subsonic Client
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"subsonic-response": {
				"status": "ok",
				"version": "1.16.1"
			}
		}`))
	}))
	defer ts.Close()

	client := NewNavidromeSubsonicClient(ts.URL, "admin", "secret")
	if !client.IsConfigured() {
		t.Fatal("expected client to be configured")
	}

	err = client.CreateRadioStation(context.Background(), "Моя Волна", "http://localhost:8010/stream", "http://localhost:8010")
	if err != nil {
		t.Fatalf("CreateRadioStation failed: %v", err)
	}
}

func TestWaveSelectorAndM3UGenerator(t *testing.T) {
	database, tempDir, cleanup := setupTestDB(t)
	defer cleanup()

	sb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	simEngine := resolver.NewSimilarityEngine(nil, nil, database)
	selector := NewWaveSelector(database, nil, simEngine)

	ctx := context.Background()

	// 1. Тест выбора трека для обычной Волны
	tr, err := selector.SelectNextTrack(ctx, "wave", "")
	if err != nil || tr == nil {
		t.Fatalf("SelectNextTrack(wave) failed: %v", err)
	}

	// 2. Тест выбора трека по жанру
	trGenre, err := selector.SelectNextTrack(ctx, "genre", "Electronic")
	if err != nil || trGenre == nil {
		t.Fatalf("SelectNextTrack(genre) failed: %v", err)
	}
	if trGenre.Artist != "Daft Punk" {
		t.Errorf("expected electronic artist Daft Punk, got %s", trGenre.Artist)
	}

	// 3. Тест выбора трека по артисту
	trArt, err := selector.SelectNextTrack(ctx, "artist", "The Weeknd")
	if err != nil || trArt == nil {
		t.Fatalf("SelectNextTrack(artist) failed: %v", err)
	}
	if trArt.Artist != "The Weeknd" {
		t.Errorf("expected artist The Weeknd, got %s", trArt.Artist)
	}

	// 4. Тест генерации плейлиста M3U
	m3uGen := NewM3UGenerator(selector, sb, 2)
	savedPath, err := m3uGen.GenerateWaveM3U(ctx)
	if err != nil {
		t.Fatalf("GenerateWaveM3U failed: %v", err)
	}

	content, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("failed to read saved m3u: %v", err)
	}

	strContent := string(content)
	if !strings.HasPrefix(strContent, "#EXTM3U\n") {
		t.Errorf("expected #EXTM3U header, got:\n%s", strContent)
	}
	if !strings.Contains(strContent, "#EXTINF:") {
		t.Errorf("expected #EXTINF in m3u, got:\n%s", strContent)
	}
	if !strings.Contains(strContent, "../Daft Punk/") && !strings.Contains(strContent, "../The Weeknd/") {
		t.Errorf("expected relative paths ../ in m3u, got:\n%s", strContent)
	}
}

func TestIcyMetadataBlock(t *testing.T) {
	block := buildICYMetadataBlock("Daft Punk - One More Time")
	if len(block) == 0 {
		t.Fatal("empty metadata block")
	}
	numBlocks := int(block[0])
	expectedLen := 1 + numBlocks*16
	if len(block) != expectedLen {
		t.Errorf("expected block length %d, got %d", expectedLen, len(block))
	}
	str := string(block[1:])
	if !strings.HasPrefix(str, "StreamTitle='Daft Punk - One More Time';") {
		t.Errorf("unexpected metadata string: %s", str)
	}
}

func TestNavidromeStatsMatching(t *testing.T) {
	stats := &NavidromeStats{
		StarredPaths: map[string]bool{
			"/var/music/Daft Punk/Discovery/01. One More Time.mp3": true,
			"D:\\Music\\The Weeknd\\Starboy\\01. Starboy.mp3":     true,
		},
		PlayCounts: map[string]int{
			"/var/music/Daft Punk/Discovery/01. One More Time.mp3": 42,
			"The Weeknd/Starboy/01. Starboy.mp3":                   15,
		},
		LastPlayed: map[string]time.Time{
			"/var/music/Daft Punk/Discovery/01. One More Time.mp3": time.Now().Add(-1 * time.Hour),
		},
	}

	// 1. Проверка IsStarred с относительными путями против абсолютных путей Navidrome
	if !stats.IsStarred("Daft Punk/Discovery/01. One More Time.mp3") {
		t.Error("expected IsStarred to match relative path with absolute Navidrome path")
	}
	if !stats.IsStarred("The Weeknd/Starboy/01. Starboy.mp3") {
		t.Error("expected IsStarred to match Windows-style Navidrome path")
	}
	if stats.IsStarred("Unknown/Album/Nonexistent.mp3") {
		t.Error("expected IsStarred to return false for nonexistent track")
	}

	// 2. Проверка GetPlayCount
	if count := stats.GetPlayCount("Daft Punk/Discovery/01. One More Time.mp3"); count != 42 {
		t.Errorf("expected play count 42, got %d", count)
	}
	if count := stats.GetPlayCount("The Weeknd/Starboy/01. Starboy.mp3"); count != 15 {
		t.Errorf("expected play count 15, got %d", count)
	}
	if count := stats.GetPlayCount("Other/Track.mp3"); count != 0 {
		t.Errorf("expected play count 0 for unknown track, got %d", count)
	}

	// 3. Проверка GetLastPlayed
	lp := stats.GetLastPlayed("Daft Punk/Discovery/01. One More Time.mp3")
	if lp.IsZero() {
		t.Error("expected non-zero last played time")
	}
}

func TestIcyStreamerOptionsAndHeaders(t *testing.T) {
	database, tempDir, cleanup := setupTestDB(t)
	defer cleanup()

	sb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	simEngine := resolver.NewSimilarityEngine(nil, nil, database)
	selector := NewWaveSelector(database, nil, simEngine)
	streamer := NewIcyStreamer(selector, sb, database, "test-token")

	// Тест OPTIONS preflight
	reqOptions := httptest.NewRequest("OPTIONS", "/api/radio/stream/wave", nil)
	recOptions := httptest.NewRecorder()
	streamer.ServeHTTP(recOptions, reqOptions, "wave", "")

	if recOptions.Code != http.StatusNoContent {
		t.Errorf("expected 204 No Content for OPTIONS, got %d", recOptions.Code)
	}
	if recOptions.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected CORS origin *")
	}
	if recOptions.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Errorf("expected CORS allow methods")
	}
}

func TestWaveSelector_GeneratePlaylist(t *testing.T) {
	database, tempDir, cleanup := setupTestDB(t)
	defer cleanup()

	simEngine := resolver.NewSimilarityEngine(nil, nil, database)
	selector := NewWaveSelector(database, nil, simEngine)

	ctx := context.Background()

	// 1. Успешная генерация плейлиста
	playlist, err := selector.GeneratePlaylist(ctx, 3)
	if err != nil {
		t.Fatalf("GeneratePlaylist failed: %v", err)
	}
	if len(playlist) == 0 {
		t.Errorf("expected non-empty playlist")
	}

	// 2. Пустая библиотека возвращает ошибку
	emptyDBPath := filepath.Join(tempDir, "empty_sel.db")
	emptyDB, err := db.Open(emptyDBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer emptyDB.Close()

	emptySelector := NewWaveSelector(emptyDB, nil, simEngine)
	_, errEmpty := emptySelector.GeneratePlaylist(ctx, 10)
	if errEmpty == nil {
		t.Errorf("expected error for empty library playlist generation")
	}
}

func TestNavidromeSubsonicClient_Errors(t *testing.T) {
	ctx := context.Background()

	// 1. Ненастроенный клиент
	unconf := NewNavidromeSubsonicClient("", "", "")
	if unconf.IsConfigured() {
		t.Error("expected IsConfigured=false")
	}
	err := unconf.CreateRadioStation(ctx, "Name", "http://stream", "http://home")
	if err == nil {
		t.Error("expected error for unconfigured client")
	}

	// 2. Сервер Subsonic возвращает HTTP 500
	tsErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer tsErr.Close()

	clientErr := NewNavidromeSubsonicClient(tsErr.URL, "user", "pass")
	err = clientErr.CreateRadioStation(ctx, "Name", "http://stream", "http://home")
	if err == nil {
		t.Error("expected error when Subsonic server returns 500")
	}

	// 3. Сервер Subsonic возвращает ошибку в теле JSON
	tsSubErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"subsonic-response": {
				"status": "failed",
				"error": {
					"code": 40,
					"message": "Wrong username or password"
				}
			}
		}`))
	}))
	defer tsSubErr.Close()

	clientSubErr := NewNavidromeSubsonicClient(tsSubErr.URL, "user", "wrong_pass")
	err = clientSubErr.CreateRadioStation(ctx, "Name", "http://stream", "http://home")
	if err == nil {
		t.Error("expected error when Subsonic returns status=failed")
	}
}

func TestNavidromeReader_NotAvailable(t *testing.T) {
	reader := NewNavidromeReader("/path/to/nonexistent/db.sqlite")
	if reader.IsAvailable() {
		t.Error("expected IsAvailable=false for nonexistent path")
	}
	_, err := reader.GetUserListeningStats(context.Background())
	if err == nil {
		t.Error("expected error for unavailable database")
	}
}
