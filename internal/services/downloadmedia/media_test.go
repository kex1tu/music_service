package downloadmedia

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFfmpegArgs_320kbps(t *testing.T) {
	args := buildFfmpegArgs("input.raw", "output.mp3")

	has320k := false
	has44100 := false
	hasMp3Lame := false
	hasNoMetadata := false

	for i := 0; i < len(args); i++ {
		if args[i] == "-b:a" && i+1 < len(args) && args[i+1] == "320k" {
			has320k = true
		}
		if args[i] == "-ar" && i+1 < len(args) && args[i+1] == "44100" {
			has44100 = true
		}
		if args[i] == "-codec:a" && i+1 < len(args) && args[i+1] == "libmp3lame" {
			hasMp3Lame = true
		}
		if args[i] == "-map_metadata" && i+1 < len(args) && args[i+1] == "-1" {
			hasNoMetadata = true
		}
	}

	if !has320k {
		t.Errorf("expected -b:a 320k in ffmpeg arguments: %v", args)
	}
	if !has44100 {
		t.Errorf("expected -ar 44100 in ffmpeg arguments: %v", args)
	}
	if !hasMp3Lame {
		t.Errorf("expected -codec:a libmp3lame in ffmpeg arguments: %v", args)
	}
	if !hasNoMetadata {
		t.Errorf("expected -map_metadata -1 in ffmpeg arguments: %v", args)
	}
}

func TestTranscodeToMP3_RealFFmpeg(t *testing.T) {
	tmpDir := t.TempDir()
	inputWav := filepath.Join(tmpDir, "test_input.wav")
	outputMp3 := filepath.Join(tmpDir, "test_output.mp3")

	// Generate a 0.2s sine wave audio using ffmpeg
	genCmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=1000:duration=0.2", inputWav)
	if err := genCmd.Run(); err != nil {
		t.Skip("ffmpeg not available or lavfi sine unsupported, skipping")
	}

	ctx := context.Background()

	// 1. Success case
	err := TranscodeToMP3(ctx, inputWav, outputMp3)
	if err != nil {
		t.Fatalf("unexpected transcode error: %v", err)
	}
	fi, err := os.Stat(outputMp3)
	if err != nil || fi.Size() == 0 {
		t.Fatalf("expected non-empty output mp3 file")
	}

	probe, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=bit_rate,sample_rate", "-of", "default=noprint_wrappers=1", outputMp3).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(probe), "bit_rate=320000") || !strings.Contains(string(probe), "sample_rate=44100") {
		t.Fatalf("wrong audio output: %s", probe)
	}
	// 2. Context timeout cancellation case
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	outputCancelled := filepath.Join(tmpDir, "test_cancelled.mp3")
	err = TranscodeToMP3(cancelledCtx, inputWav, outputCancelled)
	if err == nil {
		t.Errorf("expected error on cancelled context, got nil")
	}
}

func TestDownloadCover_LimitsAndSecurity(t *testing.T) {
	// 1. Проверка отсечения по Content-Length > MaxCoverDownloadBytes (15 МБ)
	oversizedCLServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "20971520") // 20 МБ
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("minimal-body"))
	}))
	defer oversizedCLServer.Close()

	_, err := DownloadCover(context.Background(), oversizedCLServer.Client(), oversizedCLServer.URL+"/huge.jpg")
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed") {
		t.Errorf("expected error for oversized Content-Length, got: %v", err)
	}

	// 2. Проверка отсечения при потоковом превышении (сервер не шлет Content-Length, но шлет 16 МБ)
	streamingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		chunk := bytes.Repeat([]byte("A"), 1024*1024) // 1 МБ
		for i := 0; i < 16; i++ {
			_, _ = w.Write(chunk)
		}
	}))
	defer streamingServer.Close()

	_, streamErr := DownloadCover(context.Background(), oversizedCLServer.Client(), streamingServer.URL+"/stream.jpg")
	if streamErr == nil || !strings.Contains(streamErr.Error(), "exceeds maximum allowed") {
		t.Errorf("expected error for streaming body exceeding budget, got: %v", streamErr)
	}

	// 3. Проверка того, что заголовок Authorization не передается
	var receivedAuthHeader string
	authCheckServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("valid-cover-payload"))
	}))
	defer authCheckServer.Close()

	_, _ = DownloadCover(context.Background(), authCheckServer.Client(), authCheckServer.URL+"/art.jpg")
	if receivedAuthHeader != "" {
		t.Errorf("Authorization header leaked to image host: %q", receivedAuthHeader)
	}
}

