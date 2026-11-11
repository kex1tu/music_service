package downloader

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestClassifyAudioQuality(t *testing.T) {
	tests := []struct {
		name         string
		codec        string
		bitrate      int
		cutoffHz     int
		expectedTier string
	}{
		{
			name:         "Lossless FLAC",
			codec:        "flac",
			bitrate:      900000,
			cutoffHz:     22000,
			expectedTier: "lossless",
		},
		{
			name:         "Lossless ALAC",
			codec:        "alac",
			bitrate:      800000,
			cutoffHz:     22050,
			expectedTier: "lossless",
		},
		{
			name:         "Lossless full spectrum high bitrate",
			codec:        "wav",
			bitrate:      1411000,
			cutoffHz:     22000,
			expectedTier: "lossless",
		},
		{
			name:         "True 320k high cutoff",
			codec:        "mp3",
			bitrate:      320000,
			cutoffHz:     20500,
			expectedTier: "true_320k",
		},
		{
			name:         "High Quality Opus (SoundCloud HQ)",
			codec:        "opus",
			bitrate:      160000,
			cutoffHz:     18500,
			expectedTier: "hq_opus",
		},
		{
			name:         "High Quality AAC",
			codec:        "aac",
			bitrate:      256000,
			cutoffHz:     18000,
			expectedTier: "hq_opus",
		},
		{
			name:         "Upscaled 128k (YouTube standard cut at 15.5kHz)",
			codec:        "aac",
			bitrate:      128000,
			cutoffHz:     15500,
			expectedTier: "upscaled_128k",
		},
		{
			name:         "Low bitrate MP3 (128k cut at 16kHz)",
			codec:        "mp3",
			bitrate:      128000,
			cutoffHz:     16000,
			expectedTier: "upscaled_128k",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := classifyAudioQuality(tc.codec, tc.bitrate, tc.cutoffHz)
			if res == nil {
				t.Fatalf("expected non-nil telemetry")
			}
			if res.QualityTier != tc.expectedTier {
				t.Errorf("expected tier %q, got %q (cutoff: %d, bitrate: %d)", tc.expectedTier, res.QualityTier, tc.cutoffHz, tc.bitrate)
			}
			if res.CutoffHz != tc.cutoffHz {
				t.Errorf("expected cutoff %d, got %d", tc.cutoffHz, res.CutoffHz)
			}
			if res.SourceBitrate != tc.bitrate {
				t.Errorf("expected bitrate %d, got %d", tc.bitrate, res.SourceBitrate)
			}
			if res.Description == "" {
				t.Errorf("expected non-empty description")
			}
		})
	}
}

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

	svc := &Service{}
	ctx := context.Background()

	// 1. Success case
	err := svc.transcodeToMP3(ctx, inputWav, outputMp3)
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
	err = svc.transcodeToMP3(cancelledCtx, inputWav, outputCancelled)
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

	svc := &Service{client: oversizedCLServer.Client()}
	_, err := svc.downloadCover(context.Background(), oversizedCLServer.URL+"/huge.jpg")
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

	_, streamErr := svc.downloadCover(context.Background(), streamingServer.URL+"/stream.jpg")
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

	authSvc := &Service{client: authCheckServer.Client()}
	_, _ = authSvc.downloadCover(context.Background(), authCheckServer.URL+"/art.jpg")
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

	svc := &Service{client: mockServer.Client()}

	// URL в стиле Яндекс Музыки (путь get-music-content)
	yandexCoverURL := mockServer.URL + "/get-music-content/12345/1000x1000"

	data, err := svc.downloadCover(context.Background(), yandexCoverURL)
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

	svcBothFail := &Service{client: bothFailServer.Client()}
	failURL := bothFailServer.URL + "/get-music-content/999/1000x1000"
	_, failErr := svcBothFail.downloadCover(context.Background(), failURL)
	if failErr == nil || !strings.Contains(failErr.Error(), "failed to download original") || !strings.Contains(failErr.Error(), "fallback 1000x1000") {
		t.Errorf("expected combined error for both links failure, got: %v", failErr)
	}

	// Сценарий отмены контекста: не должен пытаться делать fallback при отмене
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel() // Отменяем сразу
	_, cancelErr := svc.downloadCover(cancelCtx, yandexCoverURL)
	if cancelErr == nil || !errors.Is(cancelErr, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", cancelErr)
	}
}

func TestProbeAudioStream_RealFile(t *testing.T) {
	tmpDir := t.TempDir()
	inputWav := filepath.Join(tmpDir, "test_probe.wav")

	// Generate a 0.5s wav using ffmpeg
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=1000:duration=0.5", inputWav)
	if err := cmd.Run(); err != nil {
		t.Skip("ffmpeg not available, skipping")
	}

	codec, bitrate, sampleRate, err := probeAudioStream(context.Background(), inputWav)
	if err != nil {
		t.Fatalf("unexpected probe error: %v", err)
	}
	if codec == "" || codec == "unknown" {
		t.Errorf("expected detected codec, got %q", codec)
	}
	if sampleRate <= 0 {
		t.Errorf("expected valid sampleRate, got %d", sampleRate)
	}
	_ = bitrate

	// Test non-existent file
	_, _, _, err = probeAudioStream(context.Background(), filepath.Join(tmpDir, "non_existent.wav"))
	if err == nil {
		t.Errorf("expected error for non-existent file, got nil")
	}
}

func TestMeasureSpectralCutoff_RealFile(t *testing.T) {
	tmpDir := t.TempDir()
	inputWav := filepath.Join(tmpDir, "test_cutoff.wav")

	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=1000:duration=0.5", inputWav)
	if err := cmd.Run(); err != nil {
		t.Skip("ffmpeg not available, skipping")
	}

	cutoff, err := measureSpectralCutoff(context.Background(), inputWav)
	if err != nil {
		t.Logf("measureSpectralCutoff returned error (expected on very short files): %v", err)
	} else {
		t.Logf("measured cutoff: %d Hz", cutoff)
	}
}

func TestAnalyzeAudioQuality_RealFile(t *testing.T) {
	tmpDir := t.TempDir()
	inputWav := filepath.Join(tmpDir, "test_quality.wav")

	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=1000:duration=0.5", inputWav)
	if err := cmd.Run(); err != nil {
		t.Skip("ffmpeg not available, skipping")
	}

	svc := &Service{}
	telemetry := svc.analyzeAudioQuality(context.Background(), inputWav)
	if telemetry == nil {
		t.Fatalf("expected non-nil telemetry")
	}
	if telemetry.SourceCodec == "" {
		t.Errorf("expected non-empty SourceCodec")
	}
}

func TestFindDownloadedFile(t *testing.T) {
	tmpDir := t.TempDir()
	svc := &Service{}

	// 1. Success case: matching file exists
	targetFile := filepath.Join(tmpDir, "track_01.opus")
	_ = os.WriteFile(targetFile, []byte("audio-bytes"), 0644)

	tmpl := filepath.Join(tmpDir, "track_01.%(ext)s")
	found, err := svc.findDownloadedFile(tmpl)
	if err != nil {
		t.Fatalf("unexpected error finding file: %v", err)
	}
	if found != targetFile {
		t.Errorf("expected %s, got %s", targetFile, found)
	}

	// 2. Error case: no matching file
	missingTmpl := filepath.Join(tmpDir, "track_99.%(ext)s")
	_, err = svc.findDownloadedFile(missingTmpl)
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
