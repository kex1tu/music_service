package tageditor

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/services/organizer"
)

var (
	// ErrFileNotFound возвращается, если файл не найден в медиатеке.
	ErrFileNotFound = errors.New("file not found in library")
	// ErrDirectoryNotFound возвращается, если директория не найдена в медиатеке.
	ErrDirectoryNotFound = errors.New("directory not found in library")
	// ErrInvalidPath возвращается, если передан пустой или некорректный путь.
	ErrInvalidPath = errors.New("invalid file or directory path")
	// ErrSandboxViolation возвращается при попытке выхода за пределы песочницы.
	ErrSandboxViolation = errors.New("path traversal attempt outside sandbox root")
	// ErrCoverNotFound возвращается, если обложка не найдена.
	ErrCoverNotFound = errors.New("cover art not found")
	// ErrEmbeddedLyricsProhibited фиксирует запрет на вшивание текстов в MP3.
	ErrEmbeddedLyricsProhibited = errors.New("embedded lyrics in MP3 are prohibited by project invariant")
	// ErrClientNotInitialized возвращается, если требуемый клиент не настроен.
	ErrClientNotInitialized = errors.New("requested external catalog provider is not initialized")
)

// ServiceConfig определяет конфигурацию и зависимости сервисного слоя TagEditor.
type ServiceConfig struct {
	MusicSandbox    *sandbox.PathSandbox
	TmpSandbox      *sandbox.PathSandbox
	Repo            TagRepository
	Organizer       PathOrganizer
	TrackIndex      organizer.TrackChanger
	Deezer          DeezerCatalog
	MusicBrainz     MusicBrainzCatalog
	Shazam          ShazamCatalog
	AcoustID        AcoustIDProvider
	Lyrics          LyricsService
	Enricher        ArtistEnricher
	TokenStore      TokenPersistence
	HTTPClient      *http.Client
	MusicBrainzConf MusicBrainzConfig
	RedirectURI     string
}

// Service реализует всю доменную логику управления тегами, обложками, каталогами и переименованиями.
type Service struct {
	musicSandbox *sandbox.PathSandbox
	tmpSandbox   *sandbox.PathSandbox
	repo         TagRepository
	organizer    PathOrganizer
	trackIndex   organizer.TrackChanger
	deezer       DeezerCatalog
	musicbrainz  MusicBrainzCatalog
	shazam       ShazamCatalog
	acoustid     AcoustIDProvider
	lyrics       LyricsService
	enricher     ArtistEnricher
	tokenStore   TokenPersistence
	httpClient   *http.Client
	mbConfig     MusicBrainzConfig

	writeMu  sync.Mutex
	dirLocks map[string]*sync.Mutex
}

// NewService создает полностью инициализированный и иммутабельный экземпляр Service.
func NewService(cfg ServiceConfig) *Service {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	mbConf := cfg.MusicBrainzConf
	if mbConf.RedirectURI == "" && cfg.RedirectURI != "" {
		mbConf.RedirectURI = cfg.RedirectURI
	}

	tokStore := cfg.TokenStore
	if tokStore == nil && cfg.Repo != nil {
		if ts, ok := cfg.Repo.(TokenPersistence); ok {
			tokStore = ts
		}
	}

	return &Service{
		musicSandbox: cfg.MusicSandbox,
		tmpSandbox:   cfg.TmpSandbox,
		repo:         cfg.Repo,
		organizer:    cfg.Organizer,
		trackIndex:   cfg.TrackIndex,
		deezer:       cfg.Deezer,
		musicbrainz:  cfg.MusicBrainz,
		shazam:       cfg.Shazam,
		acoustid:     cfg.AcoustID,
		lyrics:       cfg.Lyrics,
		enricher:     cfg.Enricher,
		tokenStore:   tokStore,
		httpClient:   client,
		mbConfig:     mbConf,
		dirLocks:     make(map[string]*sync.Mutex),
	}
}

// getDirLock возвращает мьютекс для синхронизации записи в конкретную папку.
func (s *Service) getDirLock(dir string) *sync.Mutex {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	mu, exists := s.dirLocks[dir]
	if !exists {
		mu = &sync.Mutex{}
		s.dirLocks[dir] = mu
	}
	return mu
}

// ResolvePath безопасно разрешает путь сначала в tmpSandbox (если файл там есть), затем в musicSandbox.
func (s *Service) ResolvePath(relPath string) (string, bool, error) {
	trimmed := strings.TrimSpace(relPath)
	if trimmed == "" {
		return "", false, ErrInvalidPath
	}

	cleanRel := strings.TrimPrefix(trimmed, "tmp_downloads/")
	cleanRel = strings.TrimPrefix(cleanRel, "tmp/")

	// 1. Попытка разрешить во временной песочнице
	if s.tmpSandbox != nil {
		if full, err := s.tmpSandbox.Resolve(cleanRel, true); err == nil {
			if st, statErr := safefs.Stat(full); statErr == nil && !st.IsDir() {
				return full, true, nil
			}
		}
	}

	// 2. Попытка разрешить в основной песочнице библиотеки
	if s.musicSandbox == nil {
		return "", false, ErrSandboxViolation
	}
	full, err := s.musicSandbox.Resolve(trimmed, true)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", ErrSandboxViolation, err)
	}
	return full, false, nil
}
