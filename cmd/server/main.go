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

	// 4. Initialize services
	deezerClient := resolver.NewDeezerClient()
	audioMatcher := resolver.NewAudioMatcher(cfg.CookiesFile)
	sourceResolver := resolver.NewSourceResolver(deezerClient, audioMatcher, cfg.CookiesFile)

	jobManager := downloader.NewJobManager(database)
	downloadService := downloader.NewService(
		jobManager,
		sourceResolver,
		tmpSandbox,
		musicSandbox,
		cfg.CookiesFile,
		cfg.MaxConcurrentDownloads,
	)

	organizerService := organizer.NewService(musicSandbox, tmpSandbox)
	lyricsService := lyrics.NewService(musicSandbox)
	scannerService := scanner.NewScanner(musicSandbox, database, deezerClient)

	// 5. Start background scanner and fsnotify watcher
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

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

	// 6. Initialize HTTP Handlers
	authHandler := handlers.NewAuthHandler(cfg)
	downloadHandler := handlers.NewDownloadHandler(downloadService, deezerClient)
	tagHandler := handlers.NewTagHandler(musicSandbox, tmpSandbox, organizerService, deezerClient)
	fileHandler := handlers.NewFileHandler(musicSandbox, tmpSandbox, database, downloadService, organizerService, deezerClient, lyricsService, scannerService)
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
