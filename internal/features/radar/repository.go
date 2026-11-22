package radar

import (
	"context"
	"database/sql"
	"music-manager/internal/db"
)

// DBTX определяет контракт выполнения SQL-запросов.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Repository определяет контракт доступа к данным Радара.
type Repository interface {
	GetRadarFeed(ctx context.Context, releaseType, search string, limit, offset int) ([]RadarRelease, int, error)
	GetRadarUnreadCount(ctx context.Context) (int, error)
	GetLibraryArtists(ctx context.Context) ([]TrackedArtist, error)
	GetTrackedArtistByName(ctx context.Context, name string) (*TrackedArtist, error)
	SetArtistLinks(ctx context.Context, id int64, name, alias, deezerID, mbID, yandexID string) error
	GetArtistDiscography(ctx context.Context, artistName string, artistID ...int64) ([]RadarRelease, error)
	SetRadarReleaseStatus(ctx context.Context, releaseID int64, status string) error
}

// SQLiteRepository реализует Repository для работы с базой SQLite.
type SQLiteRepository struct {
	db DBTX
}

// NewRepository создает новый экземпляр репозитория Radar.
func NewRepository(db DBTX) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

func (r *SQLiteRepository) SetArtistLinks(ctx context.Context, id int64, name, alias, deezerID, mbID, yandexID string) error {
	return db.SetLocalArtistLinks(ctx, r.db, id, name, alias, deezerID, mbID, yandexID)
}

func (r *SQLiteRepository) GetLibraryArtists(ctx context.Context) ([]TrackedArtist, error) {
	return db.QueryRadarLibraryArtists(ctx, r.db)
}

func (r *SQLiteRepository) GetTrackedArtistByName(ctx context.Context, name string) (*TrackedArtist, error) {
	artist, err := db.QueryTrackedArtist(ctx, r.db, name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return artist, err
}

// GetRadarFeed возвращает ленту новинок (is_radar = 1) с фильтрацией и пагинацией.
func (r *SQLiteRepository) GetRadarFeed(ctx context.Context, releaseType, search string, limit, offset int) ([]RadarRelease, int, error) {
	items, count, err := db.QueryRadarFeed(ctx, r.db, releaseType, search, limit, offset)
	if err == nil && items == nil {
		items = []RadarRelease{}
	}
	return items, count, err
}

func (r *SQLiteRepository) GetArtistDiscography(ctx context.Context, artistName string, artistID ...int64) ([]RadarRelease, error) {
	releases, err := db.QueryArtistDiscography(ctx, r.db, artistName, artistID...)
	if err == nil && releases == nil {
		releases = []RadarRelease{}
	}
	return releases, err
}

// SetRadarReleaseStatus меняет статус релиза.
func (r *SQLiteRepository) SetRadarReleaseStatus(ctx context.Context, releaseID int64, status string) error {
	return db.UpdateRadarReleaseStatus(ctx, r.db, releaseID, status)
}

func (r *SQLiteRepository) GetRadarUnreadCount(ctx context.Context) (int, error) {
	return db.QueryRadarUnreadCount(ctx, r.db)
}
