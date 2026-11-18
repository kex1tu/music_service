package downloader

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/resolver"
)

// ErrInvalidURL возвращается при недопустимом или потенциально опасном URL.
var ErrInvalidURL = errors.New("untrusted or invalid target_url")

var (
	errSelectionConflict = errors.New("укажите задачу для выбора источника или дождитесь завершения обработки")
	errQueueStorage      = errors.New("не удалось сохранить задачу в очереди")
)

// JobStarter определяет потребительский интерфейс для постановки задач другими модулями (radar, audit).
type JobStarter interface {
	CreateJob(ctx context.Context, rawURL, source string) (*Job, error)
}

// ServiceConfig определяет зависимости и параметры конфигурации сервиса загрузок.
type ServiceConfig struct {
	Repo            *Repository
	Resolver        *resolver.SourceResolver
	Matcher         *resolver.AudioMatcher
	Lyrics          *lyrics.Service
	TmpSandbox      *sandbox.PathSandbox
	MusicSandbox    *sandbox.PathSandbox
	CookiesPath     string
	MaxConcurrent   int
	CleanerMaxAge   time.Duration
	CleanerInterval time.Duration
}

// Service координирует жизненный цикл задач загрузки, воркер-пул, дебаунс-синхронизацию с SQLite и файловую песочницу.
type Service struct {
	repo         *Repository
	resolver     *resolver.SourceResolver
	matcher      *resolver.AudioMatcher
	lyrics       *lyrics.Service
	tmpSandbox   *sandbox.PathSandbox
	musicSandbox *sandbox.PathSandbox
	cookiesPath  string

	// Bounded worker pool
	workersCount int
	wake         chan struct{}
	wg           sync.WaitGroup
	ctx          context.Context
	cancel       context.CancelFunc
	stopped      int32

	// Управление паузой очереди
	pauseMu  sync.RWMutex
	isPaused bool

	// In-memory реестр задач
	jobsMu sync.RWMutex
	jobs   map[string]*Job

	// Дебаунс синхронизации с базой данных
	timersMu   sync.Mutex
	syncTimers map[string]*time.Timer

	// In-memory кэш разрешения источников (5-минутный TTL)
	resolveCacheMu sync.RWMutex
	resolveCache   map[string]*cachedResolveResult

	client          *http.Client
	cleanerMaxAge   time.Duration
	cleanerInterval time.Duration
}

// NewService создает новый экземпляр Service без запуска фоновых горутин.
func NewService(cfg ServiceConfig) *Service {
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	cleanerAge := cfg.CleanerMaxAge
	if cleanerAge <= 0 {
		cleanerAge = 24 * time.Hour
	}
	cleanerInt := cfg.CleanerInterval
	if cleanerInt <= 0 {
		cleanerInt = 1 * time.Hour
	}

	matcher := cfg.Matcher
	if matcher == nil && cfg.Resolver != nil {
		matcher = cfg.Resolver.Matcher()
	}

	svc := &Service{
		repo:            cfg.Repo,
		resolver:        cfg.Resolver,
		matcher:         matcher,
		lyrics:          cfg.Lyrics,
		tmpSandbox:      cfg.TmpSandbox,
		musicSandbox:    cfg.MusicSandbox,
		cookiesPath:     cfg.CookiesPath,
		workersCount:    maxConcurrent,
		wake:            make(chan struct{}, maxConcurrent),
		jobs:            make(map[string]*Job),
		syncTimers:      make(map[string]*time.Timer),
		resolveCache:    make(map[string]*cachedResolveResult),
		client:          &http.Client{Timeout: 30 * time.Second},
		cleanerMaxAge:   cleanerAge,
		cleanerInterval: cleanerInt,
	}
	return svc
}

// SetLyricsService переопределяет сервис текстов песен (для тестирования или отложенного внедрения).
func (s *Service) SetLyricsService(l *lyrics.Service) {
	s.lyrics = l
}

// Resolver возвращает SourceResolver.
func (s *Service) Resolver() *resolver.SourceResolver {
	return s.resolver
}

// Repo возвращает Repository.
func (s *Service) Repo() *Repository {
	return s.repo
}

// TmpSandbox возвращает песочницу временных файлов.
func (s *Service) TmpSandbox() *sandbox.PathSandbox {
	return s.tmpSandbox
}

// MusicSandbox возвращает песочницу библиотеки музыки.
func (s *Service) MusicSandbox() *sandbox.PathSandbox {
	return s.musicSandbox
}

// CookiesPath возвращает путь к файлу cookies.
func (s *Service) CookiesPath() string {
	return s.cookiesPath
}
