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

	"music-manager/internal/db"
	"music-manager/internal/id3"
	"music-manager/internal/services/downloader"
)

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
			Path                string  `json:"path"`
			Album               string  `json:"album"`
			Artist              string  `json:"artist"`
			PresentCount        int     `json:"present_count"`
			ExpectedCount       int     `json:"expected_count"`
			MissingTrackNumbers []int   `json:"missing_track_numbers"`
			IsCatalogMatched    bool    `json:"is_catalog_matched"`
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

func TestFileHandler_BackfillEditorData_ArtistNormalization(t *testing.T) {
	h, _, tmpSb, database, musicDir, tmpDir := setupTestFileHandler(t)
	defer database.Close()

	// Local album: "Животный мир - Макс Корж" with track 1 tagged as "Макс Корж"
	localAlbumDir := filepath.Join(musicDir, "Животный мир - Макс Корж")
	_ = os.MkdirAll(localAlbumDir, 0755)
	t1Path := filepath.Join(localAlbumDir, "01. Мир моих снов.mp3")
	_ = os.WriteFile(t1Path, []byte("track 1 audio"), 0644)
	tags1 := id3.TrackTags{
		Title:       "Мир моих снов",
		Artist:      "Макс Корж",
		AlbumArtist: "Макс Корж",
		Album:       "Животный мир",
		TrackNumber: 1,
		Year:        2012,
		Genre:       "Hip-Hop",
	}
	_ = id3.WriteTags(t1Path, tags1, nil)

	// Backfilled track from Deezer with English transliteration "Max Korzh"
	jobID := "job-korzh-backfill"
	jobConvertedDir := filepath.Join(tmpDir, jobID, "converted")
	_ = os.MkdirAll(jobConvertedDir, 0755)

	t2Path := filepath.Join(jobConvertedDir, "02. Оля.mp3")
	_ = os.WriteFile(t2Path, []byte("track 2 audio"), 0644)
	tags2 := id3.TrackTags{
		Title:       "Оля",
		Artist:      "Max Korzh",
		AlbumArtist: "Max Korzh",
		Album:       "Животный мир",
		TrackNumber: 2,
		Year:        2012,
		Genre:       "Hip-Hop",
	}
	_ = id3.WriteTags(t2Path, tags2, nil)

	_ = tmpSb

	req := httptest.NewRequest(http.MethodGet, "/files/audit/incomplete-albums/backfill-editor-data?path=%D0%96%D0%B8%D0%B2%D0%BE%D1%82%D0%BD%D1%8B%D0%B9%20%D0%BC%D0%B8%D1%80%20-%20%D0%9C%D0%B0%D0%BA%D1%81%20%D0%9A%D0%BE%D1%80%D0%B6&job_id="+jobID, nil)
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
		} `json:"album"`
	}

	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if len(resp.Tracks) != 2 {
		t.Fatalf("expected 2 tracks merged, got %d", len(resp.Tracks))
	}

	// Verify both tracks and album use canonical "Макс Корж"
	if resp.Album.AlbumArtist != "Макс Корж" {
		t.Errorf("expected album artist 'Макс Корж', got %q", resp.Album.AlbumArtist)
	}
	if resp.Album.Album != "Животный мир" {
		t.Errorf("expected album title 'Животный мир', got %q", resp.Album.Album)
	}
	for i, tr := range resp.Tracks {
		if tr.Artist != "Макс Корж" {
			t.Errorf("track %d artist should be 'Макс Корж', got %q", i+1, tr.Artist)
		}
		if tr.AlbumArtist != "Макс Корж" {
			t.Errorf("track %d album artist should be 'Макс Корж', got %q", i+1, tr.AlbumArtist)
		}
	}
}

