package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/api/handlers"
	"music-manager/internal/config"
	"music-manager/internal/core/auth"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/services/downloader"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
	"music-manager/internal/services/scanner"
)

func setupTestRouter(t *testing.T) (http.Handler, *config.Config) {
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

	pwHash, _ := auth.HashPassword("admin_pass")
	cfg := &config.Config{
		Host:                   "127.0.0.1",
		Port:                   8010,
		MusicDir:               musicDir,
		TmpDir:                 tmpDir,
		DatabasePath:           filepath.Join(tempDir, "test.db"),
		SecretKey:              "test_secret_key_long_enough_123456",
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
	t.Cleanup(downSvc.Close)
	orgSvc := organizer.NewService(musicSb, tmpSb)
	lyrSvc := lyrics.NewService(musicSb)
	scannerSvc := scanner.NewScanner(musicSb, database, deezerClient)

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

	router := NewRouter(RouterParams{
		Config:          cfg,
		AuthHandler:     authHandler,
		DownloadHandler: downHandler,
		TagHandler:      tagHandler,
		FileHandler:     fileHandler,
		LyricsHandler:   lyrHandler,
	})

	return router, cfg
}

func TestRouter_HealthAndIndex(t *testing.T) {
	router, _ := setupTestRouter(t)

	// 1. Health check
	req := httptest.NewRequest("GET", "/api/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// 2. Index page
	req2 := httptest.NewRequest("GET", "/", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Errorf("expected status 200 for index, got %d", rec2.Code)
	}
	body := rec2.Body.String()
	expectedSubstrings := []string{
		"Music Manager",
		"downloaderComponent()",
		"fileManagerComponent()",
		"libraryAuditComponent()",
		"tagEditorComponent()",
		"lyricsModalComponent()",
		"Очередь загрузок",
	}
	for _, substr := range expectedSubstrings {
		if !bytes.Contains([]byte(body), []byte(substr)) {
			t.Errorf("expected body to contain %q", substr)
		}
	}
}

func TestRouter_AuthFlow(t *testing.T) {
	router, _ := setupTestRouter(t)

	// 1. Unauthorized access to /downloads/queue/summary
	req := httptest.NewRequest("GET", "/downloads/queue/summary", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}

	// 2. Login
	loginBody, _ := json.Marshal(map[string]string{
		"login":    "admin",
		"password": "admin_pass",
	})
	reqLogin := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(loginBody))
	reqLogin.Header.Set("Content-Type", "application/json")
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, reqLogin)

	if recLogin.Code != http.StatusOK {
		t.Fatalf("expected login 200, got %d: %s", recLogin.Code, recLogin.Body.String())
	}

	cookie := recLogin.Result().Cookies()
	if len(cookie) == 0 || cookie[0].Name != auth.CookieName {
		t.Fatalf("expected session cookie in response")
	}

	// 3. Authorized access with session cookie
	reqAuth := httptest.NewRequest("GET", "/downloads/queue/summary", nil)
	reqAuth.AddCookie(cookie[0])
	recAuth := httptest.NewRecorder()
	router.ServeHTTP(recAuth, reqAuth)

	if recAuth.Code != http.StatusOK {
		t.Errorf("expected status 200 for authenticated request, got %d", recAuth.Code)
	}
}

func getAuthCookie(t *testing.T, router http.Handler) *http.Cookie {
	loginBody, _ := json.Marshal(map[string]string{
		"login":    "admin",
		"password": "admin_pass",
	})
	reqLogin := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(loginBody))
	reqLogin.Header.Set("Content-Type", "application/json")
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, reqLogin)

	if recLogin.Code != http.StatusOK {
		t.Fatalf("expected login 200, got %d", recLogin.Code)
	}
	cookies := recLogin.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("expected session cookie")
	}
	return cookies[0]
}

