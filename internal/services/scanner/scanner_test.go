package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/id3"
)

func TestScanner_ScanAll(t *testing.T) {
	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	_ = os.MkdirAll(filepath.Join(musicDir, "TestAlbum"), 0755)

	sb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Create a dummy MP3 with ID3v2.4 tags
	mp3Path := filepath.Join(musicDir, "TestAlbum", "01. TestSong.mp3")
	if err := os.WriteFile(mp3Path, []byte("fake mp3"), 0644); err != nil {
		t.Fatal(err)
	}

	tags := id3.TrackTags{
		Title:       "TestSong",
		Artist:      "TestArtist",
		Album:       "TestAlbum",
		TrackNumber: 1,
	}
	_ = id3.WriteTags(mp3Path, tags, nil)

	s := NewScanner(sb, database)
	scanned, err := s.ScanAll(context.Background())
	if err != nil {
		t.Fatalf("ScanAll failed: %v", err)
	}
	if scanned != 1 {
		t.Errorf("expected 1 scanned file, got %d", scanned)
	}

	// Verify SQLite has the track
	var count int
	err = database.Conn().QueryRow("SELECT COUNT(*) FROM tracks WHERE title = 'TestSong'").Scan(&count)
	if err != nil || count != 1 {
		t.Errorf("expected track in SQLite, got count %d, err: %v", count, err)
	}
}
