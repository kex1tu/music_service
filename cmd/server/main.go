package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"music-manager/internal/api"
	"music-manager/internal/api/handlers"
	"music-manager/internal/config"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/services/downloader"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
	"music-manager/internal/services/scanner"
)

func main() {
	log.Println("[Main] Starting Music Manager (Go Backend)...")

	// 1. Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[Main] Configuration error: %v", err)
	}

	// 2. Initialize sandboxes
	musicSandbox, err := sandbox.NewPathSandbox(cfg.MusicDir)
	if err != nil {
		log.Fatalf("[Main] Failed to initialize MUSIC_DIR sandbox: %v", err)
	}
	tmpSandbox, err := sandbox.NewPathSandbox(cfg.TmpDir)
	if err != nil {
		log.Fatalf("[Main] Failed to initialize TMP_DIR sandbox: %v", err)
	}

	// 3. Initialize SQLite database
	_ = os.MkdirAll(filepath.Dir(cfg.DatabasePath), 0755)
	database, err := db.Open(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("[Main] Failed to open SQLite database: %v", err)
	}
	defer database.Close()

	// Reset library cache tables on startup (preserving download_jobs) and rebuild from filesystem
	log.Println("[Main] Resetting library metadata cache tables on startup (preserving download_jobs)...")
	if err := database.ResetLibraryTables(context.Background()); err != nil {
		log.Printf("[Main] Warning: Failed to reset library cache tables: %v", err)
	}

	// 4. Initialize services and root context
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	deezerClient := resolver.NewDeezerClient()
	audioMatcher := resolver.NewAudioMatcher(cfg.CookiesFile)
	audioMatcher.SetDB(database)
	audioMatcher.SetConfig(cfg)
	audioMatcher.SetCatalogClients(deezerClient, resolver.NewITunesClient(nil))
	sourceResolver := resolver.NewSourceResolver(
		deezerClient,
		audioMatcher,
		cfg.CookiesFile,
		cfg.YandexMusicToken,
		cfg.YandexMusicProxy,
	)

	jobManager := downloader.NewJobManager(database)
	downloadService := downloader.NewService(
		appCtx,
		jobManager,
		sourceResolver,
		tmpSandbox,
		musicSandbox,
		cfg.CookiesFile,
		cfg.MaxConcurrentDownloads,
	)
	defer downloadService.Close()

	organizerService := organizer.NewService(musicSandbox, tmpSandbox)
	lyricsService := lyrics.NewService(musicSandbox)
	scannerService := scanner.NewScanner(musicSandbox, database, deezerClient)

	// 5. Start background scanner and fsnotify watcher
	if err := scannerService.StartWatcher(appCtx); err != nil {
		log.Printf("[Main] Warning: fsnotify watcher failed to start: %v", err)
	}
	go func() {
		log.Println("[Scanner] Performing initial library scan...")
		count, sErr := scannerService.ScanAll(appCtx)
		if sErr != nil && !errors.Is(sErr, context.Canceled) {
			log.Printf("[Scanner] Initial scan error: %v", sErr)
		} else {
			log.Printf("[Scanner] Initial scan finished: indexed %d tracks", count)
		}
	}()

	// Ensure permissions (0755 for dirs, 0644 for files) are restored for Navidrome in background
	go func() {
		dirsFixed, filesFixed, pErr := organizer.FixLibraryPermissions(cfg.MusicDir)
		if pErr == nil && (dirsFixed > 0 || filesFixed > 0) {
			log.Printf("[Permissions] Adjusted permissions for Navidrome: %d dirs (0755), %d files (0644)", dirsFixed, filesFixed)
		}
	}()

	// 6. Initialize HTTP Handlers
	authHandler := handlers.NewAuthHandler(cfg)
	downloadHandler := handlers.NewDownloadHandler(downloadService, deezerClient)
	tagHandler := handlers.NewTagHandler(musicSandbox, tmpSandbox, organizerService, deezerClient, sourceResolver.Enricher())

	yandexClient := resolver.NewYandexMusicClient(cfg.YandexMusicToken, cfg.YandexMusicProxy)
	ytmusicClient := resolver.NewYouTubeMusicClient("")
	itunesClient := resolver.NewITunesClient(nil)
	mbClient := resolver.NewMusicBrainzClient(cfg.MusicBrainzUserToken)
	mbClient.SetOAuthConfig(cfg.MusicBrainzClientID, cfg.MusicBrainzClientSecret, cfg.MusicBrainzRedirectURI)
	if database != nil {
		mbClient.SetPersistence(&dbTokenPersistence{db: database})
		if savedAccess, err := database.GetSetting(appCtx, "mb_access_token"); err == nil && savedAccess != "" {
			savedRefresh, _ := database.GetSetting(appCtx, "mb_refresh_token")
			savedExpStr, _ := database.GetSetting(appCtx, "mb_expires_at")
			savedUser, _ := database.GetSetting(appCtx, "mb_username")
			var exp time.Time
			if savedExpStr != "" {
				exp, _ = time.Parse(time.RFC3339, savedExpStr)
			}
			mbClient.SetTokens(savedAccess, savedRefresh, exp, savedUser)
			log.Printf("[MusicBrainz] Loaded persisted OAuth session for user: %s", savedUser)
		}
	}
	shazamProvider := resolver.NewShazamProvider(itunesClient)

	tagHandler.SetConfig(cfg)
	tagHandler.SetMusicBrainz(mbClient)
	tagHandler.SetShazam(shazamProvider)

	fileHandler := handlers.NewFileHandler(handlers.FileHandlerParams{
		MusicSandbox: musicSandbox,
		TmpSandbox:   tmpSandbox,
		Database:     database,
		Downloader:   downloadService,
		Organizer:    organizerService,
		Deezer:       deezerClient,
		Lyrics:       lyricsService,
		Scanner:      scannerService,
		Yandex:       yandexClient,
		YTMusic:      ytmusicClient,
		ITunes:       itunesClient,
	})
	lyricsHandler := handlers.NewLyricsHandler(lyricsService)

	// 7. Initialize Router
	router := api.NewRouter(api.RouterParams{
		Config:          cfg,
		AuthHandler:     authHandler,
		DownloadHandler: downloadHandler,
		TagHandler:      tagHandler,
		FileHandler:     fileHandler,
		LyricsHandler:   lyricsHandler,
	})

	// 8. Start HTTP Server
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 300 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("[Main] Server listening at http://%s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[Main] Server error: %v", err)
		}
	}()

	// 9. Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("[Main] Shutting down gracefully...")
	appCancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[Main] Server shutdown error: %v", err)
	}
	log.Println("[Main] Server stopped.")
}

type dbTokenPersistence struct {
	db *db.DB
}

func (p *dbTokenPersistence) SaveTokens(ctx context.Context, accessToken, refreshToken string, expiresAt time.Time, username string) error {
	_ = p.db.SetSetting(ctx, "mb_access_token", accessToken)
	_ = p.db.SetSetting(ctx, "mb_refresh_token", refreshToken)
	_ = p.db.SetSetting(ctx, "mb_expires_at", expiresAt.Format(time.RFC3339))
	_ = p.db.SetSetting(ctx, "mb_username", username)
	return nil
}

func (p *dbTokenPersistence) ClearTokens(ctx context.Context) error {
	_ = p.db.DeleteSetting(ctx, "mb_access_token")
	_ = p.db.DeleteSetting(ctx, "mb_refresh_token")
	_ = p.db.DeleteSetting(ctx, "mb_expires_at")
	_ = p.db.DeleteSetting(ctx, "mb_username")
	return nil
}

