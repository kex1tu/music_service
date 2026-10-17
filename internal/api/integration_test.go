package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"music-manager/internal/api/handlers"
	"music-manager/internal/config"
	"music-manager/internal/core/auth"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	featureFileManager "music-manager/internal/features/filemanager"
	"music-manager/internal/id3"
	"music-manager/internal/services/downloader"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/radar"
	"music-manager/internal/services/resolver"
	"music-manager/internal/services/scanner"
)

type integrationContext struct {
	server       *httptest.Server
	router       http.Handler
	cfg          *config.Config
	database     *db.DB
	musicSb      *sandbox.PathSandbox
	tmpSb        *sandbox.PathSandbox
	scanner      *scanner.Scanner
	downloader   *downloader.Service
	deezerClient *resolver.DeezerClient
}

func setupIntegrationServer(t *testing.T) *integrationContext {
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

	pwHash, _ := auth.HashPassword("integration_pass")
	cfg := &config.Config{
		Host:                   "127.0.0.1",
		Port:                   8010,
		MusicDir:               musicDir,
		TmpDir:                 tmpDir,
		DatabasePath:           filepath.Join(tempDir, "integration.db"),
		SecretKey:              "integration_secret_key_long_enough_123456",
		AdminLogin:             "admin",
		AdminPasswordHash:      pwHash,
		MaxConcurrentDownloads: 2,
	}

	database, err := db.Open(cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	deezerClient := resolver.NewDeezerClient()
	matcher := resolver.NewAudioMatcher("")
	res := resolver.NewSourceResolver(deezerClient, matcher, "")

	jobMgr := downloader.NewJobManager(database)
	downSvc := downloader.NewService(context.Background(), jobMgr, res, tmpSb, musicSb, "", 2)
	downSvc.PauseQueue() // pause queue during tests so we control async jobs
	t.Cleanup(downSvc.Close)

	orgSvc := organizer.NewService(musicSb, tmpSb)
	lyrSvc := lyrics.NewService(musicSb)
	scannerSvc := scanner.NewScanner(musicSb, database, deezerClient)

	agg := resolver.NewDiscographyAggregator(deezerClient, nil, nil, database)
	radarSvc := radar.NewService(database, agg, downSvc, cfg)

	authHandler := handlers.NewAuthHandler(cfg)
	downHandler := handlers.NewDownloadHandler(downSvc, deezerClient)
	tagHandler := handlers.NewTagHandler(musicSb, tmpSb, orgSvc, deezerClient)
	fileHandler := handlers.NewFileHandler(handlers.FileHandlerParams{
		MusicSandbox: musicSb,
		TmpSandbox:   tmpSb,
		Database:     database,
		Downloader:   downSvc,
		Organizer:    orgSvc,
		Deezer:       deezerClient,
		Lyrics:       lyrSvc,
		Scanner:      scannerSvc,
	})
	lyrHandler := handlers.NewLyricsHandler(lyrSvc)
	radarHandler := handlers.NewRadarHandler(radarSvc, database)

	fileManagerRepo := featureFileManager.NewRepository(database.Conn())
	fileManagerSvc := featureFileManager.NewService(featureFileManager.ServiceConfig{
		MusicSandbox: musicSb,
		TmpSandbox:   tmpSb,
		Repo:         fileManagerRepo,
		Organizer:    &testFileManagerOrganizerAdapter{sb: musicSb},
		Scanner:      &testFileManagerScannerAdapter{scanner: scannerSvc, sb: musicSb},
		JobCreator:   &testFileManagerJobCreator{downSvc: downSvc},
		SourceFinder: &testFileManagerSourceFinder{deezer: deezerClient},
	})
	fileManagerModule := featureFileManager.NewModule(fileManagerSvc)

	router := NewRouter(RouterParams{
		Config:            cfg,
		AuthHandler:       authHandler,
		DownloadHandler:   downHandler,
		TagHandler:        tagHandler,
		FileHandler:       fileHandler,
		FileManagerModule: fileManagerModule,
		LyricsHandler:     lyrHandler,
		RadarHandler:      radarHandler,
	})

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &integrationContext{
		server:       server,
		router:       router,
		cfg:          cfg,
		database:     database,
		musicSb:      musicSb,
		tmpSb:        tmpSb,
		scanner:      scannerSvc,
		downloader:   downSvc,
		deezerClient: deezerClient,
	}
}

func loginForCookie(t *testing.T, serverURL string) *http.Cookie {
	loginPayload, _ := json.Marshal(map[string]string{
		"login":    "admin",
		"password": "integration_pass",
	})

	resp, err := http.Post(serverURL+"/api/auth/login", "application/json", bytes.NewReader(loginPayload))
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected login 200, got %d", resp.StatusCode)
	}

	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	t.Fatalf("session cookie not returned in login response")
	return nil
}

