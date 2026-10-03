package organizer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
)

func TestFixLibraryPermissions(t *testing.T) {
	tempDir := t.TempDir()

	subDir := filepath.Join(tempDir, "Album - Artist")
	if err := os.MkdirAll(subDir, 0700); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	testFile := filepath.Join(subDir, "01. Song.mp3")
	if err := os.WriteFile(testFile, []byte("fake mp3 data"), 0600); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	dirsFixed, filesFixed, err := FixLibraryPermissions(tempDir)
	if err != nil {
		t.Fatalf("FixLibraryPermissions returned error: %v", err)
	}

	if dirsFixed == 0 && filesFixed == 0 {
		t.Errorf("expected fixes, got dirsFixed=%d, filesFixed=%d", dirsFixed, filesFixed)
	}

	dirInfo, err := os.Stat(subDir)
	if err != nil || dirInfo.Mode().Perm() != 0755 {
		t.Errorf("expected dir perm 0755, got %v", dirInfo.Mode().Perm())
	}

	fileInfo, err := os.Stat(testFile)
	if err != nil || fileInfo.Mode().Perm() != 0644 {
		t.Errorf("expected file perm 0644, got %v", fileInfo.Mode().Perm())
	}
}

func TestNormalizeNavidromeLibrary(t *testing.T) {
	tempDir := t.TempDir()
	sb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatalf("failed to create sandbox: %v", err)
	}

	service := NewService(sb, sb)

	albumDir := filepath.Join(tempDir, "Test Album")
	_ = os.MkdirAll(albumDir, 0755)

	trackPath := filepath.Join(albumDir, "01. Track.mp3")
	dummyAudio := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 500)...)
	_ = os.WriteFile(trackPath, dummyAudio, 0600)

	// Write tag with literal backslash in artist
	tags := id3.TrackTags{
		Title:       "Track",
		Artist:      "Artist 1\\\\Artist 2",
		Album:       "Test Album",
		TrackNumber: 1,
	}
	_ = id3.WriteTags(trackPath, tags, nil)

	normalizedCount := 0
	report, err := service.NormalizeNavidromeLibrary(context.Background(), func(rel string) {
		normalizedCount++
	})
	if err != nil {
		t.Fatalf("NormalizeNavidromeLibrary error: %v", err)
	}

	if report.TotalScanned == 0 {
		t.Errorf("expected files scanned > 0")
	}

	// Verify file permissions 0644
	st, _ := os.Stat(trackPath)
	if st.Mode().Perm() != 0644 {
		t.Errorf("expected perm 0644, got %v", st.Mode().Perm())
	}
}
