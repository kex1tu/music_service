package lyrics

import (
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/core/sandbox"
)

func TestLyricsService_SaveAndGet(t *testing.T) {
	tempDir := t.TempDir()
	sb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewService(sb)

	// Create a dummy mp3 file
	mp3Path := filepath.Join(tempDir, "01. Test.mp3")
	if err := os.WriteFile(mp3Path, []byte("fake mp3 data"), 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Initial state: no lyrics
	resp, err := svc.GetLyrics("01. Test.mp3")
	if err != nil {
		t.Fatalf("GetLyrics failed: %v", err)
	}
	if resp.Kind != "none" {
		t.Errorf("expected kind none, got %s", resp.Kind)
	}

	// 2. Save synced lyrics (.lrc)
	syncedContent := "[00:12.34]Hello world\n[00:15.67]Second line"
	if err := svc.SaveLyrics("01. Test.mp3", syncedContent, "auto"); err != nil {
		t.Fatalf("SaveLyrics failed: %v", err)
	}

	resp2, err := svc.GetLyrics("01. Test.mp3")
	if err != nil {
		t.Fatalf("GetLyrics failed: %v", err)
	}
	if resp2.Kind != "lrc" || !resp2.Synced {
		t.Errorf("expected kind lrc and synced=true, got kind=%s synced=%v", resp2.Kind, resp2.Synced)
	}

	// Check that file is on disk
	lrcPath := filepath.Join(tempDir, "01. Test.lrc")
	if _, err := os.Stat(lrcPath); err != nil {
		t.Errorf("expected .lrc file on disk")
	}

	// 3. Delete lyrics
	deleted, err := svc.DeleteLyrics("01. Test.mp3")
	if err != nil || !deleted {
		t.Errorf("expected deleted=true, got %v, err: %v", deleted, err)
	}
	if _, err := os.Stat(lrcPath); !os.IsNotExist(err) {
		t.Errorf("expected .lrc file to be deleted")
	}
}