// TestIntegration_AuthFlow:
// POST /api/auth/login → cookie → GET /files (200) → POST /api/auth/logout → GET /files (401)
func TestIntegration_AuthFlow(t *testing.T) {
	it := setupIntegrationServer(t)

	// 1. Initial unauthenticated request to /files -> 401
	unauthResp, err := http.Get(it.server.URL + "/files")
	if err != nil {
		t.Fatal(err)
	}
	defer unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthenticated request, got %d", unauthResp.StatusCode)
	}

	// 2. Login -> obtain session cookie
	sessionCookie := loginForCookie(t, it.server.URL)

	// 3. Authenticated request with cookie -> 200 OK
	client := it.server.Client()
	req, _ := http.NewRequest("GET", it.server.URL+"/files", nil)
	req.AddCookie(sessionCookie)
	authResp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer authResp.Body.Close()
	if authResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for authenticated request, got %d", authResp.StatusCode)
	}

	// 4. Logout -> invalidates cookie
	logoutReq, _ := http.NewRequest("POST", it.server.URL+"/api/auth/logout", nil)
	logoutReq.AddCookie(sessionCookie)
	logoutResp, err := client.Do(logoutReq)
	if err != nil {
		t.Fatal(err)
	}
	defer logoutResp.Body.Close()
	if logoutResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for logout, got %d", logoutResp.StatusCode)
	}

	// Check that cookie was expired (Max-Age < 0)
	var expiredCookie *http.Cookie
	for _, c := range logoutResp.Cookies() {
		if c.Name == auth.CookieName {
			expiredCookie = c
			break
		}
	}
	if expiredCookie == nil || expiredCookie.MaxAge > 0 {
		t.Errorf("expected session cookie to be expired in logout response")
	}

	// 5. Subsequent request with expired cookie -> 401
	reqAfter, _ := http.NewRequest("GET", it.server.URL+"/files", nil)
	if expiredCookie != nil {
		reqAfter.AddCookie(expiredCookie)
	}
	afterResp, err := client.Do(reqAfter)
	if err != nil {
		t.Fatal(err)
	}
	defer afterResp.Body.Close()
	if afterResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 after logout, got %d", afterResp.StatusCode)
	}
}

// TestIntegration_PathTraversalBlocked:
// GET /files?path=../../etc/passwd → 403 Forbidden
func TestIntegration_PathTraversalBlocked(t *testing.T) {
	it := setupIntegrationServer(t)
	cookie := loginForCookie(t, it.server.URL)

	client := it.server.Client()
	traversalURLs := []string{
		it.server.URL + "/files?path=../../etc/passwd",
		it.server.URL + "/files?path=%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		it.server.URL + "/files?path=..",
	}

	for _, u := range traversalURLs {
		req, _ := http.NewRequest("GET", u, nil)
		req.AddCookie(cookie)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for traversal path %s, got %d", u, resp.StatusCode)
		}
	}
}

