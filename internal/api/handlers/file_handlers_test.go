package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/id3"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/scanner"
)

func setupTestFileHandler(t *testing.T) (*FileHandler, *sandbox.PathSandbox, *db.DB, string) {
	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	tmpDir := filepath.Join(tempDir, "tmp")
	_ = os.MkdirAll(musicDir, 0755)
	_ = os.MkdirAll(tmpDir, 0755)

	musicSb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}
	tmpSb, err := sandbox.NewPathSandbox(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	lyrSvc := lyrics.NewService(musicSb)
	orgSvc := organizer.NewService(musicSb, tmpSb)
	scannerSvc := scanner.NewScanner(musicSb, database, nil)
	handler := NewFileHandler(musicSb, tmpSb, database, nil, orgSvc, nil, lyrSvc, scannerSvc)

	return handler, musicSb, database, musicDir
}

func TestFileHandler_FixAllDuplicates(t *testing.T) {
	h, _, database, musicDir := setupTestFileHandler(t)
	defer database.Close()

	albumDir := filepath.Join(musicDir, "TestAlbum")
	_ = os.MkdirAll(albumDir, 0755)

	// Create original track and a duplicate track
	f1 := filepath.Join(albumDir, "01. Song.mp3")
	f2 := filepath.Join(albumDir, "01. Song (1).mp3")
	_ = os.WriteFile(f1, []byte("good mp3"), 0644)
	_ = os.WriteFile(f2, []byte("duplicate mp3"), 0644)

	// Insert artist first
	aRes, err := database.Conn().Exec("INSERT INTO artists (name, clean_name) VALUES ('TestArtist', 'testartist')")
	if err != nil {
		t.Fatal(err)
	}
	artistID, _ := aRes.LastInsertId()

	// Insert album
	res, err := database.Conn().Exec("INSERT INTO albums (title, clean_title, artist_id, folder_path) VALUES ('TestAlbum', 'testalbum', ?, 'TestAlbum')", artistID)
	if err != nil {
		t.Fatal(err)
	}
	albumID, _ := res.LastInsertId()

	// Insert tracks into database
	rel1 := "TestAlbum/01. Song.mp3"
	rel2 := "TestAlbum/01. Song (1).mp3"
	_, _ = database.Conn().Exec(`
		INSERT INTO tracks (album_id, artist_id, title, clean_title, file_path, file_size, bitrate, mtime)
		VALUES (?, ?, 'Song', 'song', ?, 1000, 320000, 100.0)
	`, albumID, artistID, rel1)
	_, _ = database.Conn().Exec(`
		INSERT INTO tracks (album_id, artist_id, title, clean_title, file_path, file_size, bitrate, mtime)
		VALUES (?, ?, 'Song', 'song', ?, 900, 128000, 100.0)
	`, albumID, artistID, rel2)

	req := httptest.NewRequest(http.MethodPost, "/files/audit/duplicates/fix-all", nil)
	w := httptest.NewRecorder()

	h.FixAllDuplicates(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp struct {
		Success      bool     `json:"success"`
		DeletedCount int      `json:"deleted_count"`
		DeletedFiles []string `json:"deleted_files"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if !resp.Success {
		t.Errorf("expected success true, got false")
	}
	if resp.DeletedCount != 1 {
		t.Errorf("expected deleted_count 1, got %d", resp.DeletedCount)
	}
	if len(resp.DeletedFiles) != 1 || resp.DeletedFiles[0] != rel2 {
		t.Errorf("expected deleted file %s, got %v", rel2, resp.DeletedFiles)
	}

	// Verify duplicate file is gone from disk
	if _, err := os.Stat(f2); !os.IsNotExist(err) {
		t.Errorf("duplicate file still exists on disk")
	}
	// Verify original file still exists
	if _, err := os.Stat(f1); err != nil {
		t.Errorf("original file was unexpectedly deleted: %v", err)
	}
}

func TestFileHandler_RetagGenres(t *testing.T) {
	h, _, database, musicDir := setupTestFileHandler(t)
	defer database.Close()

	albumDir := filepath.Join(musicDir, "HipHopAlbum")
	_ = os.MkdirAll(albumDir, 0755)

	mp3File := filepath.Join(albumDir, "01. Track.mp3")
	_ = os.WriteFile(mp3File, []byte("mp3data"), 0644)
	tags := id3.TrackTags{
		Title:  "Track",
		Artist: "Artist",
		Album:  "HipHopAlbum",
		Genre:  "хип-хоп",
	}
	_ = id3.WriteTags(mp3File, tags, nil)

	relPath := "HipHopAlbum/01. Track.mp3"
	aRes, err := database.Conn().Exec("INSERT INTO artists (name, clean_name) VALUES ('Artist', 'artist')")
	if err != nil {
		t.Fatal(err)
	}
	artID, _ := aRes.LastInsertId()

	res, err := database.Conn().Exec("INSERT INTO albums (title, clean_title, artist_id, folder_path) VALUES ('HipHopAlbum', 'hiphopalbum', ?, 'HipHopAlbum')", artID)
	if err != nil {
		t.Fatal(err)
	}
	albID, _ := res.LastInsertId()

	tRes, err := database.Conn().Exec(`
		INSERT INTO tracks (album_id, artist_id, title, clean_title, file_path, file_size, mtime)
		VALUES (?, ?, 'Track', 'track', ?, 500, 100.0)
	`, albID, artID, relPath)
	if err != nil {
		t.Fatal(err)
	}
	trackID, _ := tRes.LastInsertId()

	gRes, _ := database.Conn().Exec("INSERT INTO genres (name, is_valid_en, canonical_en_name) VALUES ('хип-хоп', 0, 'Hip-Hop')")
	genreID, _ := gRes.LastInsertId()

	_, _ = database.Conn().Exec("INSERT INTO track_genres (track_id, genre_id) VALUES (?, ?)", trackID, genreID)

	req := httptest.NewRequest(http.MethodPost, "/files/audit/retag-genres", nil)
	w := httptest.NewRecorder()

	h.RetagGenres(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp struct {
		Success      bool `json:"success"`
		UpdatedCount int  `json:"updated_count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if !resp.Success {
		t.Errorf("expected success true")
	}
	if resp.UpdatedCount != 1 {
		t.Errorf("expected updated_count 1, got %d", resp.UpdatedCount)
	}

	// Verify tag in MP3 file was updated to canonical Hip-Hop
	readBack, err := id3.ReadTags(mp3File, relPath)
	if err != nil {
		t.Fatal(err)
	}
	if readBack.Genre != "Hip-Hop" {
		t.Errorf("expected genre 'Hip-Hop', got %q", readBack.Genre)
	}
}

func TestFileHandler_BatchLyricsProgress_Empty(t *testing.T) {
	h, _, database, _ := setupTestFileHandler(t)
	defer database.Close()

	req := httptest.NewRequest(http.MethodGet, "/files/audit/missing-lyrics/progress", nil)
	w := httptest.NewRecorder()

	h.BatchLyricsProgress(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp struct {
		IsRunning bool `json:"is_running"`
		Total     int  `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.IsRunning {
		t.Errorf("expected is_running false")
	}
}

func TestFileHandler_RefreshAudit_DetectsRenamedFolder(t *testing.T) {
	h, _, database, musicDir := setupTestFileHandler(t)
	defer database.Close()

	// Create a track in a non-canonical folder
	wrongDir := filepath.Join(musicDir, "Wrong Album Folder")
	_ = os.MkdirAll(wrongDir, 0755)
	mp3Path := filepath.Join(wrongDir, "01. Hit Song.mp3")
	_ = os.WriteFile(mp3Path, []byte("audio payload data"), 0644)

	tags := id3.TrackTags{
		Title:       "Hit Song",
		Artist:      "Cool Artist",
		Album:       "Canonical Album",
		TrackNumber: 1,
	}
	if err := id3.WriteTags(mp3Path, tags, nil); err != nil {
		t.Fatal(err)
	}

	// Trigger RefreshAudit
	req := httptest.NewRequest(http.MethodPost, "/files/audit/refresh", nil)
	w := httptest.NewRecorder()
	h.RefreshAudit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var summary map[string]any
	if err := json.NewDecoder(w.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}

	if summary["invalid_paths_count"] != float64(1) {
		t.Errorf("expected invalid_paths_count 1, got %v", summary["invalid_paths_count"])
	}

	// Query InvalidPaths
	reqInv := httptest.NewRequest(http.MethodGet, "/files/audit/invalid-paths", nil)
	wInv := httptest.NewRecorder()
	h.InvalidPaths(wInv, reqInv)

	var invList []db.InvalidPathRecord
	if err := json.NewDecoder(wInv.Body).Decode(&invList); err != nil {
		t.Fatal(err)
	}
	if len(invList) != 1 {
		t.Fatalf("expected 1 invalid path record, got %d", len(invList))
	}
	if invList[0].FilePath != "Wrong Album Folder/01. Hit Song.mp3" {
		t.Errorf("expected FilePath 'Wrong Album Folder/01. Hit Song.mp3', got %q", invList[0].FilePath)
	}
	if invList[0].SuggestedPath != "Canonical Album - Cool Artist/01. Hit Song.mp3" {
		t.Errorf("expected SuggestedPath 'Canonical Album - Cool Artist/01. Hit Song.mp3', got %q", invList[0].SuggestedPath)
	}

	// FixPath
	body := strings.NewReader(`{"path":"Wrong Album Folder/01. Hit Song.mp3"}`)
	reqFix := httptest.NewRequest(http.MethodPost, "/files/audit/fix-path", body)
	wFix := httptest.NewRecorder()
	h.FixPath(wFix, reqFix)

	if wFix.Code != http.StatusOK {
		t.Fatalf("expected status 200 from FixPath, got %d", wFix.Code)
	}

	// Verify moved on disk
	newDiskPath := filepath.Join(musicDir, "Canonical Album - Cool Artist", "01. Hit Song.mp3")
	if _, err := os.Stat(newDiskPath); os.IsNotExist(err) {
		t.Fatalf("file not found at canonical location: %s", newDiskPath)
	}
	if _, err := os.Stat(mp3Path); !os.IsNotExist(err) {
		t.Errorf("file still exists at old location: %s", mp3Path)
	}

	// Verify database no longer has invalid paths
	remaining, err := database.GetInvalidPaths(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected 0 invalid paths after FixPath, got %d", len(remaining))
	}
}
