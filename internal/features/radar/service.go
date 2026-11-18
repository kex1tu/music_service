package radar

import (
	"context"
	"fmt"

	serviceRadar "music-manager/internal/services/radar"
)

// ScannerEngine определяет контракт фонового сканера и агрегатора релизов.
type ScannerEngine interface {
	ScanArtist(ctx context.Context, artistName string) (*serviceRadar.ScanArtistResult, error)
	ScanAll(ctx context.Context, trigger string) error
	GetProgress() serviceRadar.ScanProgress
	CancelScan()
	FollowArtist(ctx context.Context, artistName string, followed bool) error
	SetAutoDownload(ctx context.Context, artistName string, auto bool) error
	UpdateArtistCustomLinks(ctx context.Context, artistName, alias, deezerID, mbID, yandexID string) error
}

// ServiceConfig определяет зависимости для инициализации сервиса Radar.
type ServiceConfig struct {
	Repo    Repository
	Scanner ScannerEngine
}

// Service инкапсулирует бизнес-логику Release Radar, отслеживания артистов и поиска пробелов в дискографии.
type Service struct {
	repo    Repository
	scanner ScannerEngine
}

// NewService создает новый экземпляр Service.
func NewService(cfg ServiceConfig) *Service {
	return &Service{
		repo:    cfg.Repo,
		scanner: cfg.Scanner,
	}
}

// GetFeed возвращает ленту релизов Радара с фильтрами и пагинацией.
func (s *Service) GetFeed(ctx context.Context, relType, search string, limit, offset int) ([]RadarRelease, int, error) {
	if s.repo == nil {
		return []RadarRelease{}, 0, nil
	}
	return s.repo.GetRadarFeed(ctx, relType, search, limit, offset)
}

// GetUnreadCount возвращает количество непросмотренных новинок Радара.
func (s *Service) GetUnreadCount(ctx context.Context) (int, error) {
	if s.repo == nil {
		return 0, nil
	}
	return s.repo.GetRadarUnreadCount(ctx)
}

// GetTrackedArtists возвращает список всех исполнителей в каталоге отслеживания.
func (s *Service) GetTrackedArtists(ctx context.Context, onlyFollowed bool) ([]TrackedArtist, error) {
	if s.repo == nil {
		return []TrackedArtist{}, nil
	}
	return s.repo.GetTrackedArtists(ctx, onlyFollowed)
}

// AddTrackedArtist добавляет артиста на отслеживание вручную.
func (s *Service) AddTrackedArtist(ctx context.Context, name string) error {
	if s.repo == nil {
		return fmt.Errorf("repository unavailable")
	}
	return s.repo.UpsertTrackedArtist(ctx, name)
}

// FollowArtist включает или отключает отслеживание артиста.
func (s *Service) FollowArtist(ctx context.Context, name string, followed bool, artistID int64) error {
	if artistID > 0 && s.repo != nil {
		return s.repo.SetArtistFollowed(ctx, artistID, followed)
	}
	if s.scanner == nil {
		return fmt.Errorf("scanner engine unavailable")
	}
	return s.scanner.FollowArtist(ctx, name, followed)
}

// SetAutoDownload включает или отключает автозагрузку новых релизов артиста.
func (s *Service) SetAutoDownload(ctx context.Context, name string, auto bool, artistID int64) error {
	if artistID > 0 && s.repo != nil {
		return s.repo.SetArtistAutoDownload(ctx, artistID, auto)
	}
	if s.scanner == nil {
		return fmt.Errorf("scanner engine unavailable")
	}
	return s.scanner.SetAutoDownload(ctx, name, auto)
}

// UpdateCustomLinks обновляет пользовательские идентификаторы артиста в каталогах.
func (s *Service) UpdateCustomLinks(ctx context.Context, name, alias, deezerID, mbID, yandexID string, artistID int64) error {
	if artistID > 0 && s.repo != nil {
		return s.repo.SetArtistLinks(ctx, artistID, alias, deezerID, mbID, yandexID)
	}
	if s.scanner == nil {
		return fmt.Errorf("scanner engine unavailable")
	}
	return s.scanner.UpdateArtistCustomLinks(ctx, name, alias, deezerID, mbID, yandexID)
}

// GetArtistDiscography возвращает дискографию артиста с маркировкой статусов релизов.
func (s *Service) GetArtistDiscography(ctx context.Context, name string) ([]RadarRelease, *TrackedArtist, error) {
	if s.repo == nil {
		return []RadarRelease{}, nil, nil
	}
	releases, err := s.repo.GetArtistDiscography(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	tracked, _ := s.repo.GetTrackedArtistByName(ctx, name)
	return releases, tracked, nil
}

// ScanArtist запускает обнаружение новинок для одного конкретного артиста.
func (s *Service) ScanArtist(ctx context.Context, artistName string) (*serviceRadar.ScanArtistResult, error) {
	if s.scanner == nil {
		return nil, fmt.Errorf("scanner engine unavailable")
	}
	return s.scanner.ScanArtist(ctx, artistName)
}

// ScanAll запускает пакетное сканирование медиатеки.
func (s *Service) ScanAll(ctx context.Context, trigger string) error {
	if s.scanner == nil {
		return fmt.Errorf("scanner engine unavailable")
	}
	return s.scanner.ScanAll(ctx, trigger)
}

// GetProgress возвращает текущий статус фонового сканирования.
func (s *Service) GetProgress() serviceRadar.ScanProgress {
	if s.scanner == nil {
		return serviceRadar.ScanProgress{}
	}
	return s.scanner.GetProgress()
}

// CancelScan прерывает текущее сканирование.
func (s *Service) CancelScan() {
	if s.scanner != nil {
		s.scanner.CancelScan()
	}
}

// SetReleaseStatus изменяет статус релиза ('ignored', 'downloaded', 'new').
func (s *Service) SetReleaseStatus(ctx context.Context, releaseID int64, status string) error {
	if s.repo == nil {
		return fmt.Errorf("repository unavailable")
	}
	return s.repo.SetRadarReleaseStatus(ctx, releaseID, status)
}
