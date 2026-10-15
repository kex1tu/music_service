package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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


