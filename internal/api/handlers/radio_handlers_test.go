package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"music-manager/internal/config"
	"music-manager/internal/core/auth"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/services/radio"
	"music-manager/internal/services/resolver"
)

func setupTestRadioHandler(t *testing.T) (*RadioHandler, *db.DB, string, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "radio-handler-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	musicDir := filepath.Join(tempDir, "music")
	_ = os.MkdirAll(musicDir, 0755)
	sb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatalf("sandbox error: %v", err)
	}

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db open error: %v", err)
	}

	cfg := &config.Config{
		RadioStreamToken: "secret-token-123",
		RadioWaveSize:    25,
		SecretKey:        "jwt-secret-key-123456",
	}

	similarity := resolver.NewSimilarityEngine(nil, nil, database)
	selector := radio.NewWaveSelector(database, nil, similarity)
	streamer := radio.NewIcyStreamer(selector, sb, database, cfg.RadioStreamToken)
	m3u := radio.NewM3UGenerator(selector, sb, cfg.RadioWaveSize)

	handler := NewRadioHandler(selector, streamer, m3u, nil, nil, database, cfg)

	cleanup := func() {
		database.Close()
		os.RemoveAll(tempDir)
	}

	return handler, database, musicDir, cleanup
}

func TestRadioHandler_Endpoints(t *testing.T) {
	handler, database, musicDir, cleanup := setupTestRadioHandler(t)
	defer cleanup()

	ctx := context.Background()

	// Создаем тестовый файл и запись в БД
	trackPath := filepath.Join(musicDir, "The Weeknd/Starboy/01. Starboy.mp3")
	_ = os.MkdirAll(filepath.Dir(trackPath), 0755)
	_ = os.WriteFile(trackPath, make([]byte, 1024), 0644)

	_, execErr := database.Conn().ExecContext(ctx, `
		INSERT INTO artists (id, name, clean_name) VALUES (1, 'The Weeknd', 'the weeknd');
		INSERT INTO genres (id, name) VALUES (1, 'R&B');
		INSERT INTO albums (id, title, clean_title, album_artist, artist_id, folder_path)
		VALUES (1, 'Starboy', 'starboy', 'The Weeknd', 1, 'The Weeknd - Starboy');
		INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, duration, bitrate, file_size, mtime, file_hash)
		VALUES (101, 1, 1, 'Starboy', 'starboy', 'The Weeknd/Starboy/01. Starboy.mp3', 230, 320, 1024, CURRENT_TIMESTAMP, 'hash101');
		INSERT INTO track_genres (track_id, genre_id) VALUES (101, 1);
	`)
	if execErr != nil {
		t.Fatalf("failed to insert test track data: %v", execErr)
	}

	// 1. GET /api/radio/wave/queue
	req := httptest.NewRequest("GET", "/api/radio/wave/queue?limit=10", nil)
	w := httptest.NewRecorder()
	handler.GetWaveQueue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetWaveQueue failed with status %d", w.Code)
	}
	var queueResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &queueResp)
	if queueResp["count"].(float64) < 1 {
		t.Errorf("expected at least 1 track in queue, got %v", queueResp["count"])
	}

	// 2. GET /api/radio/wave/stats
	req = httptest.NewRequest("GET", "/api/radio/wave/stats", nil)
	w = httptest.NewRecorder()
	handler.GetWaveStats(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetWaveStats failed with status %d", w.Code)
	}

	// 3. GET /api/radio/genres
	req = httptest.NewRequest("GET", "/api/radio/genres", nil)
	w = httptest.NewRecorder()
	handler.GetGenres(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetGenres failed with status %d", w.Code)
	}

	// 4. POST /api/radio/m3u/generate
	req = httptest.NewRequest("POST", "/api/radio/m3u/generate", nil)
	w = httptest.NewRecorder()
	handler.GenerateM3U(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GenerateM3U failed with status %d: %s", w.Code, w.Body.String())
	}

	// 5. Проверка авторизации потока (StreamWave)
	// Без токена -> 401
	reqUnauth := httptest.NewRequest("GET", "/api/radio/stream/wave", nil)
	wUnauth := httptest.NewRecorder()
	handler.StreamWave(wUnauth, reqUnauth)
	if wUnauth.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized without token, got %d", wUnauth.Code)
	}

	// С правильным токеном в query
	reqAuth := httptest.NewRequest("GET", "/api/radio/stream/wave?token=secret-token-123", nil)
	authPass := handler.checkStreamAuth(reqAuth)
	if !authPass {
		t.Error("expected auth to pass with query token")
	}

	// С правильным X-Stream-Token заголовком
	reqXToken := httptest.NewRequest("GET", "/api/radio/stream/wave", nil)
	reqXToken.Header.Set("X-Stream-Token", "secret-token-123")
	if !handler.checkStreamAuth(reqXToken) {
		t.Error("expected auth to pass with X-Stream-Token")
	}

	// С правильным Bearer заголовком
	reqBearer := httptest.NewRequest("GET", "/api/radio/stream/wave", nil)
	reqBearer.Header.Set("Authorization", "Bearer secret-token-123")
	if !handler.checkStreamAuth(reqBearer) {
		t.Error("expected auth to pass with Bearer token")
	}

	// С валидной сессионной cookie браузера (как веб-плеер)
	sessionToken := auth.CreateSessionToken("admin", handler.cfg.SecretKey)
	reqCookie := httptest.NewRequest("GET", "/api/radio/stream/wave", nil)
	reqCookie.AddCookie(&http.Cookie{
		Name:  auth.CookieName,
		Value: sessionToken,
	})
	if !handler.checkStreamAuth(reqCookie) {
		t.Error("expected auth to pass with valid session cookie")
	}

	// 6. Проверка CORS OPTIONS preflight
	reqOptions := httptest.NewRequest("OPTIONS", "/api/radio/stream/wave", nil)
	wOptions := httptest.NewRecorder()
	handler.HandleStreamOptions(wOptions, reqOptions)
	if wOptions.Code != http.StatusNoContent {
		t.Errorf("expected 204 No Content for OPTIONS, got %d", wOptions.Code)
	}
	if wOptions.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected Access-Control-Allow-Origin: *")
	}

	// 7. Проверка передачи stream_token в GetWaveStats
	reqStats := httptest.NewRequest("GET", "/api/radio/wave/stats", nil)
	wStats := httptest.NewRecorder()
	handler.GetWaveStats(wStats, reqStats)
	if !strings.Contains(wStats.Body.String(), `"stream_token":"secret-token-123"`) {
		t.Errorf("expected stream_token in stats body, got: %s", wStats.Body.String())
	}

	// 8. Проверка открытого стрима (когда RADIO_STREAM_TOKEN не задан в .env)
	handler.cfg.RadioStreamToken = ""
	reqOpen := httptest.NewRequest("GET", "/api/radio/stream/wave", nil)
	if !handler.checkStreamAuth(reqOpen) {
		t.Error("expected auth to pass when RadioStreamToken is empty")
	}

	// 9. Проверка HEAD запроса для открытого стрима
	reqHead := httptest.NewRequest("HEAD", "/api/radio/stream/wave", nil)
	wHead := httptest.NewRecorder()
	handler.StreamWave(wHead, reqHead)
	if wHead.Code != http.StatusOK {
		t.Errorf("expected 200 OK for HEAD request, got %d", wHead.Code)
	}
	if wHead.Header().Get("Accept-Ranges") != "none" {
		t.Errorf("expected Accept-Ranges: none, got %q", wHead.Header().Get("Accept-Ranges"))
	}
}
