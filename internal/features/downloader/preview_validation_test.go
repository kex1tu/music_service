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

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/services/resolver"
)

func TestFeaturesDownloader_ValidateDownloadedStreamAudio_Snippet(t *testing.T) {
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

func TestFeaturesDownloader_ValidateDownloadedStreamAudio_ProbeErrorsAndContext(t *testing.T) {
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

func TestFeaturesDownloader_ProcessSingleTrack_PreviewSnippetBlocked(t *testing.T) {
	fakeDir := setupFeatureMockMediaBinaries(t)

	// Override ffprobe in fakeDir to return actual duration 30.0s (a preview snippet)
	ffprobeScript := `#!/bin/sh
echo '{"format":{"duration":"30.0","bit_rate":"320000"},"streams":[{"codec_name":"mp3","bit_rate":"320000","sample_rate":"44100"}]}'
exit 0
`
	if err := os.WriteFile(filepath.Join(fakeDir, "ffprobe"), []byte(ffprobeScript), 0755); err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	musicDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(musicDir)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("fake-cover-bytes"))
	}))
	defer mockServer.Close()

	svc := NewService(ServiceConfig{
		TmpSandbox:   tmpSb,
		MusicSandbox: musicSb,
	})
	svc.client = mockServer.Client()

	job := &Job{
		ID:     "feat-preview-job",
		Status: StatusDownloading,
		Tracks: []resolver.ResolvedTrack{
			{
				TrackID:        "track-01",
				TrackNumber:    1,
				Title:          "Full Length Anthem",
				Artist:         "The Artist",
				Album:          "The Album",
				Duration:       210, // Expected 210 seconds!
				AudioSourceURL: "https://soundcloud.com/the-artist/snippet",
				Status:         "pending",
			},
		},
		TotalTracks: 1,
	}

	rawDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "raw"), true)
	convertedDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "converted"), true)
	_ = safefs.MkdirAll(rawDir, 0755)
	_ = safefs.MkdirAll(convertedDir, 0755)

	meta := &resolver.ReleaseMetadata{
		AlbumTitle:  "The Album",
		AlbumArtist: "The Artist",
		CoverURL:    mockServer.URL + "/cover.jpg",
	}

	// Process track: yt-dlp generates file, but ffprobe reports 30.0s for 210s expected
	ok := svc.processSingleTrack(
		context.Background(),
		job,
		meta,
		1,
		job.Tracks[0],
		rawDir,
		convertedDir,
		[]byte("fake-cover-bytes"),
		1,
	)

	// ok should be false because preview was detected
	if ok {
		t.Fatal("expected processSingleTrack to return false for 30s preview snippet, got true")
	}

	// Track status must be waiting_source
	snap := job.Snapshot()
	if snap.Tracks[0].Status != "waiting_source" {
		t.Errorf("expected track status 'waiting_source', got %q", snap.Tracks[0].Status)
	}
	if snap.Tracks[0].Error == nil || !strings.Contains(*snap.Tracks[0].Error, "detected 30s preview snippet") {
		t.Errorf("expected error containing 'detected 30s preview snippet', got: %v", snap.Tracks[0].Error)
	}

	// Converted MP3 must NOT exist
	matches, _ := safefs.Glob(filepath.Join(convertedDir, "*.mp3"))
	if len(matches) > 0 {
		t.Errorf("expected 0 converted MP3 files, found: %v", matches)
	}

	// Staging raw audio file must NOT remain
	rawMatches, _ := safefs.Glob(filepath.Join(rawDir, "*"))
	if len(rawMatches) > 0 {
		t.Errorf("expected raw directory to be clean after failure, found: %v", rawMatches)
	}
}
