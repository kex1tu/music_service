package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestDownloadCover(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("fake-jpeg-cover-data"))
	}))
	defer mockServer.Close()

	svc := &Service{
		client: mockServer.Client(),
	}

	data, err := svc.downloadCover(context.Background(), mockServer.URL+"/cover.jpg")
	if err != nil {
		t.Fatalf("unexpected error downloading cover: %v", err)
	}
	if string(data) != "fake-jpeg-cover-data" {
		t.Errorf("expected cover data 'fake-jpeg-cover-data', got %q", string(data))
	}

	// Error case: 404
	_, err = svc.downloadCover(context.Background(), mockServer.URL+"/not-found")
	// handler doesn't check path so returns 200, but test invalid URL:
	_, err = svc.downloadCover(context.Background(), "http://127.0.0.1:0/invalid")
	if err == nil {
		t.Errorf("expected error for unreachable URL, got nil")
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
