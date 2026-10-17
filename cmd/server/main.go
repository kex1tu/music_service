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
	featureAudit "music-manager/internal/features/audit"
	featureDownloader "music-manager/internal/features/downloader"
	featureFileManager "music-manager/internal/features/filemanager"
	featureRadar "music-manager/internal/features/radar"
	featureRadio "music-manager/internal/features/radio"
	featureTagEditor "music-manager/internal/features/tageditor"
	"music-manager/internal/services/downloader"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/radar"
	"music-manager/internal/services/radio"
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

	// Initialize Downloader Vertical Feature Slice
	downloaderRepo := featureDownloader.NewRepository(database.Conn())
	downloaderFeatureSvc := featureDownloader.NewService(featureDownloader.ServiceConfig{
		Repo:          downloaderRepo,
		Resolver:      sourceResolver,
		Matcher:       audioMatcher,
		TmpSandbox:    tmpSandbox,
		MusicSandbox:  musicSandbox,
		CookiesPath:   cfg.CookiesFile,
		MaxConcurrent: cfg.MaxConcurrentDownloads,
	})
	if err := downloaderFeatureSvc.Start(appCtx); err != nil {
		log.Printf("[Main] Warning: downloader vertical slice service failed to start: %v", err)
	}
	defer downloaderFeatureSvc.Close()
	downloaderModule := featureDownloader.NewModule(downloaderFeatureSvc, deezerClient)

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

	// Initialize Audit Vertical Feature Slice
	auditRepo := featureAudit.NewRepository(database.Conn())
	auditAdapter := &downloaderBackfillAdapter{svc: downloaderFeatureSvc}
	auditFeatureSvc := featureAudit.NewService(featureAudit.ServiceConfig{
		MusicSandbox:    musicSandbox,
		TmpSandbox:      tmpSandbox,
		Repo:            auditRepo,
		DB:              database.Conn(),
		JobStarter:      auditAdapter,
		BackfillStarter: auditAdapter,
		Organizer:       organizerService,
		Scanner:         scannerService,
		Lyrics:          lyricsService,
		Enricher:        sourceResolver.Enricher(),
		Deezer:          deezerClient,
		Yandex:          yandexClient,
		YTMusic:         ytmusicClient,
		ITunes:          itunesClient,
	})
	defer auditFeatureSvc.Close()
	auditModule := featureAudit.NewModule(auditFeatureSvc)

	// Initialize TagEditor Vertical Feature Slice
	tagRepo := featureTagEditor.NewRepository(database.Conn())
	tagFeatureSvc := featureTagEditor.NewService(featureTagEditor.ServiceConfig{
		MusicSandbox: musicSandbox,
		TmpSandbox:   tmpSandbox,
		Repo:         tagRepo,
		Organizer:    organizerService,
		Deezer:       deezerClient,
		MusicBrainz:  mbClient,
		Shazam:       shazamProvider,
		Lyrics:       lyricsService,
		Enricher:     sourceResolver.Enricher(),
		TokenStore:   tagRepo,
		MusicBrainzConf: featureTagEditor.MusicBrainzConfig{
			ClientID:     cfg.MusicBrainzClientID,
			ClientSecret: cfg.MusicBrainzClientSecret,
			RedirectURI:  cfg.MusicBrainzRedirectURI,
		},
		RedirectURI: cfg.MusicBrainzRedirectURI,
	})
	tagEditorModule := featureTagEditor.NewModule(tagFeatureSvc)

	// Initialize FileManager Vertical Feature Slice
	fileManagerRepo := featureFileManager.NewRepository(database.Conn())
	fileManagerFeatureSvc := featureFileManager.NewService(featureFileManager.ServiceConfig{
		MusicSandbox: musicSandbox,
		TmpSandbox:   tmpSandbox,
		Repo:         fileManagerRepo,
		Organizer:    &fileManagerOrganizerAdapter{sb: musicSandbox},
		Scanner:      &fileManagerScannerAdapter{scanner: scannerService, sb: musicSandbox},
		JobCreator:   &downloaderBackfillAdapter{svc: downloaderFeatureSvc},
		SourceFinder: &fileManagerSourceFinder{deezer: deezerClient, matcher: audioMatcher},
	})
	fileManagerModule := featureFileManager.NewModule(fileManagerFeatureSvc)

	// 7. Initialize Radar & Radio Services
	discographyAggregator := resolver.NewDiscographyAggregator(deezerClient, mbClient, itunesClient, database)
	lastfmClient := resolver.NewLastfmClient(cfg.LastfmAPIKey)
	similarityEngine := resolver.NewSimilarityEngine(deezerClient, lastfmClient, database)

	navidromeReader := radio.NewNavidromeReader(cfg.NavidromeDBPath)
	navidromeSubsonic := radio.NewNavidromeSubsonicClient(cfg.NavidromeURL, cfg.NavidromeUser, cfg.NavidromePassword)

	waveSelector := radio.NewWaveSelector(database, navidromeReader, similarityEngine)
	icyStreamer := radio.NewIcyStreamer(waveSelector, musicSandbox, database, cfg.RadioStreamToken)
	m3uGenerator := radio.NewM3UGenerator(waveSelector, musicSandbox, cfg.RadioWaveSize)

	radarService := radar.NewService(database, discographyAggregator, downloadService, cfg)
	radarService.Start(appCtx)

	// Daily 05:00 cron for «Моя Волна.m3u»
	go func() {
		// Generate once on startup in background if not generated
		if path, err := m3uGenerator.GenerateWaveM3U(appCtx); err == nil && path != "" {
			log.Printf("[Wave] Начальный плейлист «Моя Волна.m3u» сгенерирован: %s", path)
		}

		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), 5, 0, 0, 0, now.Location())
			if !next.After(now) {
				next = next.Add(24 * time.Hour)
			}
			select {
			case <-time.After(time.Until(next)):
				log.Println("[Wave] Обновление ежедневного плейлиста «Моя Волна.m3u»...")
				if path, err := m3uGenerator.GenerateWaveM3U(appCtx); err != nil {
					log.Printf("[Wave] Ошибка генерации M3U: %v", err)
				} else {
					log.Printf("[Wave] Плейлист «Моя Волна.m3u» обновлен: %s", path)
				}
			case <-appCtx.Done():
				return
			}
		}
	}()

	radarHandler := handlers.NewRadarHandler(radarService, database)
	radioHandler := handlers.NewRadioHandler(waveSelector, icyStreamer, m3uGenerator, navidromeSubsonic, navidromeReader, database, cfg)

	// Initialize Radio & Radar Vertical Feature Slices
	radioRepo := featureRadio.NewRepository(database.Conn())
	radioFeatureSvc := featureRadio.NewService(featureRadio.ServiceConfig{
		Selector:         waveSelector,
		Streamer:         icyStreamer,
		M3U:              m3uGenerator,
		Subsonic:         navidromeSubsonic,
		NavidromeReader:  navidromeReader,
		Repo:             radioRepo,
		RadioStreamToken: cfg.RadioStreamToken,
		SecretKey:        cfg.SecretKey,
		RadioWaveSize:    cfg.RadioWaveSize,
	})
	radioModule := featureRadio.NewModule(radioFeatureSvc)

	radarRepo := featureRadar.NewRepository(database.Conn())
	radarFeatureSvc := featureRadar.NewService(featureRadar.ServiceConfig{
		Repo:    radarRepo,
		Scanner: radarService,
	})
	radarModule := featureRadar.NewModule(radarFeatureSvc)

	// 8. Initialize Router
	router := api.NewRouter(api.RouterParams{
		Config:            cfg,
		AuthHandler:       authHandler,
		DownloadHandler:   downloadHandler,
		DownloaderModule:  downloaderModule,
		AuditModule:       auditModule,
		TagEditorModule:   tagEditorModule,
		FileManagerModule: fileManagerModule,
		RadioModule:       radioModule,
		RadarModule:       radarModule,
		TagHandler:        tagHandler,
		FileHandler:       fileHandler,
		LyricsHandler:     lyricsHandler,
		RadarHandler:      radarHandler,
		RadioHandler:      radioHandler,
	})

	// 9. Start HTTP Server
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

