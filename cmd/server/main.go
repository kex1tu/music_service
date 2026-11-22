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
	"music-manager/internal/core/mediaexec"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	featureAudit "music-manager/internal/features/audit"
	featureDownloader "music-manager/internal/features/downloader"
	featureFileManager "music-manager/internal/features/filemanager"
	featureRadar "music-manager/internal/features/radar"
	featureTagEditor "music-manager/internal/features/tageditor"
	"music-manager/internal/services/censorcheck"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/radar"
	"music-manager/internal/services/resolver"
	"music-manager/internal/services/scanner"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--prepare-sqlite-snapshot" {
		if len(os.Args) != 3 {
			log.Fatal("usage: music-manager --prepare-sqlite-snapshot DATABASE")
		}
		if err := db.PrepareOfflineSnapshot(context.Background(), os.Args[2]); err != nil {
			log.Fatal(err)
		}
		return
	}
	log.Println("[Main] Starting Music Manager (Go Backend)...")

	// 1. Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[Main] Configuration error: %v", err)
	}

	if err := mediaexec.ValidateRuntime(); err != nil {
		log.Printf("[Main] Downloads requiring yt-dlp are unavailable: %v", err)
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

	// 4. Initialize services and root context
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	deezerClient := resolver.NewDeezerClient()
	audioMatcher := resolver.NewAudioMatcher(cfg.CookiesFile)
	audioMatcher.SetConfig(cfg)
	audioMatcher.SetCatalogClients(deezerClient, resolver.NewITunesClient(nil))
	sourceResolver := resolver.NewSourceResolver(
		deezerClient,
		audioMatcher,
		cfg.CookiesFile,
		cfg.YandexMusicToken,
		cfg.YandexMusicProxy,
	)

	scannerService := scanner.NewScanner(musicSandbox, database, deezerClient, tmpSandbox)
	organizerService := organizer.NewService(musicSandbox, tmpSandbox)
	organizerService.SetTrackChanger(scannerService)
	lyricsService := lyrics.NewService(musicSandbox)
	yandexClient := resolver.NewYandexMusicClient(cfg.YandexMusicToken, cfg.YandexMusicProxy)
	yandexClient.SetSignKey(cfg.YandexLyricsSignKey)
	lyricsService.SetYandexClient(yandexClient)

	// Initialize Downloader Vertical Feature Slice
	downloaderRepo := featureDownloader.NewRepository(database.Conn())
	downloaderFeatureSvc := featureDownloader.NewService(featureDownloader.ServiceConfig{
		Repo:          downloaderRepo,
		Resolver:      sourceResolver,
		Matcher:       audioMatcher,
		Lyrics:        lyricsService,
		TmpSandbox:    tmpSandbox,
		MusicSandbox:  musicSandbox,
		CookiesPath:   cfg.CookiesFile,
		MaxConcurrent: cfg.MaxConcurrentDownloads,
	})
	if err := downloaderFeatureSvc.Start(appCtx); err != nil {
		log.Fatalf("[Main] Downloader service failed to start: %v", err)
	}
	defer downloaderFeatureSvc.Close()
	downloaderModule := featureDownloader.NewModule(downloaderFeatureSvc, deezerClient)

	if err := scannerService.RetryPending(appCtx); err != nil {
		log.Printf("[Main] Pending library index changes will be retried: %v", err)
	}

	go scannerService.RunIndexRecovery(appCtx)

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
	tagHandler := handlers.NewTagHandler(musicSandbox, tmpSandbox, organizerService, deezerClient, sourceResolver.Enricher())
	tagHandler.SetDB(database)

	ytmusicClient := resolver.NewYouTubeMusicClient("")
	itunesClient := resolver.NewITunesClient(nil)
	mbClient := resolver.NewMusicBrainzClient(cfg.MusicBrainzUserToken)
	mbClient.SetOAuthConfig(cfg.MusicBrainzClientID, cfg.MusicBrainzClientSecret, cfg.MusicBrainzRedirectURI)
	tagRepo := featureTagEditor.NewRepository(database.Conn())
	savedAccess, savedRefresh, savedUser, expiresAt, err := tagRepo.LoadMusicBrainzTokens(appCtx)
	if err != nil {
		log.Printf("[MusicBrainz] Failed to load persisted OAuth session: %v", err)
	} else if savedAccess != "" {
		mbClient.SetTokens(savedAccess, savedRefresh, expiresAt, savedUser)
		log.Printf("[MusicBrainz] Loaded persisted OAuth session for user: %s", savedUser)
	}
	shazamProvider := resolver.NewShazamProvider(itunesClient)

	tagHandler.SetConfig(cfg)
	tagHandler.SetMusicBrainz(mbClient)
	tagHandler.SetShazam(shazamProvider)

	lyricsHandler := handlers.NewLyricsHandler(lyricsService)

	// Initialize Audit Vertical Feature Slice
	clouds := map[string]censorcheck.Transcriber{}
	if cfg.CensorCloudEnabled {
		for _, p := range []struct{ name, key, model string }{{"gemini", cfg.GeminiAPIKey, cfg.GeminiModel}, {"deepgram", cfg.DeepgramAPIKey, cfg.DeepgramModel}} {
			if p.key == "" {
				continue
			}
			client, err := censorcheck.NewCloudClient(p.name, p.key, p.model, nil)
			if err != nil {
				log.Fatalf("[Main] Invalid censorship provider configuration: %s", p.name)
			}
			clouds[p.name] = client
		}
	}
	censorshipAudit, err := featureAudit.NewCensorshipAudit(database.Conn(), musicSandbox, clouds, cfg.CensorCloudDailySeconds)
	if err != nil {
		log.Fatalf("[Main] Failed to initialize censorship audit: %v", err)
	}
	auditRepo := featureAudit.NewRepository(database.Conn())
	auditAdapter := &downloaderBackfillAdapter{svc: downloaderFeatureSvc}
	auditFeatureSvc := featureAudit.NewService(featureAudit.ServiceConfig{
		Censorship:      censorshipAudit,
		MusicSandbox:    musicSandbox,
		TmpSandbox:      tmpSandbox,
		Repo:            auditRepo,
		DB:              database.Conn(),
		JobStarter:      auditAdapter,
		BackfillStarter: auditAdapter,
		Organizer:       organizerService,
		Scanner:         scannerService,
		TrackIndex:      scannerService,
		DeletionIndex:   scannerService,
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
	tagFeatureSvc := featureTagEditor.NewService(featureTagEditor.ServiceConfig{
		MusicSandbox: musicSandbox,
		TmpSandbox:   tmpSandbox,
		Repo:         tagRepo,
		TrackIndex:   scannerService,
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
		MusicSandbox:        musicSandbox,
		TmpSandbox:          tmpSandbox,
		Repo:                fileManagerRepo,
		Organizer:           &fileManagerOrganizerAdapter{sb: musicSandbox, index: scannerService},
		LibraryIndex:        scannerService,
		Scanner:             &fileManagerScannerAdapter{scanner: scannerService, sb: musicSandbox},
		JobCreator:          &downloaderBackfillAdapter{svc: downloaderFeatureSvc},
		SourceFinder:        &fileManagerSourceFinder{deezer: deezerClient, matcher: audioMatcher},
		RedownloadQueue:     downloaderFeatureSvc,
		RedownloadOrganizer: organizerService,
	})
	fileManagerModule := featureFileManager.NewModule(fileManagerFeatureSvc)

	// 7. Initialize Radar Services
	discographyAggregator := resolver.NewDiscographyAggregator(deezerClient, mbClient, itunesClient, database)

	radarService := radar.NewService(database, discographyAggregator, cfg)
	radarService.Start(appCtx)

	radarHandler := handlers.NewRadarHandler(radarService, database)

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
		DownloaderModule:  downloaderModule,
		AuditModule:       auditModule,
		TagEditorModule:   tagEditorModule,
		FileManagerModule: fileManagerModule,
		RadarModule:       radarModule,
		TagHandler:        tagHandler,
		LyricsHandler:     lyricsHandler,
		RadarHandler:      radarHandler,
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
	positions ...db.BackfillSelection,
) (string, error) {
	job, err := a.svc.CreateBackfillJob(ctx, targetAlbumPath, albumTitle, artistName, deezerAlbumID, trackURLs, trackNumbers, positions...)
	if err != nil {
		return "", err
	}
	return job.ID, nil
}

type fileManagerOrganizerAdapter struct {
	sb    *sandbox.PathSandbox
	index organizer.TrackChanger
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
	target, err := organizer.MoveIndexedTrack(context.Background(), a.index, oldFull, newFull, false, nil)
	if target == "" {
		return nil, err
	}
	relTarget, _ := a.sb.ToRelative(target)
	return []string{relTarget}, err
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
