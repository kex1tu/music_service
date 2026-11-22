package downloader

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
)

func TestSwitchTrackVersion_Validation(t *testing.T) {
	tmpDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(tmpDir)

	svc := NewService(context.Background(), NewJobManager(nil), nil, tmpSb, musicSb, "", 1)
	defer svc.Close()

	ctx := context.Background()

	// 1. Empty URL
	_, err := svc.SwitchTrackVersion(ctx, "track-1", "")
	if err == nil || !strings.Contains(err.Error(), "cannot be empty") {
		t.Errorf("expected empty URL error, got %v", err)
	}

	// 2. Dash leading URL
	_, err = svc.SwitchTrackVersion(ctx, "track-1", "--inject")
	if err == nil || !strings.Contains(err.Error(), "cannot start with '-'") {
		t.Errorf("expected '-' prefix error, got %v", err)
	}

	// 3. Invalid scheme
	_, err = svc.SwitchTrackVersion(ctx, "track-1", "ftp://example.com/audio")
	if err == nil || !strings.Contains(err.Error(), "invalid url scheme") {
		t.Errorf("expected scheme error, got %v", err)
	}

	// 4. Disallowed domain
	_, err = svc.SwitchTrackVersion(ctx, "track-1", "https://malicious.org/track")
	if err == nil || !strings.Contains(err.Error(), "whitelist") {
		t.Errorf("expected whitelist error, got %v", err)
	}

	// 5. Valid URL but track not found in any job or DB
	_, err = svc.SwitchTrackVersion(ctx, "non-existent-track", "https://www.youtube.com/watch?v=dQw4w9WgXcQ")
	if err == nil {
		t.Errorf("expected error for non-existent track, got nil")
	}
}

func TestCopyFile(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "source.txt")
	dst := filepath.Join(tmpDir, "nested", "dest.txt")

	content := "test-copy-file-content"
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Success copy
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("unexpected copyFile error: %v", err)
	}
	read, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("failed to read copied file: %v", err)
	}
	if string(read) != content {
		t.Errorf("expected %q, got %q", content, string(read))
	}

	// 2. Error copying from non-existent file
	if err := copyFile(filepath.Join(tmpDir, "missing.txt"), dst); err == nil {
		t.Errorf("expected error copying non-existent file, got nil")
	}
}

func TestMeasureAudioDuration(t *testing.T) {
	tmpDir := t.TempDir()
	wavFile := filepath.Join(tmpDir, "one_sec.wav")

	// Generate 1-second wav
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=1000:duration=1.0", wavFile)
	if err := cmd.Run(); err != nil {
		t.Skip("ffmpeg not available, skipping")
	}

	dur, err := measureAudioDuration(wavFile)
	if err != nil {
		t.Fatalf("unexpected error measuring duration: %v", err)
	}
	if dur != 1 {
		t.Errorf("expected duration 1, got %d", dur)
	}

	// Error case: non-existent file
	_, err = measureAudioDuration(filepath.Join(tmpDir, "non_existent.wav"))
	if err == nil {
		t.Errorf("expected error measuring non-existent file, got nil")
	}
}

func TestSwitchTrackVersion_PreviewSnippetRegression(t *testing.T) {
	fakeDir := setupMockMediaBinaries(t)

	// Override ffprobe in fakeDir to return actual duration 30.0s (a short preview snippet)
	ffprobeScript := `#!/bin/sh
echo '{"format":{"duration":"30.0","bit_rate":"320000"},"streams":[{"codec_name":"mp3","bit_rate":"320000","sample_rate":"44100"}]}'
exit 0
`
	if err := os.WriteFile(filepath.Join(fakeDir, "ffprobe"), []byte(ffprobeScript), 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(t.TempDir(), "test_switch.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()

	// Candidate on SoundCloud has duration 30s (snippet!)
	candURL := "https://soundcloud.com/test-artist/snippet-candidate"

	tmpDir := t.TempDir()
	musicDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(musicDir)

	jobMgr := NewJobManager(database)
	svc := NewService(ctx, jobMgr, nil, tmpSb, musicSb, "", 1)
	defer svc.Close()

	// Track in job has expected duration 180s (> 60s)
	job := &DownloadJob{
		ID:     "switch-job-1",
		Status: StatusDownloading,
		URL:    "https://www.deezer.com/album/456",
		Tracks: []resolver.ResolvedTrack{
			{
				TrackID:        "track-long-1",
				Title:          "Long Song",
				Artist:         "Test Artist",
				Duration:       180, // Expected 180 seconds!
				AudioSourceURL: "https://soundcloud.com/test-artist/original",
				Status:         "waiting_source",
			},
		},
		TotalTracks: 1,
	}
	jobMgr.Add(job)

	// User switches version to candURL
	_, sErr := svc.SwitchTrackVersion(ctx, "track-long-1", candURL)
	if sErr == nil {
		t.Fatal("expected SwitchTrackVersion to fail because of preview snippet, got nil")
	}
	if !strings.Contains(sErr.Error(), "detected 30s preview snippet") {
		t.Fatalf("expected error containing 'detected 30s preview snippet', got: %v", sErr)
	}

	// Verify job track state after refusal:
	snap := job.Snapshot()
	if snap.Tracks[0].Status == "ready" {
		t.Fatalf("expected track NOT to be ready after preview failure, got status %q", snap.Tracks[0].Status)
	}
	if snap.Tracks[0].Status != "waiting_source" {
		t.Errorf("expected track status 'waiting_source', got %q", snap.Tracks[0].Status)
	}
	if snap.Tracks[0].AudioSourceURL != candURL {
		t.Errorf("expected AudioSourceURL to be preserved as %q, got %q", candURL, snap.Tracks[0].AudioSourceURL)
	}
	if snap.Tracks[0].VersionStatus != "manual" {
		t.Errorf("expected VersionStatus to be 'manual', got %q", snap.Tracks[0].VersionStatus)
	}
	if snap.Tracks[0].Error == nil || !strings.Contains(*snap.Tracks[0].Error, "detected 30s preview snippet") {
		t.Errorf("expected track.Error to contain snippet message, got: %v", snap.Tracks[0].Error)
	}

	// 2. Now test that switching to a full track (duration 180s) succeeds
	fullFFprobe := `#!/bin/sh
echo '{"format":{"duration":"180.0","bit_rate":"320000"},"streams":[{"codec_name":"mp3","bit_rate":"320000","sample_rate":"44100"}]}'
exit 0
`
	if err := os.WriteFile(filepath.Join(fakeDir, "ffprobe"), []byte(fullFFprobe), 0755); err != nil {
		t.Fatal(err)
	}

	fullURL := "https://soundcloud.com/test-artist/full-candidate"
	res, fErr := svc.SwitchTrackVersion(ctx, "track-long-1", fullURL)
	if fErr != nil {
		t.Fatalf("expected SwitchTrackVersion to succeed for full candidate, got: %v", fErr)
	}
	if res.Status != "manual" {
		t.Errorf("expected status 'manual', got %q", res.Status)
	}

	snapFull := job.Snapshot()
	if snapFull.Tracks[0].Status != "ready" {
		t.Errorf("expected track status 'ready' after full audio, got %q", snapFull.Tracks[0].Status)
	}
	if snapFull.Tracks[0].AudioSourceURL != fullURL {
		t.Errorf("expected AudioSourceURL to be %q, got %q", fullURL, snapFull.Tracks[0].AudioSourceURL)
	}
}
