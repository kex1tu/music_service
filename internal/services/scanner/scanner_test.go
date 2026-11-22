package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
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
		AlbumArtist: "TestArtist\x00Co-Artist",
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
	if err := database.Conn().QueryRow(`SELECT COUNT(*) FROM album_artists aa JOIN artists ar ON ar.id = aa.artist_id
		JOIN albums al ON al.id = aa.album_id WHERE al.title = 'TestAlbum' AND ar.name = 'Co-Artist' AND aa.position = 2`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("scanner missed the second TPE2 credit: %d, %v", count, err)
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

func TestScanner_InvalidPathDetection(t *testing.T) {
	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	_ = os.MkdirAll(filepath.Join(musicDir, "CustomFolder"), 0755)

	sb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(tempDir, "test3.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Track in folder "CustomFolder", but tag Album is "OfficialAlbum"
	mp3Path := filepath.Join(musicDir, "CustomFolder", "Track1.mp3")
	_ = os.WriteFile(mp3Path, []byte("mp3data"), 0644)
	tags := id3.TrackTags{
		Title:       "My Song",
		Artist:      "Artist",
		Album:       "OfficialAlbum",
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

	// Verify tracks table has is_path_valid = 0 and suggested_path = "OfficialAlbum/01. My Song.mp3"
	var isPathValid bool
	var suggestedPath, reason string
	err = database.Conn().QueryRow("SELECT is_path_valid, suggested_path, path_violation_reason FROM tracks WHERE title = 'My Song'").
		Scan(&isPathValid, &suggestedPath, &reason)
	if err != nil {
		t.Fatalf("failed to query track: %v", err)
	}
	if isPathValid {
		t.Errorf("expected is_path_valid = false for non-canonical folder, got true")
	}
	expectedSug := "OfficialAlbum - Artist/01. My Song.mp3"
	if suggestedPath != expectedSug {
		t.Errorf("expected suggested_path %q, got %q", expectedSug, suggestedPath)
	}
	if reason == "" {
		t.Errorf("expected non-empty path_violation_reason")
	}

	// Verify GetInvalidPaths returns it
	invPaths, err := database.GetInvalidPaths(context.Background())
	if err != nil {
		t.Fatalf("GetInvalidPaths failed: %v", err)
	}
	if len(invPaths) != 1 {
		t.Fatalf("expected 1 invalid path, got %d", len(invPaths))
	}
	if invPaths[0].SuggestedPath != expectedSug {
		t.Errorf("expected suggested path %q, got %q", expectedSug, invPaths[0].SuggestedPath)
	}

	// Verify audit_issues table has invalid_path
	var auditCount int
	err = database.Conn().QueryRow("SELECT COUNT(*) FROM audit_issues WHERE issue_type = 'invalid_path'").Scan(&auditCount)
	if err != nil || auditCount != 1 {
		t.Errorf("expected 1 invalid_path audit issue, got %d, err: %v", auditCount, err)
	}
}

func TestScanner_MultiArtistAlbum_Completeness(t *testing.T) {
	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	albumFolder := filepath.Join(musicDir, "Dragonborn - Big Baby Tape")
	_ = os.MkdirAll(albumFolder, 0755)

	sb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(tempDir, "test_multi_artist.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// 1. Track 1: Solo Big Baby Tape
	f1 := filepath.Join(albumFolder, "01. Gimme The Loot.mp3")
	_ = os.WriteFile(f1, []byte("fake mp3 1"), 0644)
	_ = id3.WriteTags(f1, id3.TrackTags{
		Title:       "Gimme The Loot",
		Artist:      "Big Baby Tape",
		Album:       "Dragonborn",
		AlbumArtist: "Big Baby Tape",
		TrackNumber: 1,
		TrackTotal:  3,
	}, nil)

	// 2. Track 2: Featured JEEMBO with multi-value artist
	f2 := filepath.Join(albumFolder, "02. Konichiwa.mp3")
	_ = os.WriteFile(f2, []byte("fake mp3 2"), 0644)
	_ = id3.WriteTags(f2, id3.TrackTags{
		Title:       "Konichiwa",
		Artist:      "Big Baby Tape\\JEEMBO",
		Album:       "Dragonborn",
		TrackNumber: 2,
		TrackTotal:  3,
	}, nil)

	// 3. Track 3: Featured Boulevard Depo with null-separated multi-value artist
	f3 := filepath.Join(albumFolder, "03. Flip Phone Twerk.mp3")
	_ = os.WriteFile(f3, []byte("fake mp3 3"), 0644)
	_ = id3.WriteTags(f3, id3.TrackTags{
		Title:       "Flip Phone Twerk",
		Artist:      "Big Baby Tape\\Boulevard Depo",
		Album:       "Dragonborn",
		TrackNumber: 3,
		TrackTotal:  3,
	}, nil)

	s := NewScanner(sb, database, nil)
	scanned, err := s.ScanAll(context.Background())
	if err != nil {
		t.Fatalf("ScanAll failed: %v", err)
	}
	if scanned != 3 {
		t.Errorf("expected 3 scanned files, got %d", scanned)
	}

	// Verify only ONE unified album was created
	var albumCount int
	err = database.Conn().QueryRow("SELECT COUNT(*) FROM albums WHERE title = 'Dragonborn'").Scan(&albumCount)
	if err != nil || albumCount != 1 {
		t.Fatalf("expected exactly 1 album row for 'Dragonborn', got %d (err: %v)", albumCount, err)
	}

	// Verify album completeness and track count
	var presentCount, totalTracks, isComplete int
	var artistName string
	err = database.Conn().QueryRow(`
		SELECT al.present_tracks_count, al.total_tracks, al.is_complete, ar.name
		FROM albums al
		JOIN artists ar ON al.artist_id = ar.id
		WHERE al.title = 'Dragonborn'
	`).Scan(&presentCount, &totalTracks, &isComplete, &artistName)
	if err != nil {
		t.Fatalf("failed to query album: %v", err)
	}

	if artistName != "Big Baby Tape" {
		t.Errorf("expected album artist 'Big Baby Tape', got %q", artistName)
	}
	if presentCount != 3 {
		t.Errorf("expected present_tracks_count = 3, got %d", presentCount)
	}
	if totalTracks != 3 {
		t.Errorf("expected total_tracks = 3, got %d", totalTracks)
	}
	if isComplete != 0 {
		t.Errorf("catalog has not been verified, got is_complete = %d", isComplete)
	}
	var albumCredits int
	if err := database.Conn().QueryRow(`SELECT COUNT(*) FROM album_artists aa JOIN albums al ON al.id = aa.album_id
		WHERE al.title = 'Dragonborn'`).Scan(&albumCredits); err != nil || albumCredits != 1 {
		t.Fatalf("track features became album credits: %d credits, err: %v", albumCredits, err)
	}
}

func TestScanner_RemoveFile(t *testing.T) {
	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	_ = os.MkdirAll(filepath.Join(musicDir, "RemoveAlbum"), 0755)

	sb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(filepath.Join(tempDir, "test_remove.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Create 2 tracks
	f1 := filepath.Join(musicDir, "RemoveAlbum", "01. Song1.mp3")
	f2 := filepath.Join(musicDir, "RemoveAlbum", "02. Song2.mp3")
	_ = os.WriteFile(f1, []byte("fake mp3 1"), 0644)
	_ = os.WriteFile(f2, []byte("fake mp3 2"), 0644)

	_ = id3.WriteTags(f1, id3.TrackTags{Title: "Song1", Artist: "ArtistX", Album: "RemoveAlbum", TrackNumber: 1, TrackTotal: 2}, nil)
	_ = id3.WriteTags(f2, id3.TrackTags{Title: "Song2", Artist: "ArtistX", Album: "RemoveAlbum", TrackNumber: 2, TrackTotal: 2}, nil)

	s := NewScanner(sb, database, nil)
	scanned, err := s.ScanAll(context.Background())
	if err != nil || scanned != 2 {
		t.Fatalf("ScanAll failed: scanned=%d, err=%v", scanned, err)
	}

	// Verify album present_tracks_count is 2
	var presentCount int
	err = database.Conn().QueryRow("SELECT present_tracks_count FROM albums WHERE title = 'RemoveAlbum'").Scan(&presentCount)
	if err != nil || presentCount != 2 {
		t.Fatalf("expected present_tracks_count 2, got %d, err=%v", presentCount, err)
	}

	var artistID int64
	if err := database.Conn().QueryRow("SELECT id FROM artists WHERE name = 'ArtistX'").Scan(&artistID); err != nil {
		t.Fatal(err)
	}

	// Remove track 1
	rel1 := filepath.Join("RemoveAlbum", "01. Song1.mp3")
	if err := s.RemoveFile(rel1); err != nil {
		t.Fatalf("RemoveFile failed: %v", err)
	}

	// Verify track 1 removed, album count is 1
	var trackCount int
	_ = database.Conn().QueryRow("SELECT COUNT(*) FROM tracks WHERE title = 'Song1'").Scan(&trackCount)
	if trackCount != 0 {
		t.Errorf("expected Song1 to be removed from tracks, but found count=%d", trackCount)
	}
	_ = database.Conn().QueryRow("SELECT present_tracks_count FROM albums WHERE title = 'RemoveAlbum'").Scan(&presentCount)
	if presentCount != 1 {
		t.Errorf("expected present_tracks_count=1 after removing 1 track, got %d", presentCount)
	}

	// Remove track 2
	rel2 := filepath.Join("RemoveAlbum", "02. Song2.mp3")
	if err := s.RemoveFile(rel2); err != nil {
		t.Fatalf("RemoveFile failed: %v", err)
	}

	// Remove the album and retain the artist referenced by radar settings.
	var albumCount, artistCount int
	_ = database.Conn().QueryRow("SELECT COUNT(*) FROM albums WHERE title = 'RemoveAlbum'").Scan(&albumCount)
	_ = database.Conn().QueryRow("SELECT COUNT(*) FROM artists WHERE name = 'ArtistX'").Scan(&artistCount)
	if albumCount != 0 {
		t.Errorf("expected album to be deleted, got %d", albumCount)
	}
	if artistCount != 1 {
		t.Errorf("expected referenced artist to remain, got %d", artistCount)
	}
	if err := s.IndexFile(f1, rel1); err != nil {
		t.Fatal(err)
	}
	var reimportedID int64
	err = database.Conn().QueryRow("SELECT artist_id FROM tracks WHERE file_path = ?", rel1).Scan(&reimportedID)
	if err != nil || reimportedID != artistID {
		t.Fatalf("reimport artist id=%d, want %d (err=%v)", reimportedID, artistID, err)
	}
}

func TestScanner_SymlinkEscape(t *testing.T) {
	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	outsideDir := filepath.Join(tempDir, "outside")
	_ = os.MkdirAll(musicDir, 0755)
	_ = os.MkdirAll(outsideDir, 0755)

	sb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(filepath.Join(tempDir, "test_symlink.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Create outside MP3
	outsideMP3 := filepath.Join(outsideDir, "Secret.mp3")
	_ = os.WriteFile(outsideMP3, []byte("outside mp3"), 0644)
	_ = id3.WriteTags(outsideMP3, id3.TrackTags{Title: "SecretSong", Artist: "Hacker", Album: "Hacked"}, nil)

	// Create symlink inside musicDir pointing to outsideMP3
	symlinkPath := filepath.Join(musicDir, "Symlinked.mp3")
	if err := os.Symlink(outsideMP3, symlinkPath); err != nil {
		t.Skipf("symlink not supported on this filesystem: %v", err)
	}

	s := NewScanner(sb, database, nil)
	scanned, err := s.ScanAll(context.Background())
	if err != nil {
		t.Fatalf("ScanAll failed: %v", err)
	}
	if scanned != 0 {
		t.Errorf("expected 0 scanned files due to symlink escape, got %d", scanned)
	}

	var trackCount int
	_ = database.Conn().QueryRow("SELECT COUNT(*) FROM tracks WHERE title = 'SecretSong'").Scan(&trackCount)
	if trackCount != 0 {
		t.Errorf("security violation: outside symlink was indexed into database!")
	}
}

func TestScanner_EmptyAlbumFolder(t *testing.T) {
	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	_ = os.MkdirAll(filepath.Join(musicDir, "EmptyArtist", "EmptyAlbum"), 0755)

	sb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(filepath.Join(tempDir, "test_empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	s := NewScanner(sb, database, nil)
	scanned, err := s.ScanAll(context.Background())
	if err != nil {
		t.Fatalf("ScanAll failed: %v", err)
	}
	if scanned != 0 {
		t.Errorf("expected 0 scanned files for empty directory, got %d", scanned)
	}

	var albumCount int
	_ = database.Conn().QueryRow("SELECT COUNT(*) FROM albums").Scan(&albumCount)
	if albumCount != 0 {
		t.Errorf("expected 0 albums for empty directories, got %d", albumCount)
	}
}

func TestScanner_Concurrent(t *testing.T) {
	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	_ = os.MkdirAll(filepath.Join(musicDir, "ConcurAlbum"), 0755)

	sb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(filepath.Join(tempDir, "test_concur.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	f1 := filepath.Join(musicDir, "ConcurAlbum", "01. Track.mp3")
	_ = os.WriteFile(f1, []byte("fake mp3"), 0644)
	_ = id3.WriteTags(f1, id3.TrackTags{Title: "ConcurTrack", Artist: "ConcurArtist", Album: "ConcurAlbum", TrackNumber: 1}, nil)

	s := NewScanner(sb, database, nil)

	var wg sync.WaitGroup
	errCh := make(chan error, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ScanAll(context.Background())
			if err != nil && err.Error() != "scan already in progress" {
				errCh <- err
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("unexpected error in concurrent ScanAll: %v", err)
	}
}

func TestScanner_DeleteMissingFiles(t *testing.T) {
	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	_ = os.MkdirAll(filepath.Join(musicDir, "DelAlbum"), 0755)

	sb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(filepath.Join(tempDir, "test_del.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	f1 := filepath.Join(musicDir, "DelAlbum", "01. Song.mp3")
	_ = os.WriteFile(f1, []byte("fake mp3"), 0644)
	_ = id3.WriteTags(f1, id3.TrackTags{Title: "DelSong", Artist: "DelArtist", Album: "DelAlbum", TrackNumber: 1}, nil)

	s := NewScanner(sb, database, nil)
	scanned, err := s.ScanAll(context.Background())
	if err != nil || scanned != 1 {
		t.Fatalf("first ScanAll failed: scanned=%d, err=%v", scanned, err)
	}

	// Now delete physical file
	_ = os.Remove(f1)

	// Second ScanAll should detect missing file and delete it from DB
	scanned2, err := s.ScanAll(context.Background())
	if err != nil {
		t.Fatalf("second ScanAll failed: %v", err)
	}
	if scanned2 != 0 {
		t.Errorf("expected 0 scanned, got %d", scanned2)
	}

	var trackCount, albumCount int
	_ = database.Conn().QueryRow("SELECT COUNT(*) FROM tracks WHERE title = 'DelSong'").Scan(&trackCount)
	_ = database.Conn().QueryRow("SELECT COUNT(*) FROM albums WHERE title = 'DelAlbum'").Scan(&albumCount)
	if trackCount != 0 {
		t.Errorf("expected track to be deleted from DB after file removal, got %d", trackCount)
	}
	if albumCount != 0 {
		t.Errorf("expected album to be deleted from DB after file removal, got %d", albumCount)
	}
}

func TestScanner_CheckIncompleteAlbums_MockDeezer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/search/album") {
			resp := map[string]any{
				"data": []map[string]any{
					{
						"id":        888999,
						"title":     "Complete Album",
						"nb_tracks": 3,
						"artist": map[string]any{
							"id":   1001,
							"name": "ArtistIncomplete",
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/album/888999") {
			resp := map[string]any{
				"id":        888999,
				"title":     "Complete Album",
				"nb_tracks": 3,
				"tracks": map[string]any{
					"data": []map[string]any{
						{
							"id":             1,
							"title":          "Song One",
							"track_position": 1,
							"duration":       180,
						},
						{
							"id":             2,
							"title":          "Song Two",
							"track_position": 2,
							"duration":       200,
						},
						{
							"id":             3,
							"title":          "Song Three",
							"track_position": 3,
							"duration":       220,
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	_ = os.MkdirAll(filepath.Join(musicDir, "IncompleteDir"), 0755)

	sb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(filepath.Join(tempDir, "test_incomplete.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Only have track 1 locally
	f1 := filepath.Join(musicDir, "IncompleteDir", "01. Song One.mp3")
	_ = os.WriteFile(f1, []byte("fake mp3"), 0644)
	_ = id3.WriteTags(f1, id3.TrackTags{
		Title:       "Song One",
		Artist:      "ArtistIncomplete",
		Album:       "Complete Album",
		TrackNumber: 1,
		TrackTotal:  3,
	}, nil)

	deezerClient := resolver.NewDeezerClient()
	deezerClient.SetBaseURL(ts.URL)

	s := NewScanner(sb, database, nil)
	_, err = s.ScanAll(context.Background())
	if err != nil {
		t.Fatalf("ScanAll failed: %v", err)
	}

	sDeezer := NewScanner(sb, database, deezerClient)
	sDeezer.CheckIncompleteAlbums(context.Background())

	// Verify missing_tracks has Song Two and Song Three
	var missingCount int
	err = database.Conn().QueryRow("SELECT COUNT(*) FROM release_tracklist WHERE is_present = 0").Scan(&missingCount)
	if err != nil {
		t.Fatalf("failed to query missing_tracks: %v", err)
	}
	if missingCount != 2 {
		t.Errorf("expected 2 missing tracks (Song Two and Song Three), got %d", missingCount)
	}

	// Verify IsScanning method
	if s.IsScanning() {
		t.Errorf("expected IsScanning() to be false after completion")
	}
}

func TestSelectBestDeezerAlbumScanner(t *testing.T) {
	// 1. Empty candidates
	if got := selectBestDeezerAlbumScanner(nil, "Artist", "Album", 1, ""); got != nil {
		t.Errorf("expected nil for empty candidates, got %+v", got)
	}

	// 2. High score match
	cands := []resolver.DeezerAlbum{
		{
			ID:       10,
			Title:    "Different Title",
			NbTracks: 5,
			Artist:   resolver.DeezerArtist{Name: "Artist"},
		},
		{
			ID:       20,
			Title:    "My Best Album",
			NbTracks: 5,
			Artist:   resolver.DeezerArtist{Name: "Artist"},
		},
	}
	best := selectBestDeezerAlbumScanner(cands, "Artist", "My Best Album", 3, "")
	if best == nil || best.ID != 20 {
		t.Errorf("expected best album ID 20, got %+v", best)
	}
	homonyms := []resolver.DeezerAlbum{
		{ID: 10, Title: "My Best Album", NbTracks: 5, Artist: resolver.DeezerArtist{ID: 101, Name: "Artist"}},
		{ID: 20, Title: "My Best Album", NbTracks: 5, Artist: resolver.DeezerArtist{ID: 202, Name: "Artist"}},
	}
	if best := selectBestDeezerAlbumScanner(homonyms, "Artist", "My Best Album", 3, "202"); best == nil || best.ID != 20 {
		t.Fatalf("selected a release by the wrong Deezer artist: %+v", best)
	}

	// 3. Single track penalty for multi-track local album
	candsSingle := []resolver.DeezerAlbum{
		{
			ID:       30,
			Title:    "Single Track Version Extended Remix",
			NbTracks: 1,
			Artist:   resolver.DeezerArtist{Name: "Band"},
		},
	}
	bestSingle := selectBestDeezerAlbumScanner(candsSingle, "Band", "Single Track Version", 10, "")
	if bestSingle != nil {
		t.Errorf("expected nil due to single-track penalty on 10-track album, got %+v", bestSingle)
	}
}

func TestScanner_IndexFile_SingleNoAlbum(t *testing.T) {
	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	_ = os.MkdirAll(musicDir, 0755)

	sb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(filepath.Join(tempDir, "test_single.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Track with empty album tag (single)
	f := filepath.Join(musicDir, "SingleSong.mp3")
	_ = os.WriteFile(f, []byte("fake mp3"), 0644)
	_ = id3.WriteTags(f, id3.TrackTags{
		Title:  "SoloSingle",
		Artist: "SoloArtist",
		Album:  "",
	}, nil)

	s := NewScanner(sb, database, nil)
	rel, _ := sb.ToRelative(f)
	if err := s.IndexFile(f, rel); err != nil {
		t.Fatalf("IndexFile failed: %v", err)
	}

	var albTitle string
	err = database.Conn().QueryRow("SELECT title FROM albums WHERE id = (SELECT album_id FROM tracks WHERE title = 'SoloSingle')").Scan(&albTitle)
	if err != nil {
		t.Fatalf("failed to query album: %v", err)
	}
	if albTitle != "SoloSingle" {
		t.Errorf("expected album title fallback to track title 'SoloSingle', got %q", albTitle)
	}
}

func TestScanner_StartWatcher(t *testing.T) {
	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	_ = os.MkdirAll(musicDir, 0755)

	sb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(filepath.Join(tempDir, "test_watcher.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	s := NewScanner(sb, database, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := s.StartWatcher(ctx); err != nil {
		t.Fatalf("StartWatcher failed: %v", err)
	}
}
