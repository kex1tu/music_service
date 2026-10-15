package downloader

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"music-manager/internal/core/sandbox"
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
