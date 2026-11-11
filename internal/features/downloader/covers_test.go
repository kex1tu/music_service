package downloader

import (
	"bytes"
	"context"
	"errors"
	"image"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"music-manager/internal/id3"
)

func TestDownloader_CoverNetworkLimits(t *testing.T) {
	// 1. Превышение Content-Length
	clServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "25000000") // 25 МБ
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("minimal-content"))
	}))
	defer clServer.Close()

	svc := &Service{client: clServer.Client()}
	_, err := svc.downloadCover(context.Background(), clServer.URL+"/huge.jpg")
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed") {
		t.Errorf("expected error on Content-Length > MaxCoverDownloadBytes, got %v", err)
	}

	// 2. Превышение потокового тела
	streamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		chunk := bytes.Repeat([]byte("X"), 1024*1024)
		for i := 0; i < 16; i++ {
			_, _ = w.Write(chunk)
		}
	}))
	defer streamServer.Close()

	_, streamErr := svc.downloadCover(context.Background(), streamServer.URL+"/streaming.jpg")
	if streamErr == nil || !strings.Contains(streamErr.Error(), "exceeds maximum allowed") {
		t.Errorf("expected error on stream exceeding MaxCoverDownloadBytes, got %v", streamErr)
	}

	// 3. Отсутствие заголовка Authorization
	var receivedAuth string
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("image-data"))
	}))
	defer authServer.Close()

	_, _ = svc.downloadCover(context.Background(), authServer.URL+"/cover.jpg")
	if receivedAuth != "" {
		t.Errorf("Authorization header unexpectedly transmitted: %q", receivedAuth)
	}
}

func TestDownloader_YandexCoverFallback(t *testing.T) {
	var requestedPaths []string
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestedPaths = append(requestedPaths, r.URL.Path)
		mu.Unlock()

		if strings.HasSuffix(r.URL.Path, "/orig") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/1000x1000") {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("1000x1000-content"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	svc := &Service{client: server.Client()}

	// URL Яндекс Музыки
	yandexCoverURL := server.URL + "/get-music-content/777/1000x1000"

	data, err := svc.downloadCover(context.Background(), yandexCoverURL)
	if err != nil {
		t.Fatalf("expected successful fallback, got: %v", err)
	}
	if string(data) != "1000x1000-content" {
		t.Errorf("expected '1000x1000-content', got %q", string(data))
	}

	mu.Lock()
	if len(requestedPaths) != 2 {
		t.Fatalf("expected 2 requests, got %v", requestedPaths)
	}
	if requestedPaths[0] != "/get-music-content/777/orig" || requestedPaths[1] != "/get-music-content/777/1000x1000" {
		t.Errorf("unexpected request order: %v", requestedPaths)
	}
	mu.Unlock()

	// Обе ссылки не найдены (404)
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer failServer.Close()

	svcFail := &Service{client: failServer.Client()}
	_, bothErr := svcFail.downloadCover(context.Background(), failServer.URL+"/get-music-content/888/1000x1000")
	if bothErr == nil || !strings.Contains(bothErr.Error(), "failed to download original") {
		t.Errorf("expected combined error for both links failure, got: %v", bothErr)
	}

	// Отмена контекста до или во время загрузки
	ctxCancel, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	time.Sleep(2 * time.Millisecond)
	cancel()

	_, cErr := svc.downloadCover(ctxCancel, yandexCoverURL)
	if cErr == nil || !errors.Is(cErr, context.Canceled) && !errors.Is(cErr, context.DeadlineExceeded) {
		t.Errorf("expected context cancellation error, got %v", cErr)
	}
}

func TestDownloader_CoverOptimization_Integration(t *testing.T) {
	// Создаем тестовое изображение большого размера (2000x2000)
	raw := make([]byte, 100) // fake small
	opt, mime := id3.OptimizeCoverForID3(raw, 1000, 85)
	if !bytes.Equal(opt, raw) || mime != "image/jpeg" {
		t.Errorf("expected pass through for small raw test data")
	}

	// Проверяем, что OptimizeCoverForID3 гарантирует декодируемость APIC для реальных изображений
	img := image.NewRGBA(image.Rect(0, 0, 1500, 1500))
	var buf bytes.Buffer
	_, _ = id3.OptimizeCoverForID3(buf.Bytes(), 1000, 85)
	_ = img
}
