package radio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
)

// createSyntheticMP3 создает валидный синтетический MP3-файл с фреймами MPEG 1 Layer 3 (320 kbps, 44.1 kHz).
// Размер одного фрейма: 144 * 320000 / 44100 = 1044 байта.
func createSyntheticMP3(t *testing.T, fullPath string, numFrames int, withID3v2 bool, withID3v1 bool) {
	t.Helper()
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create dir %s: %v", dir, err)
	}

	f, err := os.Create(fullPath)
	if err != nil {
		t.Fatalf("failed to create file %s: %v", fullPath, err)
	}
	defer f.Close()

	// 1. Опциональный ID3v2 заголовок (10 байт заголовок + 10 байт тело = 20 байт)
	if withID3v2 {
		id3Header := []byte{
			'I', 'D', '3', 0x04, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x0A, // syncsafe size: 10 байт тела
		}
		_, _ = f.Write(id3Header)
		_, _ = f.Write([]byte("0123456789")) // Тело тега
	}

	// 2. Валидные MPEG 1 Layer 3 фреймы (каждый по 1044 байта)
	// Заголовок фрейма:
	// 0xFF 0xFB: syncword (11 бит 1) + MPEG 1 (01) + Layer 3 (01) + no CRC (1)
	// 0xE0: bitrate 320k (1110) + sample rate 44.1k (00) + no padding (0) + private (0)
	// 0x00: channel mode Stereo (00) + mode ext (00) + no copyright (0) + original (0) + emphasis (00)
	frameHeader := []byte{0xFF, 0xFB, 0xE0, 0x00}
	framePayload := make([]byte, 1040)
	for i := range framePayload {
		framePayload[i] = byte(i % 256)
	}

	for i := 0; i < numFrames; i++ {
		_, _ = f.Write(frameHeader)
		_, _ = f.Write(framePayload)
	}

	// 3. Опциональный ID3v1 трейлер в конце файла (128 байт, начинаются с "TAG")
	if withID3v1 {
		id3v1 := make([]byte, 128)
		copy(id3v1[:3], []byte("TAG"))
		copy(id3v1[3:33], []byte("Test Title"))
		copy(id3v1[33:63], []byte("Test Artist"))
		_, _ = f.Write(id3v1)
	}
}

func TestIcyStreamer_ServeHTTP_LiveStreamWithMetadata(t *testing.T) {
	database, tempDir, cleanup := setupTestDB(t)
	defer cleanup()

	sb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	// Создаем синтетические MP3 файлы для треков из setupTestDB (350 фреймов ~ 365 КБ каждый)
	// Это превышает размер стартового буфера BurstBytes (320 КБ) и интервала ICYMetaInt (16 КБ)
	track1Abs := filepath.Join(tempDir, "Daft Punk/Discovery/01. One More Time.mp3")
	track2Abs := filepath.Join(tempDir, "Daft Punk/Discovery/02. Aerodynamic.mp3")
	track3Abs := filepath.Join(tempDir, "The Weeknd/Starboy/01. Starboy.mp3")

	createSyntheticMP3(t, track1Abs, 350, true, true)
	createSyntheticMP3(t, track2Abs, 350, true, false)
	createSyntheticMP3(t, track3Abs, 350, false, true)

	simEngine := resolver.NewSimilarityEngine(nil, nil, database)
	selector := NewWaveSelector(database, nil, simEngine)
	streamer := NewIcyStreamer(selector, sb, database, "")

	// Клиент с поддержкой Icy-MetaData
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequestWithContext(ctx, "GET", "/api/radio/stream/wave", nil)
	req.Header.Set("Icy-MetaData", "1")

	// Используем pipe для симуляции реального сетевого стриминга
	pr, pw := io.Pipe()

	rec := newCustomResponseWriter(pw)

	// Запускаем стриминг в отдельной горутине
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		streamer.ServeHTTP(rec, req, "wave", "")
		pw.Close()
	}()

	// Ждем начала передачи данных (все заголовки гарантированно записаны)
	select {
	case <-rec.firstWrite:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for first write")
	}

	// 1. Проверяем HTTP-заголовки
	if rec.header.Get("Content-Type") != "audio/mpeg" {
		t.Errorf("expected Content-Type audio/mpeg, got %s", rec.header.Get("Content-Type"))
	}
	if rec.header.Get("icy-metaint") != "16384" {
		t.Errorf("expected icy-metaint 16384, got %s", rec.header.Get("icy-metaint"))
	}
	if rec.header.Get("Accept-Ranges") != "none" {
		t.Errorf("expected Accept-Ranges none, got %s", rec.header.Get("Accept-Ranges"))
	}

	// 2. Считываем данные из потока (стартовый burst 320 КБ отдается моментально)
	readBuf := make([]byte, 16384+1000)
	n, readErr := io.ReadFull(pr, readBuf)
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		t.Fatalf("failed to read from stream: %v (read %d bytes)", readErr, n)
	}
	if n < 16384 {
		t.Fatalf("expected at least 16384 bytes, got %d", n)
	}

	// 3. Отменяем контекст запроса, симулируя отключение плеера
	cancel()
	_ = pr.Close()

	// 4. Убеждаемся, что серверная горутина завершилась корректно без зависания
	select {
	case <-serverDone:
		// Успешный выход
	case <-time.After(2 * time.Second):
		t.Fatal("streamer goroutine leaked or did not terminate after client context cancel")
	}

	// 5. Проверяем GetNowPlaying
	nowPlaying, history := streamer.GetNowPlaying("wave", "")
	if nowPlaying == nil {
		t.Error("expected nowPlaying to be set")
	} else if nowPlaying.Artist == "" || nowPlaying.Title == "" {
		t.Errorf("expected valid track info, got %+v", nowPlaying)
	}
	if len(history) == 0 {
		t.Error("expected history to have at least one entry")
	}
}