func TestDownloadCover_YandexFallback(t *testing.T) {
	var requestedPaths []string
	var mu sync.Mutex

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestedPaths = append(requestedPaths, r.URL.Path)
		mu.Unlock()

		if strings.HasSuffix(r.URL.Path, "/orig") {
			// Оригинал недоступен (404)
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/1000x1000") {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("fallback-1000x1000-image-data"))
			return
		}
		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	// URL в стиле Яндекс Музыки (путь get-music-content)
	yandexCoverURL := mockServer.URL + "/get-music-content/12345/1000x1000"

	data, err := DownloadCover(context.Background(), mockServer.Client(), yandexCoverURL)
	if err != nil {
		t.Fatalf("expected successful fallback, got error: %v", err)
	}
	if string(data) != "fallback-1000x1000-image-data" {
		t.Errorf("got unexpected cover data: %q", string(data))
	}

	mu.Lock()
	if len(requestedPaths) != 2 {
		t.Fatalf("expected 2 requests (orig -> 1000x1000), got %v", requestedPaths)
	}
	if requestedPaths[0] != "/get-music-content/12345/orig" || requestedPaths[1] != "/get-music-content/12345/1000x1000" {
		t.Errorf("unexpected request sequence: %v", requestedPaths)
	}
	mu.Unlock()

	// Сценарий отказа обеих ссылок
	bothFailServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer bothFailServer.Close()

	failURL := bothFailServer.URL + "/get-music-content/999/1000x1000"
	_, failErr := DownloadCover(context.Background(), bothFailServer.Client(), failURL)
	if failErr == nil || !strings.Contains(failErr.Error(), "failed to download original") || !strings.Contains(failErr.Error(), "fallback 1000x1000") {
		t.Errorf("expected combined error for both links failure, got: %v", failErr)
	}

	// Сценарий отмены контекста: не должен пытаться делать fallback при отмене
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel() // Отменяем сразу
	_, cancelErr := DownloadCover(cancelCtx, mockServer.Client(), yandexCoverURL)
	if cancelErr == nil || !errors.Is(cancelErr, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", cancelErr)
	}
}

func TestFindDownloadedFile(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Success case: matching file exists
	targetFile := filepath.Join(tmpDir, "track_01.opus")
	_ = os.WriteFile(targetFile, []byte("audio-bytes"), 0644)

	tmpl := filepath.Join(tmpDir, "track_01.%(ext)s")
	found, err := findDownloadedFile(tmpl)
	if err != nil {
		t.Fatalf("unexpected error finding file: %v", err)
	}
	if found != targetFile {
		t.Errorf("expected %s, got %s", targetFile, found)
	}

	// 2. Error case: no matching file
	missingTmpl := filepath.Join(tmpDir, "track_99.%(ext)s")
	_, err = findDownloadedFile(missingTmpl)
	if err == nil {
		t.Errorf("expected error for missing file, got nil")
	}
}

