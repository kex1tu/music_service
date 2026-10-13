package organizer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestNormalizeNavidromeLibrary_SingleTrackPromotion(t *testing.T) {
	tempDir := t.TempDir()
	sb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatalf("failed to create sandbox: %v", err)
	}

	service := NewService(sb, sb)

	singleDir := filepath.Join(tempDir, "Single - Track")
	_ = os.MkdirAll(singleDir, 0755)

	trackPath := filepath.Join(singleDir, "01. Song.mp3")
	dummyAudio := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 500)...)
	_ = os.WriteFile(trackPath, dummyAudio, 0644)

	// Single track downloaded previously with Album="Single", feat in title, and empty AlbumArtist
	initialTags := id3.TrackTags{
		Title:       "Great Song (feat. Collaborator)",
		Artist:      "Main Artist",
		Album:       "Single",
		AlbumArtist: "",
		TrackNumber: 1,
	}
	if err := id3.WriteTags(trackPath, initialTags, nil); err != nil {
		t.Fatalf("failed to write initial tags: %v", err)
	}

	normalizedCount := 0
	report, err := service.NormalizeNavidromeLibrary(context.Background(), func(rel string) {
		normalizedCount++
	})
	if err != nil {
		t.Fatalf("NormalizeNavidromeLibrary error: %v", err)
	}

	if report.SinglesFixed != 1 {
		t.Errorf("expected SinglesFixed=1, got %d", report.SinglesFixed)
	}
	if report.TagsNormalized != 1 {
		t.Errorf("expected TagsNormalized=1, got %d", report.TagsNormalized)
	}
	if normalizedCount != 1 {
		t.Errorf("expected callback called 1 time, got %d", normalizedCount)
	}

	rel, _ := sb.ToRelative(trackPath)
	updatedTags, err := id3.ReadTags(trackPath, rel)
	if err != nil {
		t.Fatalf("failed to read updated tags: %v", err)
	}

	if updatedTags.Title != "Great Song" {
		t.Errorf("expected Title 'Great Song', got '%s'", updatedTags.Title)
	}
	if updatedTags.Album != "Great Song" {
		t.Errorf("expected Album 'Great Song', got '%s'", updatedTags.Album)
	}
	if !strings.Contains(updatedTags.Artist, "Collaborator") {
		t.Errorf("expected Artist to contain Collaborator, got '%s'", updatedTags.Artist)
	}
	if !strings.Contains(updatedTags.AlbumArtist, "Collaborator") || !strings.Contains(updatedTags.AlbumArtist, "Main Artist") {
		t.Errorf("expected AlbumArtist to have all artists, got '%s'", updatedTags.AlbumArtist)
	}
}

func TestNormalizeNavidromeLibrary_MultiTrackUntouched(t *testing.T) {
	tempDir := t.TempDir()
	sb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatalf("failed to create sandbox: %v", err)
	}

	service := NewService(sb, sb)

	albumDir := filepath.Join(tempDir, "Album - Artist")
	_ = os.MkdirAll(albumDir, 0755)

	trackPath := filepath.Join(albumDir, "01. Song.mp3")
	dummyAudio := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 500)...)
	_ = os.WriteFile(trackPath, dummyAudio, 0644)

	// Multi-track album track
	initialTags := id3.TrackTags{
		Title:       "Song 1",
		Artist:      "Rock Band",
		Album:       "Masterpiece Album",
		AlbumArtist: "Rock Band",
		TrackNumber: 1,
	}
	if err := id3.WriteTags(trackPath, initialTags, nil); err != nil {
		t.Fatalf("failed to write initial tags: %v", err)
	}

	report, err := service.NormalizeNavidromeLibrary(context.Background(), nil)
	if err != nil {
		t.Fatalf("NormalizeNavidromeLibrary error: %v", err)
	}

	if report.SinglesFixed != 0 {
		t.Errorf("expected SinglesFixed=0, got %d", report.SinglesFixed)
	}
	if report.TagsNormalized != 0 {
		t.Errorf("expected TagsNormalized=0, got %d", report.TagsNormalized)
	}

	rel, _ := sb.ToRelative(trackPath)
	tags, err := id3.ReadTags(trackPath, rel)
	if err != nil {
		t.Fatalf("failed to read tags: %v", err)
	}

	if tags.Album != "Masterpiece Album" {
		t.Errorf("expected Album 'Masterpiece Album', got '%s'", tags.Album)
	}
	if tags.AlbumArtist != "Rock Band" {
		t.Errorf("expected AlbumArtist 'Rock Band', got '%s'", tags.AlbumArtist)
	}
}