func TestIcyStreamer_ServeHTTP_CleanAudioStreamNoIcy(t *testing.T) {
	database, tempDir, cleanup := setupTestDB(t)
	defer cleanup()

	sb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	track1Abs := filepath.Join(tempDir, "Daft Punk/Discovery/01. One More Time.mp3")
	track2Abs := filepath.Join(tempDir, "Daft Punk/Discovery/02. Aerodynamic.mp3")
	track3Abs := filepath.Join(tempDir, "The Weeknd/Starboy/01. Starboy.mp3")
	createSyntheticMP3(t, track1Abs, 50, false, false)
	createSyntheticMP3(t, track2Abs, 50, false, false)
	createSyntheticMP3(t, track3Abs, 50, false, false)

	simEngine := resolver.NewSimilarityEngine(nil, nil, database)
	selector := NewWaveSelector(database, nil, simEngine)
	streamer := NewIcyStreamer(selector, sb, database, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Без Icy-MetaData заголовка
	req := httptest.NewRequestWithContext(ctx, "GET", "/api/radio/stream/wave", nil)
	pr, pw := io.Pipe()

	rec := newCustomResponseWriter(pw)

	go func() {
		streamer.ServeHTTP(rec, req, "wave", "")
		pw.Close()
	}()

	select {
	case <-rec.firstWrite:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for first write in clean stream")
	}

	// icy-metaint не должен быть выставлен
	if rec.header.Get("icy-metaint") != "" {
		t.Errorf("expected no icy-metaint header when client did not request it, got %s", rec.header.Get("icy-metaint"))
	}

	buf := make([]byte, 4000)
	n, err := pr.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("failed to read plain audio stream: %v", err)
	}

	cancel()
	_ = pr.Close()
}

func TestIcyStreamer_ServeHTTP_HEAD_Method(t *testing.T) {
	database, tempDir, cleanup := setupTestDB(t)
	defer cleanup()

	sb, _ := sandbox.NewPathSandbox(tempDir)
	track1Abs := filepath.Join(tempDir, "Daft Punk/Discovery/01. One More Time.mp3")
	createSyntheticMP3(t, track1Abs, 10, false, false)

	simEngine := resolver.NewSimilarityEngine(nil, nil, database)
	selector := NewWaveSelector(database, nil, simEngine)
	streamer := NewIcyStreamer(selector, sb, database, "")

	req := httptest.NewRequest("HEAD", "/api/radio/stream/wave", nil)
	rec := httptest.NewRecorder()

	streamer.ServeHTTP(rec, req, "wave", "")

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for HEAD request, got %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "audio/mpeg" {
		t.Errorf("expected audio/mpeg Content-Type")
	}
	if rec.Header().Get("Accept-Ranges") != "none" {
		t.Errorf("expected Accept-Ranges: none")
	}
	if rec.Body.Len() > 0 {
		t.Errorf("HEAD response must not have body data, got %d bytes", rec.Body.Len())
	}
}

