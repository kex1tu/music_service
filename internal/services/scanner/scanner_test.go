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

	s := NewScanner(sb, database, nil)
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

func TestScanner_GenreAndCompleteness(t *testing.T) {
	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	_ = os.MkdirAll(filepath.Join(musicDir, "RockAlbum"), 0755)

	sb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(tempDir, "test2.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mp3Path := filepath.Join(musicDir, "RockAlbum", "01. Song.mp3")
	_ = os.WriteFile(mp3Path, []byte("mp3data"), 0644)
	tags := id3.TrackTags{
		Title:       "Song",
		Artist:      "Artist",
		Album:       "RockAlbum",
		TrackNumber: 1,
		TrackTotal:  10,
		Genre:       "русский рок",
	}
	_ = id3.WriteTags(mp3Path, tags, nil)

	s := NewScanner(sb, database, nil)
	_, err = s.ScanAll(context.Background())
	if err != nil {
		t.Fatalf("ScanAll failed: %v", err)
	}

	// Verify genre was registered with canonical English name
	var genreName, canonicalName string
	var isValidEn bool
	err = database.Conn().QueryRow("SELECT name, is_valid_en, canonical_en_name FROM genres WHERE name = 'русский рок'").
		Scan(&genreName, &isValidEn, &canonicalName)
	if err != nil {
		t.Fatalf("genre not found in db: %v", err)
	}
	if isValidEn {
		t.Errorf("expected is_valid_en = false for Russian genre, got true")
	}
	if canonicalName != "Russian Rock" {
		t.Errorf("expected canonical_en_name 'Russian Rock', got %q", canonicalName)
	}

	// Verify audit_issues contains non_en_genre issue
	var issueCount int
	err = database.Conn().QueryRow("SELECT COUNT(*) FROM audit_issues WHERE issue_type = 'non_en_genre'").Scan(&issueCount)
	if err != nil || issueCount != 1 {
		t.Errorf("expected 1 non_en_genre issue, got %d, err: %v", issueCount, err)
	}

	// Verify album total_tracks and completeness
	var totalTracks, isComplete int
	err = database.Conn().QueryRow("SELECT total_tracks, is_complete FROM albums WHERE title = 'RockAlbum'").
		Scan(&totalTracks, &isComplete)
	if err != nil {
		t.Fatalf("album not found: %v", err)
	}
	if totalTracks != 10 {
		t.Errorf("expected total_tracks = 10, got %d", totalTracks)
	}
	if isComplete != 0 {
		t.Errorf("expected is_complete = 0 (1/10 tracks), got %d", isComplete)
	}
}

