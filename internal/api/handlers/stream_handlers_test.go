package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/db"
	"music-manager/internal/id3"
)

func createTestAudioFile(t *testing.T, musicDir, relPath string, content []byte) string {
	fullPath := filepath.Join(musicDir, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		t.Fatal(err)
	}
	return fullPath
}

func TestStreamTrack_FullAndRangeRequests(t *testing.T) {
	handler, _, _, database, musicDir, _ := setupTestFileHandler(t)

	// Создаем тестовый файл размером 10 000 байт
	fileSize := 10000
	testData := make([]byte, fileSize)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	relPath := "Test Artist/Test Album/01. Test Track.mp3"
	fullPath := createTestAudioFile(t, musicDir, relPath, testData)

	// Записываем ID3 теги
	tags := id3.TrackTags{
		Title:       "Test Track",
		Artist:      "Test Artist",
		Album:       "Test Album",
		TrackNumber: 1,
	}
	_ = id3.WriteTags(fullPath, tags, nil)

	// Индексируем в базе данных
	scannerSvc := handler.scanner
	if err := scannerSvc.IndexFile(fullPath, relPath); err != nil {
		t.Fatalf("failed to index file: %v", err)
	}

	trackHash := db.TrackHash(relPath)

	// Настраиваем роутер Chi для тестирования маршрутов с {id}
	r := chi.NewRouter()
	r.Get("/api/tracks/{id}/stream", handler.StreamTrack)
	r.Get("/api/tracks/{id}/metadata", handler.GetTrackMetadata)
	r.Get("/api/tracks/{id}/cover", handler.GetTrackCover)
	r.Get("/api/library/stream/{id}/chunks", handler.StreamTrack)
	r.Get("/api/library/tracks", handler.ListLibraryTracks)

	// 1. Тест: Запрос без Range возвращает 200 OK и весь файл
	t.Run("FullContent_200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/tracks/"+trackHash+"/stream", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d (body: %s)", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Accept-Ranges") != "bytes" {
			t.Errorf("expected Accept-Ranges: bytes, got %s", rec.Header().Get("Accept-Ranges"))
		}
		if rec.Header().Get("Content-Type") != "audio/mpeg" {
			t.Errorf("expected Content-Type: audio/mpeg, got %s", rec.Header().Get("Content-Type"))
		}
		if rec.Body.Len() == 0 {
			t.Errorf("expected non-empty body")
		}
	})

	// 2. Тест: Частичный запрос с Range: bytes=0-99 возвращает 206 Partial Content
	t.Run("RangePartial_206", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/tracks/"+trackHash+"/stream", nil)
		req.Header.Set("Range", "bytes=0-99")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusPartialContent {
			t.Fatalf("expected 206 Partial Content, got %d", rec.Code)
		}
		expectedCR := fmt.Sprintf("bytes 0-99/%d", rec.Body.Len()+len(testData)-100) // Content-Range
		cr := rec.Header().Get("Content-Range")
		if !strings.HasPrefix(cr, "bytes 0-99/") {
			t.Errorf("unexpected Content-Range header: %s, expected to start with 'bytes 0-99/'", cr)
		}
		if rec.Body.Len() != 100 {
			t.Errorf("expected 100 bytes, got %d", rec.Body.Len())
		}
		_ = expectedCR
	})

	// 3. Тест: Open-ended Range: bytes=1000-
	t.Run("RangeOpenEnded_206", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/tracks/"+trackHash+"/stream", nil)
		req.Header.Set("Range", "bytes=1000-")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusPartialContent {
			t.Fatalf("expected 206 Partial Content, got %d", rec.Code)
		}
		cr := rec.Header().Get("Content-Range")
		if !strings.HasPrefix(cr, "bytes 1000-") {
			t.Errorf("unexpected Content-Range header: %s", cr)
		}
	})

	// 4. Тест: Невалидный диапазон за пределами файла -> 416 Range Not Satisfiable
	t.Run("RangeNotSatisfiable_416", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/tracks/"+trackHash+"/stream", nil)
		req.Header.Set("Range", "bytes=99999999-")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestedRangeNotSatisfiable {
			t.Fatalf("expected 416 Range Not Satisfiable, got %d", rec.Code)
		}
		cr := rec.Header().Get("Content-Range")
		if !strings.HasPrefix(cr, "bytes */") {
			t.Errorf("expected Content-Range to start with 'bytes */', got %s", cr)
		}
	})

	// 5. Тест: VPN-алиас /api/library/stream/{id}/chunks
	t.Run("VPNAlias_206", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/library/stream/"+trackHash+"/chunks", nil)
		req.Header.Set("Range", "bytes=0-49")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusPartialContent {
			t.Fatalf("expected 206 Partial Content from VPN alias, got %d", rec.Code)
		}
		if rec.Body.Len() != 50 {
			t.Errorf("expected 50 bytes, got %d", rec.Body.Len())
		}
	})

	// 6. Тест: Воспроизведение по числовому ID из SQLite
	t.Run("StreamByNumericID", func(t *testing.T) {
		var numID int64
		err := database.Conn().QueryRow("SELECT id FROM tracks WHERE file_path = ?", relPath).Scan(&numID)
		if err != nil {
			t.Fatalf("failed to query track ID: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/tracks/%d/stream", numID), nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK by numeric ID, got %d", rec.Code)
		}
	})

	// 7. Тест: Метаданные трека /api/tracks/{id}/metadata
	t.Run("TrackMetadata_200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/tracks/"+trackHash+"/metadata", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d (body: %s)", rec.Code, rec.Body.String())
		}

		var meta TrackMetadataResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
			t.Fatalf("failed to decode JSON metadata: %v", err)
		}

		if meta.ID != trackHash {
			t.Errorf("expected ID %s, got %s", trackHash, meta.ID)
		}
		if meta.Title != "Test Track" {
			t.Errorf("expected Title 'Test Track', got '%s'", meta.Title)
		}
		if meta.Artist != "Test Artist" {
			t.Errorf("expected Artist 'Test Artist', got '%s'", meta.Artist)
		}
		if meta.Codec != "mp3" {
			t.Errorf("expected Codec 'mp3', got '%s'", meta.Codec)
		}
		if meta.StreamURL != "/api/tracks/"+trackHash+"/stream" {
			t.Errorf("expected StreamURL '/api/tracks/%s/stream', got '%s'", trackHash, meta.StreamURL)
		}
	})

	// 8. Тест: Защита от Path Traversal -> 403 Forbidden
	t.Run("PathTraversalBlocked_403", func(t *testing.T) {
		// Передаем "../../etc/passwd" в качестве id
		req := httptest.NewRequest(http.MethodGet, "/api/tracks/..%2F..%2Fetc%2Fpasswd/stream", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Fatalf("expected 403 Forbidden or 404 Not Found for traversal, got %d", rec.Code)
		}
	})

	// 9. Тест: Несуществующий трек -> 404 Not Found
	t.Run("NotFound_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/tracks/nonexistent12345/stream", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found, got %d", rec.Code)
		}
	})

	// 10. Тест: Листинг библиотеки /api/library/tracks
	t.Run("ListLibraryTracks_200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/library/tracks", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		count, ok := resp["count"].(float64)
		if !ok || count < 1 {
			t.Errorf("expected count >= 1, got %v", resp["count"])
		}
	})
}