// TestIntegration_ScanAndSearch:
// Scan MUSIC_DIR → GET /files/search?q=... → нашлись все треки
func TestIntegration_ScanAndSearch(t *testing.T) {
	it := setupIntegrationServer(t)
	cookie := loginForCookie(t, it.server.URL)

	// Create test album and mp3 on disk
	artistDir := filepath.Join(it.musicSb.Root(), "Integration Band")
	albumDir := filepath.Join(artistDir, "First Album")
	_ = os.MkdirAll(albumDir, 0755)

	mp3Path := filepath.Join(albumDir, "01. Anthem.mp3")
	_ = os.WriteFile(mp3Path, []byte("fake-mp3-audio-payload-for-scanner"), 0644)

	_ = id3.WriteTags(mp3Path, id3.TrackTags{
		Title:       "Integration Anthem",
		Artist:      "Integration Band",
		Album:       "First Album",
		TrackNumber: 1,
		Year:        2024,
	}, nil)

	// Run scanner
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := it.scanner.ScanAll(ctx); err != nil {
		t.Fatalf("scanner.ScanAll failed: %v", err)
	}

	// Search library via HTTP API
	client := it.server.Client()
	req, _ := http.NewRequest("GET", it.server.URL+"/files/search?q=Anthem", nil)
	req.AddCookie(cookie)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for search, got %d", resp.StatusCode)
	}

	var searchResults struct {
		Tracks  []map[string]any `json:"tracks"`
		Albums  []map[string]any `json:"albums"`
		Artists []map[string]any `json:"artists"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResults); err != nil {
		t.Fatalf("failed to decode search JSON: %v", err)
	}

	if len(searchResults.Tracks) == 0 {
		t.Fatalf("expected search results to find 'Integration Anthem', got 0 tracks")
	}

	found := false
	for _, res := range searchResults.Tracks {
		if title, ok := res["title"].(string); ok && strings.Contains(title, "Anthem") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected search results to contain 'Anthem', got %+v", searchResults.Tracks)
	}
}

// TestIntegration_StreamRange:
// GET /api/tracks/{id}/stream с Range: bytes=0-1023 → 206 Partial Content + ETag
func TestIntegration_StreamRange(t *testing.T) {
	it := setupIntegrationServer(t)
	cookie := loginForCookie(t, it.server.URL)

	// Create 4096-byte dummy MP3 file on disk
	streamDir := filepath.Join(it.musicSb.Root(), "Stream Test")
	_ = os.MkdirAll(streamDir, 0755)
	mp3Path := filepath.Join(streamDir, "01. StreamTrack.mp3")

	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i % 256)
	}
	_ = os.WriteFile(mp3Path, payload, 0644)

	relPath := "Stream Test/01. StreamTrack.mp3"
	_ = id3.WriteTags(mp3Path, id3.TrackTags{
		Title:  "Stream Test Track",
		Artist: "Stream Artist",
		Album:  "Stream Test",
	}, nil)

	if err := it.scanner.IndexFile(mp3Path, relPath); err != nil {
		t.Fatalf("failed to index stream file: %v", err)
	}

	fileBytes, err := os.ReadFile(mp3Path)
	if err != nil {
		t.Fatal(err)
	}

	// Query track ID from DB
	var trackID int64
	err = it.database.Conn().QueryRowContext(context.Background(), "SELECT id FROM tracks WHERE file_path = ?", relPath).Scan(&trackID)
	if err != nil {
		t.Fatalf("failed to get track ID: %v", err)
	}

	client := it.server.Client()

	// 1. Full stream request without Range
	fullReq, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/tracks/%d/stream", it.server.URL, trackID), nil)
	fullReq.AddCookie(cookie)
	fullResp, err := client.Do(fullReq)
	if err != nil {
		t.Fatal(err)
	}
	defer fullResp.Body.Close()
	if fullResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for full stream, got %d", fullResp.StatusCode)
	}
	etag := fullResp.Header.Get("ETag")

	// 2. Range request: bytes=0-1023
	rangeReq, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/tracks/%d/stream", it.server.URL, trackID), nil)
	rangeReq.AddCookie(cookie)
	rangeReq.Header.Set("Range", "bytes=0-1023")
	rangeResp, err := client.Do(rangeReq)
	if err != nil {
		t.Fatal(err)
	}
	defer rangeResp.Body.Close()

	if rangeResp.StatusCode != http.StatusPartialContent {
		t.Fatalf("expected 206 Partial Content, got %d", rangeResp.StatusCode)
	}

	contentRange := rangeResp.Header.Get("Content-Range")
	if !strings.HasPrefix(contentRange, "bytes 0-1023/") {
		t.Errorf("expected Content-Range starting with 'bytes 0-1023/', got %q", contentRange)
	}
	if rangeResp.Header.Get("Accept-Ranges") != "bytes" {
		t.Errorf("expected Accept-Ranges: bytes, got %q", rangeResp.Header.Get("Accept-Ranges"))
	}
	if etag != "" && rangeResp.Header.Get("ETag") != etag {
		t.Errorf("expected matching ETag %q, got %q", etag, rangeResp.Header.Get("ETag"))
	}

	rangeBytes, _ := io.ReadAll(rangeResp.Body)
	if len(rangeBytes) != 1024 {
		t.Errorf("expected exactly 1024 bytes in response body, got %d", len(rangeBytes))
	}
	if !bytes.Equal(rangeBytes, fileBytes[0:1024]) {
		t.Errorf("streamed range bytes do not match expected file slice")
	}
}

// TestIntegration_RadarReconcile:
// Seed radar_releases with status=downloaded / status=in_library / status=new
// Verify GET /api/radar/feed returns only status=new
func TestIntegration_RadarReconcile(t *testing.T) {
	it := setupIntegrationServer(t)
	cookie := loginForCookie(t, it.server.URL)

	ctx := context.Background()

	// Seed artist
	_, err := it.database.Conn().ExecContext(ctx, `
		INSERT INTO artists (name, clean_name) VALUES ('Integration Radar Artist', 'integration radar artist');
	`)
	if err != nil {
		t.Fatal(err)
	}

	// Seed 3 releases in radar_releases:
	// 1. downloaded (should be excluded)
	// 2. in_library (should be excluded)
	// 3. new (should be included in feed)
	_, err = it.database.Conn().ExecContext(ctx, `
		INSERT INTO radar_releases (artist_name, clean_artist_name, title, clean_title, release_type, source_url, release_date, year, status, is_radar, deezer_id)
		VALUES 
			('Integration Radar Artist', 'integration radar artist', 'Old Downloaded Track', 'old downloaded track', 'single', 'https://deezer.com/1', '2023-01-01', 2023, 'downloaded', 1, 'dz_old_dl'),
			('Integration Radar Artist', 'integration radar artist', 'In Library Track', 'in library track', 'single', 'https://deezer.com/2', '2023-02-01', 2023, 'in_library', 0, 'dz_in_lib'),
			('Integration Radar Artist', 'integration radar artist', 'Fresh New Track', 'fresh new track', 'single', 'https://deezer.com/3', '2024-05-01', 2024, 'new', 1, 'dz_fresh');
	`)
	if err != nil {
		t.Fatal(err)
	}

	client := it.server.Client()
	req, _ := http.NewRequest("GET", it.server.URL+"/api/radar/feed", nil)
	req.AddCookie(cookie)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for radar feed, got %d", resp.StatusCode)
	}

	var feedResp struct {
		Items []struct {
			ID     int64  `json:"id"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&feedResp); err != nil {
		t.Fatalf("failed to decode radar feed JSON: %v", err)
	}

	if len(feedResp.Items) != 1 {
		t.Fatalf("expected exactly 1 release in radar feed ('Fresh New Track'), got %d", len(feedResp.Items))
	}
	if feedResp.Items[0].Title != "Fresh New Track" {
		t.Errorf("expected 'Fresh New Track', got %q", feedResp.Items[0].Title)
	}
	if feedResp.Items[0].Status != "new" {
		t.Errorf("expected status 'new', got %q", feedResp.Items[0].Status)
	}
}

