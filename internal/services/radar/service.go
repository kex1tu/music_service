package radar

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"music-manager/internal/config"
	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
)

// ScanProgress отражает текущее состояние фонового сканирования релизов Радара.
type ScanProgress struct {
	IsRunning        bool      `json:"is_running"`
	CurrentIndex     int       `json:"current_index"`
	TotalArtists     int       `json:"total_artists"`
	CurrentArtist    string    `json:"current_artist"`
	StartedAt        time.Time `json:"started_at"`
	NewReleasesFound int       `json:"new_releases_found"`
	LastError        string    `json:"last_error,omitempty"`
}

// ScanArtistResult содержит результаты сканирования дискографии одного исполнителя.
type ScanArtistResult struct {
	ArtistName      string                       `json:"artist_name"`
	TotalDiscovered int                          `json:"total_discovered"`
	NewReleases     int                          `json:"new_releases"`
	Releases        []resolver.AggregatedRelease `json:"releases"`
}

// Service отвечает за регулярный опрос каталогов, обнаружение пробелов и новинок Радара.
type Service struct {
	db         *db.DB
	aggregator *resolver.DiscographyAggregator
	cfg        *config.Config

	mu   sync.RWMutex
	scan *scanRun
}

type scanRun struct {
	cancel   context.CancelFunc
	done     chan struct{}
	progress ScanProgress
}

// NewService создает новый сервис Release Radar & Gap Detector.
func NewService(
	database *db.DB,
	aggregator *resolver.DiscographyAggregator,
	cfg *config.Config,
) *Service {
	return &Service{
		db:         database,
		aggregator: aggregator,
		cfg:        cfg,
	}
}

// Start запускает фоновый периодический воркер сканирования (каждые N часов, по умолчанию 24ч).
func (s *Service) Start(ctx context.Context) {
	intervalHours := 24
	if s.cfg != nil && s.cfg.RadarScanIntervalHours > 0 {
		intervalHours = s.cfg.RadarScanIntervalHours
	}

	// Асинхронная актуализация существующих записей в radar_releases при старте
	go func() {
		if count, err := s.db.ReconcileAllRadarReleases(ctx); err == nil && count > 0 {
			log.Printf("[Radar] Стартовая актуализация: скрыто %d ранее скачанных релизов из ленты", count)
		}
	}()

	ticker := time.NewTicker(time.Duration(intervalHours) * time.Hour)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				if done := s.cancelCurrentScan(); done != nil {
					<-done
				}
				return
			case <-ticker.C:
				log.Printf("[Radar] Запуск планового сканирования релизов (интервал %dч)...", intervalHours)
				if err := s.ScanAll(ctx, "periodic_cron"); err != nil {
					log.Printf("[Radar] Плановое сканирование пропущено: %v", err)
				}
			}
		}
	}()
}

// GetProgress возвращает снимок прогресса текущего сканирования.
func (s *Service) GetProgress() ScanProgress {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.scan == nil {
		return ScanProgress{}
	}
	return s.scan.progress
}

// CancelScan прерывает текущее фоновое сканирование.
func (s *Service) CancelScan() {
	s.cancelCurrentScan()
}

func (s *Service) cancelCurrentScan() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scan == nil {
		return nil
	}
	s.scan.cancel()
	s.scan.progress.IsRunning = false
	return s.scan.done
}

// ScanAll запускает процесс обхода всех артистов медиатеки с задержкой 1.5 сек между запросами.
func (s *Service) ScanAll(_ context.Context, trigger string) error {
	s.mu.Lock()
	if s.scan != nil && s.scan.progress.IsRunning {
		s.mu.Unlock()
		return errors.New("сканирование уже выполняется")
	}

	// Background scans outlive the HTTP request that started them.
	scanCtx, cancel := context.WithCancel(context.Background())
	run := &scanRun{
		cancel: cancel,
		done:   make(chan struct{}),
		progress: ScanProgress{
			IsRunning:     true,
			StartedAt:     time.Now(),
			CurrentArtist: "Сбор списка исполнителей...",
		},
	}
	s.scan = run
	s.mu.Unlock()

	go func() {
		defer func() {
			defer close(run.done)
			defer cancel()
			if scanCtx.Err() == nil {
				if count, err := s.db.ReconcileAllRadarReleases(scanCtx); err == nil && count > 0 {
					log.Printf("[Radar] Пост-сканирование: скрыто %d ранее скачанных релизов из ленты", count)
				}
			}
			s.mu.Lock()
			run.progress.IsRunning = false
			newCount := run.progress.NewReleasesFound
			s.mu.Unlock()
			log.Printf("[Radar] Сканирование завершено (триггер: %s, найдено новинок: %d)", trigger, newCount)
		}()

		artists, err := s.db.GetRadarLibraryArtists(scanCtx)
		if scanCtx.Err() != nil {
			return
		}
		if err != nil {
			s.mu.Lock()
			run.progress.LastError = err.Error()
			s.mu.Unlock()
			log.Printf("[Radar] Ошибка получения списка артистов: %v", err)
			return
		}

		s.mu.Lock()
		run.progress.TotalArtists = len(artists)
		s.mu.Unlock()

		for i, artist := range artists {
			select {
			case <-scanCtx.Done():
				log.Printf("[Radar] Сканирование прервано по контексту")
				return
			default:
			}

			s.mu.Lock()
			run.progress.CurrentIndex = i + 1
			run.progress.CurrentArtist = artist.Name
			s.mu.Unlock()

			res, err := s.scanArtist(scanCtx, artist.Name, &artist)
			if scanCtx.Err() != nil {
				return
			}
			if err != nil {
				s.mu.Lock()
				run.progress.LastError = err.Error()
				s.mu.Unlock()
				log.Printf("[Radar] Ошибка сканирования дискографии %q: %v", artist.Name, err)
			} else {
				s.mu.Lock()
				run.progress.NewReleasesFound += res.NewReleases
				s.mu.Unlock()
			}

			// Paced Worker: задержка 1500 мс во избежание 429 Too Many Requests
			select {
			case <-time.After(1500 * time.Millisecond):
			case <-scanCtx.Done():
				return
			}
		}
	}()

	return nil
}

