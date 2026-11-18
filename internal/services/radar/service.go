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
	"music-manager/internal/services/downloader"
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
	downloader *downloader.Service
	cfg        *config.Config

	mu         sync.RWMutex
	progress   ScanProgress
	cancelScan context.CancelFunc
}

// NewService создает новый сервис Release Radar & Gap Detector.
func NewService(
	database *db.DB,
	aggregator *resolver.DiscographyAggregator,
	dl *downloader.Service,
	cfg *config.Config,
) *Service {
	return &Service{
		db:         database,
		aggregator: aggregator,
		downloader: dl,
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
				s.CancelScan()
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
	return s.progress
}

// CancelScan прерывает текущее фоновое сканирование.
func (s *Service) CancelScan() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelScan != nil {
		s.cancelScan()
		s.cancelScan = nil
	}
	s.progress.IsRunning = false
}

// ScanAll запускает процесс обхода всех отслеживаемых артистов с задержкой 1.5 сек между запросами.
func (s *Service) ScanAll(parentCtx context.Context, trigger string) error {
	s.mu.Lock()
	if s.progress.IsRunning {
		s.mu.Unlock()
		return errors.New("сканирование уже выполняется")
	}

	scanCtx, cancel := context.WithCancel(context.Background())
	s.cancelScan = cancel
	s.progress = ScanProgress{
		IsRunning:        true,
		StartedAt:        time.Now(),
		CurrentIndex:     0,
		TotalArtists:     0,
		CurrentArtist:    "Сбор списка исполнителей...",
		NewReleasesFound: 0,
		LastError:        "",
	}
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.progress.IsRunning = false
			s.cancelScan = nil
			s.mu.Unlock()
			if count, err := s.db.ReconcileAllRadarReleases(context.Background()); err == nil && count > 0 {
				log.Printf("[Radar] Пост-сканирование: скрыто %d ранее скачанных релизов из ленты", count)
			}
			log.Printf("[Radar] Сканирование завершено (триггер: %s, найдено новинок: %d)", trigger, s.GetProgress().NewReleasesFound)
		}()

		artists, err := s.db.GetTrackedArtists(scanCtx, true)
		if err != nil {
			s.mu.Lock()
			s.progress.LastError = err.Error()
			s.mu.Unlock()
			log.Printf("[Radar] Ошибка получения списка артистов: %v", err)
			return
		}

		if len(artists) == 0 {
			// Если нет явно подписанных, сканируем всех отслеживаемых артистов библиотеки
			artists, err = s.db.GetTrackedArtists(scanCtx, false)
			if err != nil {
				s.mu.Lock()
				s.progress.LastError = err.Error()
				s.mu.Unlock()
				return
			}
		}

		s.mu.Lock()
		s.progress.TotalArtists = len(artists)
		s.mu.Unlock()

		for i, artist := range artists {
			select {
			case <-scanCtx.Done():
				log.Printf("[Radar] Сканирование прервано по контексту")
				return
			default:
			}

			s.mu.Lock()
			s.progress.CurrentIndex = i + 1
			s.progress.CurrentArtist = artist.Name
			s.mu.Unlock()

			res, err := s.ScanArtist(scanCtx, artist.Name)
			if err != nil {
				log.Printf("[Radar] Ошибка сканирования дискографии %q: %v", artist.Name, err)
			} else if res != nil && res.NewReleases > 0 {
				s.mu.Lock()
				s.progress.NewReleasesFound += res.NewReleases
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

	tracked, err := s.db.GetTrackedArtistByName(ctx, artistName)
	var alias string
	knownIDs := make(map[string]string)
	if err == nil && tracked != nil {
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
	for _, rel := range releases {
		entity := db.RadarReleaseEntity{
			ArtistName:      rel.ArtistName,
			CleanArtistName: rel.CleanArtist,
			Title:           rel.Title,
			CleanTitle:      rel.CleanTitle,
			ReleaseType:     rel.ReleaseType,
			ReleaseDate:     rel.ReleaseDate,
			Year:            rel.Year,
			CoverURL:        rel.CoverURL,
			TrackCount:      rel.TrackCount,
			DeezerID:        rel.DeezerID,
			MusicBrainzID:   rel.MusicBrainzID,
			ShazamID:        rel.ShazamID,
			SourceURL:       rel.SourceURL,
			Status:          rel.Status,
			IsRadar:         rel.IsRadar,
			Tracklist:       rel.Tracklist,
		}

		if err := s.db.UpsertRadarRelease(ctx, &entity); err != nil {
			log.Printf("[Radar] Ошибка сохранения релиза %q - %q: %v", rel.ArtistName, rel.Title, err)
			continue
		}

		if entity.Status == "new" {
			newCount++
			// Проверяем возможность автозагрузки
			autoDownload := false
			if tracked != nil && tracked.AutoDownload {
				autoDownload = true
			} else if s.cfg != nil && s.cfg.RadarAutoDownload {
				autoDownload = true
			}

			if autoDownload && s.downloader != nil && rel.SourceURL != "" {
				log.Printf("[Radar] Автозагрузка нового релиза: %s — %s (%s)", rel.ArtistName, rel.Title, rel.SourceURL)
				if _, dlErr := s.downloader.CreateJob(rel.SourceURL, "auto"); dlErr != nil {
					log.Printf("[Radar] Не удалось создать задание автозагрузки: %v", dlErr)
				}
			}
		}
	}

	if tracked != nil {
		if err := s.db.UpdateArtistLastCheckedByID(ctx, tracked.ID); err != nil {
			log.Printf("[Radar] Ошибка обновления времени проверки %q: %v", artistName, err)
		}
	}

	return &ScanArtistResult{
		ArtistName:      artistName,
		TotalDiscovered: len(releases),
		NewReleases:     newCount,
		Releases:        releases,
	}, nil
}

// FollowArtist включает/выключает отслеживание артиста.
func (s *Service) FollowArtist(ctx context.Context, artistName string, followed bool) error {
	return s.db.SetArtistFollowed(ctx, artistName, followed)
}

// SetAutoDownload включает/выключает автозагрузку релизов артиста.
func (s *Service) SetAutoDownload(ctx context.Context, artistName string, auto bool) error {
	return s.db.SetArtistAutoDownload(ctx, artistName, auto)
}

// UpdateArtistCustomLinks сохраняет кастомные ID и поисковый псевдоним.
func (s *Service) UpdateArtistCustomLinks(ctx context.Context, artistName, alias, deezerID, mbID, yandexID string) error {
	return s.db.UpdateArtistCustomLinks(ctx, artistName, alias, deezerID, mbID, yandexID)
}