// TestIntegration_FullDownloadFlow:
// POST /downloads/ → 200 OK with job ID → GET /downloads/{id} polling → verifies job created and tracked
func TestIntegration_FullDownloadFlow(t *testing.T) {
	it := setupIntegrationServer(t)
	cookie := loginForCookie(t, it.server.URL)

	client := it.server.Client()

	downloadBody, _ := json.Marshal(map[string]string{
		"url":    "https://www.deezer.com/album/302127",
		"source": "deezer",
	})
	postReq, _ := http.NewRequest("POST", it.server.URL+"/downloads/", bytes.NewReader(downloadBody))
	postReq.Header.Set("Content-Type", "application/json")
	postReq.AddCookie(cookie)

	postResp, err := client.Do(postReq)
	if err != nil {
		t.Fatal(err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusOK && postResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(postResp.Body)
		t.Fatalf("expected 200 or 202 for download creation, got %d: %s", postResp.StatusCode, string(body))
	}

	var createResp map[string]any
	if err := json.NewDecoder(postResp.Body).Decode(&createResp); err != nil {
		t.Fatalf("failed to decode create download JSON: %v", err)
	}

	jobID, ok := createResp["id"].(string)
	if !ok || jobID == "" {
		t.Fatalf("expected job ID string in response, got %+v", createResp)
	}

	// Poll GET /downloads/{id}
	getReq, _ := http.NewRequest("GET", fmt.Sprintf("%s/downloads/%s", it.server.URL, jobID), nil)
	getReq.AddCookie(cookie)

	getResp, err := client.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for download job status, got %d", getResp.StatusCode)
	}

	var jobSnap map[string]any
	if err := json.NewDecoder(getResp.Body).Decode(&jobSnap); err != nil {
		t.Fatalf("failed to decode job status JSON: %v", err)
	}

	if jobSnap["id"] != jobID {
		t.Errorf("expected job ID %q, got %v", jobID, jobSnap["id"])
	}
	status, _ := jobSnap["status"].(string)
	if status == "" {
		t.Errorf("expected non-empty status in job snapshot")
	}
}