// ScanArtist опрашивает внешние каталоги, склеивает дискографию и сохраняет новинки артиста в базу.
func (s *Service) ScanArtist(ctx context.Context, artistName string) (*ScanArtistResult, error) {
	artistName = strings.TrimSpace(artistName)
	if artistName == "" {
		return nil, fmt.Errorf("artist name cannot be empty")
	}

	tracked, _ := s.db.GetTrackedArtistByName(ctx, artistName)
	return s.scanArtist(ctx, artistName, tracked)
}

func (s *Service) ScanArtistByID(ctx context.Context, artistID int64) (*ScanArtistResult, error) {
	tracked, err := s.db.GetTrackedArtistByID(ctx, artistID)
	if err != nil {
		return nil, err
	}
	return s.scanArtist(ctx, tracked.Name, &tracked)
}

func (s *Service) scanArtist(ctx context.Context, artistName string, tracked *db.TrackedArtistEntity) (*ScanArtistResult, error) {
	var alias string
	knownIDs := make(map[string]string)
	if tracked != nil {
		alias = tracked.CustomSearchAlias
		if tracked.DeezerID != "" {
			knownIDs["deezer"] = tracked.DeezerID
		}
		if tracked.MusicBrainzID != "" {
			knownIDs["musicbrainz"] = tracked.MusicBrainzID
		}
	}

	releases, err := s.aggregator.Aggregate(ctx, artistName, alias, knownIDs)
	if err != nil {
		return nil, fmt.Errorf("aggregation failed for %q: %w", artistName, err)
	}

	newCount := 0
	for i, rel := range releases {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entity := db.RadarReleaseEntity{
			CatalogCredits:      rel.CatalogCredits,
			ProviderCredits:     rel.ProviderCredits,
			MusicBrainzMetadata: rel.MusicBrainzMetadata,
			ArtistName:          rel.ArtistName,
			CleanArtistName:     rel.CleanArtist,
			Title:               rel.Title,
			CleanTitle:          rel.CleanTitle,
			ReleaseType:         rel.ReleaseType,
			ReleaseDate:         rel.ReleaseDate,
			Year:                rel.Year,
			CoverURL:            rel.CoverURL,
			TrackCount:          rel.TrackCount,
			DeezerID:            rel.DeezerID,
			MusicBrainzID:       rel.MusicBrainzID,
			ShazamID:            rel.ShazamID,
			SourceURL:           rel.SourceURL,
			Status:              rel.Status,
			IsRadar:             rel.IsRadar,
			Tracklist:           rel.Tracklist,
		}

		if err := s.db.UpsertRadarRelease(ctx, &entity); err != nil {
			log.Printf("[Radar] Ошибка сохранения релиза %q - %q: %v", rel.ArtistName, rel.Title, err)
			continue
		}

		releases[i].Status, releases[i].IsRadar = entity.Status, entity.IsRadar
		if entity.Status == "new" {
			newCount++
		}
	}

	if tracked != nil {
		if err := s.db.UpdateArtistLastCheckedByID(ctx, tracked.ID); err != nil {
			log.Printf("[Radar] Ошибка обновления времени проверки %q: %v", artistName, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return &ScanArtistResult{
		ArtistName:      artistName,
		TotalDiscovered: len(releases),
		NewReleases:     newCount,
		Releases:        releases,
	}, nil
}

// UpdateArtistCustomLinks сохраняет кастомные ID и поисковый псевдоним.
func (s *Service) UpdateArtistCustomLinks(ctx context.Context, artistName, alias, deezerID, mbID, yandexID string) error {
	return s.db.UpdateArtistCustomLinks(ctx, artistName, alias, deezerID, mbID, yandexID)
}