type downloaderBackfillAdapter struct {
	svc *featureDownloader.Service
}

func (a *downloaderBackfillAdapter) CreateJob(ctx context.Context, rawURL, source string) (string, error) {
	job, err := a.svc.CreateJob(ctx, rawURL, source)
	if err != nil {
		return "", err
	}
	return job.ID, nil
}

func (a *downloaderBackfillAdapter) CreateBackfillJob(
	ctx context.Context,
	targetAlbumPath, albumTitle, artistName, deezerAlbumID string,
	trackURLs []string,
	trackNumbers []int,
) (string, error) {
	job, err := a.svc.CreateBackfillJob(ctx, targetAlbumPath, albumTitle, artistName, deezerAlbumID, trackURLs, trackNumbers)
	if err != nil {
		return "", err
	}
	return job.ID, nil
}

type fileManagerOrganizerAdapter struct {
	sb *sandbox.PathSandbox
}

func (a *fileManagerOrganizerAdapter) RenameFile(oldRel, newRel string) ([]string, error) {
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

func (a *fileManagerOrganizerAdapter) SyncCompanions(oldBaseRel, newBaseRel string) error {
	return nil
}

type fileManagerScannerAdapter struct {
	scanner *scanner.Scanner
	sb      *sandbox.PathSandbox
}

func (a *fileManagerScannerAdapter) IndexFile(ctx context.Context, relPath string) error {
	full, err := a.sb.Resolve(relPath, false)
	if err != nil {
		return err
	}
	return a.scanner.IndexFile(full, relPath)
}

func (a *fileManagerScannerAdapter) RemoveFile(ctx context.Context, relPath string) error {
	return a.scanner.RemoveFile(relPath)
}

type fileManagerSourceFinder struct {
	deezer  *resolver.DeezerClient
	matcher *resolver.AudioMatcher
}

func (f *fileManagerSourceFinder) FindTrackURL(ctx context.Context, artist, title, album string, duration int) (string, error) {
	if f.deezer != nil && (artist != "" || title != "") {
		query := fmt.Sprintf(`artist:"%s" track:"%s"`, artist, title)
		tracks, err := f.deezer.SearchTracks(ctx, query)
		if err != nil || len(tracks) == 0 {
			tracks, err = f.deezer.SearchTracks(ctx, fmt.Sprintf("%s %s", artist, title))
		}
		if err == nil && len(tracks) > 0 {
			return fmt.Sprintf("https://www.deezer.com/track/%d", tracks[0].ID), nil
		}
	}
	if f.matcher != nil && (title != "" || artist != "") {
		ytURL, err := f.matcher.FindYouTubeMatch(ctx, title, artist, duration)
		if err == nil && ytURL != "" {
			return ytURL, nil
		}
	}
	return "", fmt.Errorf("source not found for track %s - %s", artist, title)
}

func (f *fileManagerSourceFinder) FindAlbumURL(ctx context.Context, artist, album string) (string, error) {
	if f.deezer != nil && (artist != "" || album != "") {
		query := fmt.Sprintf(`artist:"%s" album:"%s"`, artist, album)
		albums, err := f.deezer.SearchAlbums(ctx, query)
		if err != nil || len(albums) == 0 {
			albums, err = f.deezer.SearchAlbums(ctx, fmt.Sprintf("%s %s", artist, album))
		}
		if err == nil && len(albums) > 0 {
			return fmt.Sprintf("https://www.deezer.com/album/%d", albums[0].ID), nil
		}
	}
	return "", fmt.Errorf("source not found for album %s - %s", artist, album)
}