func TestRouter_DiscographyEndpoint(t *testing.T) {
	router, _ := setupTestRouter(t)
	authCookie := getAuthCookie(t, router)

	// 1. Unauthorized
	reqUnauth := httptest.NewRequest("GET", "/downloads/discography?url=https://www.deezer.com/artist/123", nil)
	recUnauth := httptest.NewRecorder()
	router.ServeHTTP(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 unauthorized, got %d", recUnauth.Code)
	}

	// 2. Missing url param
	reqNoURL := httptest.NewRequest("GET", "/downloads/discography", nil)
	reqNoURL.AddCookie(authCookie)
	recNoURL := httptest.NewRecorder()
	router.ServeHTTP(recNoURL, reqNoURL)
	if recNoURL.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing url, got %d", recNoURL.Code)
	}

	// 3. Invalid non-Deezer url
	reqBadURL := httptest.NewRequest("GET", "/downloads/discography?url=https://unknown.com/artist/123", nil)
	reqBadURL.AddCookie(authCookie)
	recBadURL := httptest.NewRecorder()
	router.ServeHTTP(recBadURL, reqBadURL)
	if recBadURL.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-Deezer url, got %d", recBadURL.Code)
	}
}

func TestRouter_TorrentEndpoint(t *testing.T) {
	router, _ := setupTestRouter(t)
	authCookie := getAuthCookie(t, router)

	// 1. Unauthorized
	reqUnauth := httptest.NewRequest("POST", "/downloads/torrent", nil)
	recUnauth := httptest.NewRecorder()
	router.ServeHTTP(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 unauthorized, got %d", recUnauth.Code)
	}

	// 2. Upload valid .torrent file
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "album.torrent")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("d8:announce12:udp://test.com4:infod6:lengthi12345eee"))
	_ = writer.Close()

	reqUpload := httptest.NewRequest("POST", "/downloads/torrent", body)
	reqUpload.Header.Set("Content-Type", writer.FormDataContentType())
	reqUpload.AddCookie(authCookie)
	recUpload := httptest.NewRecorder()
	router.ServeHTTP(recUpload, reqUpload)

	// Returns 400 with friendly message for now
	if recUpload.Code != http.StatusBadRequest {
		t.Errorf("expected 400 status with message, got %d", recUpload.Code)
	}
	var res map[string]string
	if err := json.NewDecoder(recUpload.Body).Decode(&res); err != nil {
		t.Errorf("expected JSON response, got error: %v", err)
	}
	if res["message"] == "" {
		t.Errorf("expected non-empty message in response")
	}
}

func TestRouter_RecognizeAudioEndpoint(t *testing.T) {
	router, _ := setupTestRouter(t)
	authCookie := getAuthCookie(t, router)

	// 1. Unauthorized
	reqUnauth := httptest.NewRequest("POST", "/tags/recognize-audio", nil)
	recUnauth := httptest.NewRecorder()
	router.ServeHTTP(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 unauthorized, got %d", recUnauth.Code)
	}

	// 2. Non-existent file - should safely return { "recognized": false } with 200
	reqPayload, _ := json.Marshal(map[string]string{
		"path": "non_existent_track.mp3",
	})
	reqRecog := httptest.NewRequest("POST", "/tags/recognize-audio", bytes.NewReader(reqPayload))
	reqRecog.Header.Set("Content-Type", "application/json")
	reqRecog.AddCookie(authCookie)
	recRecog := httptest.NewRecorder()
	router.ServeHTTP(recRecog, reqRecog)

	if recRecog.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", recRecog.Code)
	}
	var res map[string]any
	if err := json.NewDecoder(recRecog.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res["recognized"] != false {
		t.Errorf("expected recognized: false, got %v", res["recognized"])
	}
}

func TestRouter_RetryDownloadEndpoint(t *testing.T) {
	router, _ := setupTestRouter(t)
	authCookie := getAuthCookie(t, router)

	// 1. Unauthorized
	reqUnauth := httptest.NewRequest("POST", "/downloads/job-123/retry", nil)
	recUnauth := httptest.NewRecorder()
	router.ServeHTTP(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 unauthorized, got %d", recUnauth.Code)
	}

	// 2. Non-existent job
	reqNonExistent := httptest.NewRequest("POST", "/downloads/non-existent-job/retry", nil)
	reqNonExistent.AddCookie(authCookie)
	recNonExistent := httptest.NewRecorder()
	router.ServeHTTP(recNonExistent, reqNonExistent)
	if recNonExistent.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for non-existent job, got %d", recNonExistent.Code)
	}
}