type testFileManagerOrganizerAdapter struct {
	sb *sandbox.PathSandbox
}

func (a *testFileManagerOrganizerAdapter) RenameFile(oldRel, newRel string) ([]string, error) {
	oldFull, err := a.sb.Resolve(oldRel, false)
	if err != nil {
		return nil, err
	}
	newFull, err := a.sb.Resolve(newRel, false)
	if err != nil {
		return nil, err
	}
	target, err := organizer.MoveTrackWithCompanions(oldFull, newFull, false)
	if err != nil {
		return nil, err
	}
	relTarget, _ := a.sb.ToRelative(target)
	return []string{relTarget}, nil
}

func (a *testFileManagerOrganizerAdapter) SyncCompanions(oldBaseRel, newBaseRel string) error {
	return nil
}

type testFileManagerScannerAdapter struct {
	scanner *scanner.Scanner
	sb      *sandbox.PathSandbox
}

func (a *testFileManagerScannerAdapter) IndexFile(ctx context.Context, relPath string) error {
	full, err := a.sb.Resolve(relPath, false)
	if err != nil {
		return err
	}
	return a.scanner.IndexFile(full, relPath)
}

func (a *testFileManagerScannerAdapter) RemoveFile(ctx context.Context, relPath string) error {
	return a.scanner.RemoveFile(relPath)
}

type testFileManagerJobCreator struct {
	downSvc *downloader.Service
}

func (j *testFileManagerJobCreator) CreateJob(ctx context.Context, rawURL, source string) (string, error) {
	job, err := j.downSvc.CreateJob(rawURL, source)
	if err != nil {
		return "", err
	}
	return job.ID, nil
}

type testFileManagerSourceFinder struct {
	deezer *resolver.DeezerClient
}

func (f *testFileManagerSourceFinder) FindTrackURL(ctx context.Context, artist, title, album string, duration int) (string, error) {
	if f.deezer != nil && (artist != "" || title != "") {
		tracks, err := f.deezer.SearchTracks(ctx, fmt.Sprintf("%s %s", artist, title))
		if err == nil && len(tracks) > 0 {
			return fmt.Sprintf("https://www.deezer.com/track/%d", tracks[0].ID), nil
		}
	}
	return "", fmt.Errorf("source not found for track %s - %s", artist, title)
}

func (f *testFileManagerSourceFinder) FindAlbumURL(ctx context.Context, artist, album string) (string, error) {
	if f.deezer != nil && (artist != "" || album != "") {
		albums, err := f.deezer.SearchAlbums(ctx, fmt.Sprintf("%s %s", artist, album))
		if err == nil && len(albums) > 0 {
			return fmt.Sprintf("https://www.deezer.com/album/%d", albums[0].ID), nil
		}
	}
	return "", fmt.Errorf("source not found for album %s - %s", artist, album)
}

