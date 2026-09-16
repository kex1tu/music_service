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
	"music-manager/internal/services/downloader"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/scanner"
)

func setupTestFileHandler(t *testing.T) (*FileHandler, *sandbox.PathSandbox, *sandbox.PathSandbox, *db.DB, string, string) {
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
	jobMgr := downloader.NewJobManager(database)
	dlSvc := downloader.NewService(jobMgr, nil, tmpSb, musicSb, "", 1)
	scannerSvc := scanner.NewScanner(musicSb, database, nil)
	handler := NewFileHandler(musicSb, tmpSb, database, dlSvc, orgSvc, nil, lyrSvc, scannerSvc)

	return handler, musicSb, tmpSb, database, musicDir, tmpDir
}

func TestFileHandler_FixAllDuplicates(t *testing.T) {
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
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
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
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
	h, _, _, database, _, _ := setupTestFileHandler(t)
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
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
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

func TestFileHandler_IncompleteAlbums(t *testing.T) {
	h, _, _, database, _, _ := setupTestFileHandler(t)
	defer database.Close()

	aRes, err := database.Conn().Exec("INSERT INTO artists (name, clean_name) VALUES ('TestBand', 'testband')")
	if err != nil {
		t.Fatal(err)
	}
	artID, _ := aRes.LastInsertId()

	albRes, err := database.Conn().Exec(`
		INSERT INTO albums (title, clean_title, artist_id, folder_path, is_complete, present_tracks_count, expected_tracks_count, total_tracks, deezer_id)
		VALUES ('Greatest Hits', 'greatesthits', ?, 'TestBand/Greatest Hits', 0, 3, 5, 5, 'dz12345')
	`, artID)
	if err != nil {
		t.Fatal(err)
	}
	albID, _ := albRes.LastInsertId()

	// Insert missing tracks records
	_, _ = database.Conn().Exec("INSERT INTO missing_tracks (album_id, track_number, title, artist) VALUES (?, 2, 'Missing Track 2', 'TestBand')", albID)
	_, _ = database.Conn().Exec("INSERT INTO missing_tracks (album_id, track_number, title, artist) VALUES (?, 4, 'Missing Track 4', 'TestBand')", albID)

	req := httptest.NewRequest(http.MethodGet, "/files/audit/incomplete-albums", nil)
	w := httptest.NewRecorder()
	h.IncompleteAlbums(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Albums []struct {
			Path                string `json:"path"`
			Album               string `json:"album"`
			Artist              string `json:"artist"`
			PresentCount        int    `json:"present_count"`
			ExpectedCount       int    `json:"expected_count"`
			MissingTrackNumbers []int  `json:"missing_track_numbers"`
			IsCatalogMatched    bool   `json:"is_catalog_matched"`
			DeezerAlbumID       *string `json:"deezer_album_id"`
		} `json:"albums"`
		TotalIncomplete int `json:"total_incomplete_albums"`
	}

	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if len(resp.Albums) != 1 {
		t.Fatalf("expected 1 incomplete album, got %d", len(resp.Albums))
	}

	alb := resp.Albums[0]
	if alb.Album != "Greatest Hits" || alb.Artist != "TestBand" {
		t.Errorf("unexpected album data: %+v", alb)
	}
	if alb.PresentCount != 3 || alb.ExpectedCount != 5 {
		t.Errorf("expected 3/5 tracks, got %d/%d", alb.PresentCount, alb.ExpectedCount)
	}
	if len(alb.MissingTrackNumbers) != 2 || alb.MissingTrackNumbers[0] != 2 || alb.MissingTrackNumbers[1] != 4 {
		t.Errorf("expected missing tracks [2, 4], got %v", alb.MissingTrackNumbers)
	}
	if !alb.IsCatalogMatched || alb.DeezerAlbumID == nil || *alb.DeezerAlbumID != "dz12345" {
		t.Errorf("expected catalog matched with deezer ID dz12345, got %+v", alb)
	}
}

func TestFileHandler_DownloadMissing(t *testing.T) {
	h, _, _, database, _, _ := setupTestFileHandler(t)
	defer database.Close()

	aRes, err := database.Conn().Exec("INSERT INTO artists (name, clean_name) VALUES ('TestBand', 'testband')")
	if err != nil {
		t.Fatal(err)
	}
	artID, _ := aRes.LastInsertId()

	_, err = database.Conn().Exec(`
		INSERT INTO albums (title, clean_title, artist_id, folder_path, is_complete, present_tracks_count, expected_tracks_count, deezer_id)
		VALUES ('Greatest Hits', 'greatesthits', ?, 'TestBand/Greatest Hits', 0, 3, 5, '12345')
	`, artID)
	if err != nil {
		t.Fatal(err)
	}

	body := strings.NewReader(`{
		"path": "TestBand/Greatest Hits",
		"deezer_album_id": "12345",
		"track_numbers": [2, 4]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/files/audit/incomplete-albums/download-missing", body)
	w := httptest.NewRecorder()
	h.DownloadMissing(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var jobs []*downloader.DownloadJob
	if err := json.NewDecoder(w.Body).Decode(&jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	job := jobs[0]
	if !job.IsBackfill {
		t.Errorf("expected job.IsBackfill true, got false")
	}
	if job.TargetAlbumPath == nil || *job.TargetAlbumPath != "TestBand/Greatest Hits" {
		t.Errorf("expected TargetAlbumPath 'TestBand/Greatest Hits', got %v", job.TargetAlbumPath)
	}
}

func TestFileHandler_BackfillEditorData(t *testing.T) {
	h, musicSb, tmpSb, database, musicDir, tmpDir := setupTestFileHandler(t)
	defer database.Close()

	// 1. Create existing library track in music sandbox
	albumDir := filepath.Join(musicDir, "TestBand", "Greatest Hits")
	_ = os.MkdirAll(albumDir, 0755)
	t1Path := filepath.Join(albumDir, "01. Track One.mp3")
	_ = os.WriteFile(t1Path, []byte("audio1"), 0644)
	tags1 := id3.TrackTags{
		Title:       "Track One",
		Artist:      "TestBand",
		Album:       "Greatest Hits",
		TrackNumber: 1,
		Year:        2024,
		Genre:       "Rock",
	}
	_ = id3.WriteTags(t1Path, tags1, nil)

	// 2. Create downloaded missing track in tmp sandbox
	jobID := "job-backfill-test"
	jobConvertedDir := filepath.Join(tmpDir, jobID, "converted")
	_ = os.MkdirAll(jobConvertedDir, 0755)
	t2Path := filepath.Join(jobConvertedDir, "02. Track Two.mp3")
	_ = os.WriteFile(t2Path, []byte("audio2"), 0644)
	tags2 := id3.TrackTags{
		Title:       "Track Two",
		Artist:      "TestBand",
		Album:       "Greatest Hits",
		TrackNumber: 2,
		Year:        2024,
		Genre:       "Rock",
	}
	_ = id3.WriteTags(t2Path, tags2, nil)

	coverFile := filepath.Join(jobConvertedDir, "cover.jpg")
	_ = os.WriteFile(coverFile, []byte("fake jpg cover"), 0644)

	_ = musicSb
	_ = tmpSb

	req := httptest.NewRequest(http.MethodGet, "/files/audit/incomplete-albums/backfill-editor-data?path=TestBand%2FGreatest%20Hits&job_id="+jobID, nil)
	w := httptest.NewRecorder()
	h.BackfillEditorData(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Tracks []id3.TrackTags `json:"tracks"`
		Album  struct {
			AlbumArtist string `json:"album_artist"`
			Album       string `json:"album"`
			Genre       string `json:"genre"`
			Year        int    `json:"year"`
			TrackTotal  int    `json:"track_total"`
		} `json:"album"`
		SourceDir       string   `json:"source_dir"`
		HasCover        bool     `json:"has_cover"`
		CoverPath       string   `json:"cover_path"`
		JobIDs          []string `json:"job_ids"`
		BackfilledCount int      `json:"backfilled_count"`
	}

	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if len(resp.Tracks) != 2 {
		t.Fatalf("expected 2 tracks merged, got %d", len(resp.Tracks))
	}
	if resp.Tracks[0].TrackNumber != 1 || resp.Tracks[1].TrackNumber != 2 {
		t.Errorf("tracks not sorted properly: %v, %v", resp.Tracks[0].TrackNumber, resp.Tracks[1].TrackNumber)
	}
	if !resp.HasCover {
		t.Errorf("expected HasCover true")
	}
	if len(resp.JobIDs) != 1 || resp.JobIDs[0] != jobID {
		t.Errorf("expected JobIDs [%s], got %v", jobID, resp.JobIDs)
	}
	if resp.Album.Album != "Greatest Hits" {
		t.Errorf("expected album 'Greatest Hits', got %s", resp.Album.Album)
	}
}