func TestValidateDownloadedStreamAudio_Snippet(t *testing.T) {
	tmpDir := t.TempDir()
	shortWav := filepath.Join(tmpDir, "short_snippet.wav")

	// Generate a 5.0s audio file with ffmpeg
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=1000:duration=5.0", shortWav)
	if err := cmd.Run(); err != nil {
		t.Skip("ffmpeg not available, skipping")
	}

	ctx := context.Background()

	// 1. Snippet detected: actual 5s < 35s, but expected 180s (> 60s)
	err := validateDownloadedStreamAudio(ctx, shortWav, 180)
	if err == nil {
		t.Fatalf("expected error for 5s snippet when expecting 180s track, got nil")
	}
	t.Logf("correctly caught snippet: %v", err)

	// 2. Short skit/intro: actual 5s, expected 20s (<= 60s) -> allowed
	err = validateDownloadedStreamAudio(ctx, shortWav, 20)
	if err != nil {
		t.Fatalf("expected nil for short intro/skit, got error: %v", err)
	}

	// 3. Normal track: generate a 36s audio file (>= 35s)
	normalWav := filepath.Join(tmpDir, "normal_track.wav")
	cmdNormal := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=1000:duration=36.0", normalWav)
	if err := cmdNormal.Run(); err != nil {
		t.Skip("ffmpeg not available, skipping")
	}

	err = validateDownloadedStreamAudio(ctx, normalWav, 180)
	if err != nil {
		t.Fatalf("expected nil for 36s track with 180s expectation, got: %v", err)
	}
}

func TestValidateDownloadedStreamAudio_Matrix(t *testing.T) {
	tmpDir := t.TempDir()

	genAudio := func(name string, dur float64) string {
		path := filepath.Join(tmpDir, name)
		cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=1000:duration=%.1f", dur), path)
		if err := cmd.Run(); err != nil {
			t.Skipf("ffmpeg not available: %v", err)
		}
		return path
	}

	shortFile := genAudio("actual_20s.wav", 20.0) // actual < 35
	boundFile := genAudio("actual_35s.wav", 35.0) // actual == 35.0
	longFile := genAudio("actual_40s.wav", 40.0)  // actual > 35

	ctx := context.Background()

	tests := []struct {
		name        string
		file        string
		expectedSec int
		wantErr     bool
	}{
		// actual < 35
		{name: "actual_20s_expected_180s_preview_detected", file: shortFile, expectedSec: 180, wantErr: true},
		{name: "actual_20s_expected_61s_preview_detected", file: shortFile, expectedSec: 61, wantErr: true},
		{name: "actual_20s_expected_60s_boundary_allowed", file: shortFile, expectedSec: 60, wantErr: false},
		{name: "actual_20s_expected_45s_allowed", file: shortFile, expectedSec: 45, wantErr: false},
		{name: "actual_20s_expected_20s_short_legit_track_allowed", file: shortFile, expectedSec: 20, wantErr: false},
		{name: "actual_20s_expected_0s_unknown_allowed", file: shortFile, expectedSec: 0, wantErr: false},
		{name: "actual_20s_expected_negative_unknown_allowed", file: shortFile, expectedSec: -1, wantErr: false},

		// actual == 35.0
		{name: "actual_35s_boundary_expected_180s_allowed", file: boundFile, expectedSec: 180, wantErr: false},
		{name: "actual_35s_boundary_expected_60s_allowed", file: boundFile, expectedSec: 60, wantErr: false},

		// actual > 35
		{name: "actual_40s_expected_180s_allowed", file: longFile, expectedSec: 180, wantErr: false},
		{name: "actual_40s_expected_60s_allowed", file: longFile, expectedSec: 60, wantErr: false},
		{name: "actual_40s_expected_0s_unknown_allowed", file: longFile, expectedSec: 0, wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDownloadedStreamAudio(ctx, tc.file, tc.expectedSec)
			if tc.wantErr && err == nil {
				t.Fatalf("[%s] expected error, got nil", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("[%s] expected nil, got error: %v", tc.name, err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "detected 30s preview snippet") {
				t.Errorf("[%s] error should contain 'detected 30s preview snippet', got: %v", tc.name, err)
			}
		})
	}
}

func TestValidateDownloadedStreamAudio_ProbeErrorsAndContext(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Missing file -> probe error
	missing := filepath.Join(tmpDir, "non_existent.wav")
	err := validateDownloadedStreamAudio(context.Background(), missing, 180)
	if err == nil || !strings.Contains(err.Error(), "ffprobe probe failed") {
		t.Fatalf("expected ffprobe probe failed error for missing file, got %v", err)
	}

	// 2. Corrupt / empty file -> probe error
	corrupt := filepath.Join(tmpDir, "corrupt.wav")
	_ = os.WriteFile(corrupt, []byte("not an audio file at all garbage data"), 0644)
	err = validateDownloadedStreamAudio(context.Background(), corrupt, 180)
	if err == nil || !strings.Contains(err.Error(), "ffprobe probe failed") {
		t.Fatalf("expected ffprobe probe failed error for corrupt file, got %v", err)
	}

	// 3. Canceled context -> context.Canceled
	validFile := filepath.Join(tmpDir, "sample.wav")
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=1000:duration=2.0", validFile)
	if err := cmd.Run(); err != nil {
		t.Skip("ffmpeg not available")
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	err = validateDownloadedStreamAudio(canceledCtx, validFile, 180)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got: %v", err)
	}

	// 4. Timed out context -> context.DeadlineExceeded
	timeoutCtx, cancelTimeout := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancelTimeout()
	time.Sleep(2 * time.Millisecond) // ensure expired
	err = validateDownloadedStreamAudio(timeoutCtx, validFile, 180)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected deadline exceeded or canceled error, got: %v", err)
	}
}

