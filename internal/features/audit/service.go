package audit

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
)

var (
	ErrFolderNotFound      = errors.New("folder not found in library")
	ErrFileNotFound        = errors.New("file not found in library")
	ErrAlbumNotFound       = errors.New("album not found")
	ErrBatchAlreadyRunning = errors.New("batch operation is already running")
	ErrSandboxViolation    = errors.New("path traversal attempt outside sandbox root")
	ErrInvalidRequest      = errors.New("invalid request parameters")
)

// ServiceConfig defines dependencies for constructing the Audit service.
type ServiceConfig struct {
	Censorship      *CensorshipAudit
	MusicSandbox    *sandbox.PathSandbox
	TmpSandbox      *sandbox.PathSandbox
	Repo            AuditRepository
	DB              DBTX
	JobStarter      JobStarter
	BackfillStarter BackfillStarter
	Organizer       PathOrganizer
	Scanner         LibraryScanner
	TrackIndex      organizer.TrackChanger
	DeletionIndex   LibraryDeletionIndex
	Lyrics          LyricsService
	Enricher        ArtistEnricher
	Deezer          *resolver.DeezerClient
	Yandex          *resolver.YandexMusicClient
	YTMusic         *resolver.YouTubeMusicClient
	ITunes          *resolver.ITunesClient
	HTTPClient      *http.Client
}

// Service encapsulates library integrity auditing, duplicate detection and resolution,
// path reorganization, tag health auditing, batch lyrics/cover workers, and catalog matching.
type Service struct {
	censorship      *CensorshipAudit
	musicSandbox    *sandbox.PathSandbox
	tmpSandbox      *sandbox.PathSandbox
	repo            AuditRepository
	db              DBTX
	jobStarter      JobStarter
	backfillStarter BackfillStarter
	organizer       PathOrganizer
	scanner         LibraryScanner
	trackIndex      organizer.TrackChanger
	deletionIndex   LibraryDeletionIndex
	lyrics          LyricsService
	enricher        ArtistEnricher
	deezer          *resolver.DeezerClient
	yandex          *resolver.YandexMusicClient
	ytmusic         *resolver.YouTubeMusicClient
	itunes          *resolver.ITunesClient
	httpClient      *http.Client

	// Lifecycle and concurrency controls
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Background batch lyrics worker state
	lyricsMu     sync.RWMutex
	lyricsState  *BatchProgress
	lyricsCancel context.CancelFunc

	// Background batch covers worker state
	coversMu     sync.RWMutex
	coversState  BatchProgress
	coversCancel context.CancelFunc
}

// NewService creates a new Service instance.
func NewService(cfg ServiceConfig) *Service {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		censorship:      cfg.Censorship,
		musicSandbox:    cfg.MusicSandbox,
		tmpSandbox:      cfg.TmpSandbox,
		repo:            cfg.Repo,
		db:              cfg.DB,
		jobStarter:      cfg.JobStarter,
		backfillStarter: cfg.BackfillStarter,
		organizer:       cfg.Organizer,
		scanner:         cfg.Scanner,
		trackIndex:      cfg.TrackIndex,
		deletionIndex:   cfg.DeletionIndex,
		lyrics:          cfg.Lyrics,
		enricher:        cfg.Enricher,
		deezer:          cfg.Deezer,
		yandex:          cfg.Yandex,
		ytmusic:         cfg.YTMusic,
		itunes:          cfg.ITunes,
		httpClient:      client,
		ctx:             ctx,
		cancel:          cancel,
		lyricsState:     &BatchProgress{},
	}
}

// Close gracefully stops any active background batch operations and waits for completion.
func (s *Service) Close() error {
	if s.censorship != nil {
		s.censorship.Close()
	}
	s.cancel()

	s.lyricsMu.Lock()
	if s.lyricsCancel != nil {
		s.lyricsCancel()
	}
	s.lyricsMu.Unlock()

	s.coversMu.Lock()
	if s.coversCancel != nil {
		s.coversCancel()
	}
	s.coversMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("audit service graceful shutdown timed out")
	}
}