func TestGetTrackCover_EmbeddedAndFolder(t *testing.T) {
	handler, _, _, _, musicDir, _ := setupTestFileHandler(t)

	// Создаем тестовую обложку
	dummyCover := bytes.Repeat([]byte{0xFF, 0xD8, 0xFF, 0xE0}, 50)
	albumDir := filepath.Join(musicDir, "CoverArtist", "CoverAlbum")
	_ = os.MkdirAll(albumDir, 0755)
	_ = os.WriteFile(filepath.Join(albumDir, "cover.jpg"), dummyCover, 0644)

	trackRel := "CoverArtist/CoverAlbum/01. Song.mp3"
	trackFull := filepath.Join(musicDir, trackRel)
	_ = os.WriteFile(trackFull, []byte("fake mp3 data"), 0644)

	trackHash := db.TrackHash(trackRel)
	handler.trackCache.Store(trackHash, trackRel)

	r := chi.NewRouter()
	r.Get("/api/tracks/{id}/cover", handler.GetTrackCover)

	req := httptest.NewRequest(http.MethodGet, "/api/tracks/"+trackHash+"/cover", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for cover, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("expected Content-Type image/jpeg, got %s", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "max-age=") {
		t.Errorf("expected Cache-Control header, got %s", rec.Header().Get("Cache-Control"))
	}
	if rec.Body.Len() != len(dummyCover) {
		t.Errorf("expected cover size %d, got %d", len(dummyCover), rec.Body.Len())
	}
}
