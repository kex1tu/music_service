package radar

import (
	"context"
	"database/sql"
	"fmt"
	"music-manager/internal/core/textkey"
	"music-manager/internal/db"
	"strings"
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
	GetTrackedArtists(ctx context.Context, onlyFollowed bool) ([]TrackedArtist, error)
	GetTrackedArtistByName(ctx context.Context, name string) (*TrackedArtist, error)
	UpsertTrackedArtist(ctx context.Context, name string) error
	SetArtistFollowed(ctx context.Context, id int64, followed bool) error
	SetArtistAutoDownload(ctx context.Context, id int64, auto bool) error
	SetArtistLinks(ctx context.Context, id int64, alias, deezerID, mbID, yandexID string) error
	GetArtistDiscography(ctx context.Context, artistName string) ([]RadarRelease, error)
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

// UpsertTrackedArtist добавляет артиста на отслеживание или обновляет метку времени.
func (r *SQLiteRepository) UpsertTrackedArtist(ctx context.Context, name string) error {
	return db.RegisterTrackedArtist(ctx, r.db, name)
}

func (r *SQLiteRepository) SetArtistFollowed(ctx context.Context, id int64, followed bool) error {
	return db.SetTrackedArtistFollowed(ctx, r.db, id, followed)
}

func (r *SQLiteRepository) SetArtistAutoDownload(ctx context.Context, id int64, auto bool) error {
	return db.SetTrackedArtistAutoDownload(ctx, r.db, id, auto)
}

func (r *SQLiteRepository) SetArtistLinks(ctx context.Context, id int64, alias, deezerID, mbID, yandexID string) error {
	return db.SetTrackedArtistLinks(ctx, r.db, id, alias, deezerID, mbID, yandexID)
}

func (r *SQLiteRepository) GetTrackedArtists(ctx context.Context, onlyFollowed bool) ([]TrackedArtist, error) {
	return db.QueryTrackedArtists(ctx, r.db, onlyFollowed)
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
	if limit <= 0 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}

	var conditions []string
	var args []any

	conditions = append(conditions, "rr.is_radar = 1", "rr.status = 'new'")

	if releaseType != "" && releaseType != "all" {
		conditions = append(conditions, "rr.release_type = ?")
		args = append(args, releaseType)
	}

	search = strings.TrimSpace(search)
	if search != "" {
		conditions = append(conditions, "(instr(rr.clean_artist_name, ?) > 0 OR instr(rr.search_title, ?) > 0 OR instr(rr.search_clean_title, ?) > 0)")
		pattern := textkey.Search(search)
		args = append(args, pattern, pattern, pattern)
	}

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM radar_releases rr %s", whereClause)
	var total int
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	dataQuery := fmt.Sprintf(`
	SELECT 
		rr.id, rr.artist_name, rr.clean_artist_name, rr.title, rr.clean_title,
		rr.release_type, COALESCE(rr.release_date, ''), rr.year, COALESCE(rr.cover_url, ''),
		rr.track_count, COALESCE(rr.deezer_id, ''), COALESCE(rr.musicbrainz_id, ''),
		COALESCE(rr.shazam_id, ''), rr.source_url, rr.status, rr.is_radar,
		rr.created_at, rr.updated_at
	FROM radar_releases rr
	%s
	ORDER BY rr.release_date DESC, rr.created_at DESC, rr.id
	LIMIT ? OFFSET ?;
	`, whereClause)

	queryArgs := append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, dataQuery, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []RadarRelease
	for rows.Next() {
		var rel RadarRelease
		var isRadarInt int
		err := rows.Scan(
			&rel.ID, &rel.ArtistName, &rel.CleanArtistName, &rel.Title, &rel.CleanTitle,
			&rel.ReleaseType, &rel.ReleaseDate, &rel.Year, &rel.CoverURL,
			&rel.TrackCount, &rel.DeezerID, &rel.MusicBrainzID,
			&rel.ShazamID, &rel.SourceURL, &rel.Status, &isRadarInt,
			&rel.CreatedAt, &rel.UpdatedAt,
		)
		if err != nil {
			return nil, 0, err
		}
		rel.IsRadar = isRadarInt == 1
		result = append(result, rel)
	}
	if result == nil {
		result = []RadarRelease{}
	}

	return result, total, rows.Err()
}

func (r *SQLiteRepository) GetArtistDiscography(ctx context.Context, artistName string) ([]RadarRelease, error) {
	releases, err := db.QueryArtistDiscography(ctx, r.db, artistName)
	if err == nil && releases == nil {
		releases = []RadarRelease{}
	}
	return releases, err
}

// SetRadarReleaseStatus меняет статус релиза.
func (r *SQLiteRepository) SetRadarReleaseStatus(ctx context.Context, releaseID int64, status string) error {
	isRadar := 0
	if status == "new" {
		isRadar = 1
	}
	query := `UPDATE radar_releases SET status = ?, is_radar = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;`
	_, err := r.db.ExecContext(ctx, query, status, isRadar, releaseID)
	return err
}

// GetRadarUnreadCount возвращает количество непросмотренных новинок Радара.
func (r *SQLiteRepository) GetRadarUnreadCount(ctx context.Context) (int, error) {
	query := `SELECT COUNT(*) FROM radar_releases WHERE is_radar = 1 AND status = 'new';`
	var count int
	err := r.db.QueryRowContext(ctx, query).Scan(&count)
	return count, err
}
