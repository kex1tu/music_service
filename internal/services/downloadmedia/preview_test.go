package downloadmedia

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateDownloadedStreamAudio_FeatureSnippet(t *testing.T) {
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

func TestValidateDownloadedStreamAudio_FeatureProbeErrorsAndContext(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Missing file -> probe error
	missing := filepath.Join(tmpDir, "non_existent.wav")
	err := validateDownloadedStreamAudio(context.Background(), missing, 180)
	if err == nil || !strings.Contains(err.Error(), "ffprobe probe failed") {
		t.Fatalf("expected ffprobe probe failed error for missing file, got %v", err)
	}

	// 2. Corrupt / empty file -> probe error
	corrupt := filepath.Join(tmpDir, "corrupt.wav")
	_ = os.WriteFile(corrupt, []byte("garbage data"), 0644)
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
	time.Sleep(2 * time.Millisecond)
	err = validateDownloadedStreamAudio(timeoutCtx, validFile, 180)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected deadline exceeded or canceled error, got: %v", err)
	}
}
