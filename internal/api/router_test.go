package api

import (
	"bytes"
	"encoding/json"
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

	jobMgr := downloader.NewJobManager()
	downSvc := downloader.NewService(jobMgr, res, tmpSb, musicSb, "", 2)
	orgSvc := organizer.NewService(musicSb, tmpSb)
	lyrSvc := lyrics.NewService(musicSb)

	authHandler := handlers.NewAuthHandler(cfg)
	downHandler := handlers.NewDownloadHandler(downSvc)
	tagHandler := handlers.NewTagHandler(musicSb, tmpSb, orgSvc, deezerClient)
	fileHandler := handlers.NewFileHandler(musicSb, tmpSb, database, downSvc, orgSvc, deezerClient)
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