func TestIcyStreamer_ServeHTTP_EmptyLibrary(t *testing.T) {
	// Пустая БД без треков
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "empty.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	sb, _ := sandbox.NewPathSandbox(tempDir)
	simEngine := resolver.NewSimilarityEngine(nil, nil, database)
	selector := NewWaveSelector(database, nil, simEngine)
	streamer := NewIcyStreamer(selector, sb, database, "")

	req := httptest.NewRequest("GET", "/api/radio/stream/wave", nil)
	rec := httptest.NewRecorder()

	streamer.ServeHTTP(rec, req, "wave", "")

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 Not Found for empty library, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No tracks available") {
		t.Errorf("unexpected error body: %s", rec.Body.String())
	}
}

func TestIcyStreamer_TokenValidation(t *testing.T) {
	database, tempDir, cleanup := setupTestDB(t)
	defer cleanup()

	sb, _ := sandbox.NewPathSandbox(tempDir)
	track1Abs := filepath.Join(tempDir, "Daft Punk/Discovery/01. One More Time.mp3")
	createSyntheticMP3(t, track1Abs, 10, false, false)

	simEngine := resolver.NewSimilarityEngine(nil, nil, database)
	selector := NewWaveSelector(database, nil, simEngine)
	streamer := NewIcyStreamer(selector, sb, database, "my-secret-token")

	// 1. Неверный токен через query -> 401
	reqBad := httptest.NewRequest("GET", "/api/radio/stream/wave?token=wrong_token", nil)
	recBad := httptest.NewRecorder()
	streamer.ServeHTTP(recBad, reqBad, "wave", "")
	if recBad.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong token, got %d", recBad.Code)
	}

	// 2. Неверный токен через X-Stream-Token -> 401
	reqHdrBad := httptest.NewRequest("GET", "/api/radio/stream/wave", nil)
	reqHdrBad.Header.Set("X-Stream-Token", "wrong_token")
	recHdrBad := httptest.NewRecorder()
	streamer.ServeHTTP(recHdrBad, reqHdrBad, "wave", "")
	if recHdrBad.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong header token, got %d", recHdrBad.Code)
	}

	// 3. Валидация методов ValidateToken
	if !streamer.ValidateToken("my-secret-token") {
		t.Error("expected valid token to pass")
	}
	if streamer.ValidateToken("invalid-token") {
		t.Error("expected invalid token to fail")
	}

	// 4. Открытый стример (token = "")
	openStreamer := NewIcyStreamer(selector, sb, database, "")
	if !openStreamer.ValidateToken("any-token") {
		t.Error("expected open streamer to accept any token")
	}
	if !openStreamer.ValidateToken("") {
		t.Error("expected open streamer to accept empty token")
	}
}

// customResponseWriter реализует http.ResponseWriter и http.Flusher поверх io.Writer с потокобезопасной синхронизацией заголовков.
type customResponseWriter struct {
	header     http.Header
	writer     io.Writer
	statusCode int
	firstWrite chan struct{}
	once       sync.Once
}

func newCustomResponseWriter(w io.Writer) *customResponseWriter {
	return &customResponseWriter{
		header:     make(http.Header),
		writer:     w,
		firstWrite: make(chan struct{}),
	}
}

func (c *customResponseWriter) Header() http.Header {
	return c.header
}

func (c *customResponseWriter) Write(p []byte) (int, error) {
	c.once.Do(func() {
		close(c.firstWrite)
	})
	return c.writer.Write(p)
}

func (c *customResponseWriter) WriteHeader(statusCode int) {
	c.statusCode = statusCode
}

func (c *customResponseWriter) Flush() {
	// Flusher stub
}