func TestValidateDownloadedStreamAudio_MalformedAndNonNumericJSON(t *testing.T) {
	fakeDir := t.TempDir()
	origPath := os.Getenv("PATH")

	makeFakeFfprobe := func(scriptBody string) {
		bin := filepath.Join(fakeDir, "ffprobe")
		content := "#!/bin/sh\n" + scriptBody + "\n"
		if err := os.WriteFile(bin, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+origPath)

	dummyFile := filepath.Join(fakeDir, "test.wav")
	_ = os.WriteFile(dummyFile, []byte("dummy"), 0644)

	ctx := context.Background()

	// 1. Malformed JSON -> returns json unmarshal error
	makeFakeFfprobe(`echo "not a valid json output"`)
	err := validateDownloadedStreamAudio(ctx, dummyFile, 180)
	if err == nil || !strings.Contains(err.Error(), "failed to parse ffprobe json") {
		t.Fatalf("expected json unmarshal error, got: %v", err)
	}

	// 2. Empty duration -> allowed (does not block)
	makeFakeFfprobe(`echo '{"format":{"duration":""}}'`)
	err = validateDownloadedStreamAudio(ctx, dummyFile, 180)
	if err != nil {
		t.Fatalf("expected nil for empty duration, got: %v", err)
	}

	// 3. Non-numeric duration "N/A" -> allowed (does not block)
	makeFakeFfprobe(`echo '{"format":{"duration":"N/A"}}'`)
	err = validateDownloadedStreamAudio(ctx, dummyFile, 180)
	if err != nil {
		t.Fatalf("expected nil for non-numeric duration, got: %v", err)
	}

	// 4. NaN duration -> allowed (does not block)
	makeFakeFfprobe(`echo '{"format":{"duration":"NaN"}}'`)
	err = validateDownloadedStreamAudio(ctx, dummyFile, 180)
	if err != nil {
		t.Fatalf("expected nil for NaN duration, got: %v", err)
	}

	// 5. Inf / -Inf duration -> allowed (does not block)
	makeFakeFfprobe(`echo '{"format":{"duration":"Inf"}}'`)
	err = validateDownloadedStreamAudio(ctx, dummyFile, 180)
	if err != nil {
		t.Fatalf("expected nil for Inf duration, got: %v", err)
	}

	makeFakeFfprobe(`echo '{"format":{"duration":"-Inf"}}'`)
	err = validateDownloadedStreamAudio(ctx, dummyFile, 180)
	if err != nil {
		t.Fatalf("expected nil for -Inf duration, got: %v", err)
	}

	// 6. Non-positive duration (0.0 or -5.0) -> allowed (does not block)
	makeFakeFfprobe(`echo '{"format":{"duration":"0.0"}}'`)
	err = validateDownloadedStreamAudio(ctx, dummyFile, 180)
	if err != nil {
		t.Fatalf("expected nil for 0.0 duration, got: %v", err)
	}
}
