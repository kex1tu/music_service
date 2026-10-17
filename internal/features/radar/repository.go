package radar

import (
	"context"
	"database/sql"
	"fmt"
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

func cleanKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// UpsertTrackedArtist добавляет артиста на отслеживание или обновляет метку времени.
func (r *SQLiteRepository) UpsertTrackedArtist(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("artist name cannot be empty")
	}
	cleanName := cleanKey(name)
	query := `
	INSERT INTO tracked_artists (name, clean_name, is_followed, updated_at)
	VALUES (?, ?, 1, CURRENT_TIMESTAMP)
	ON CONFLICT(name) DO UPDATE SET
		clean_name = excluded.clean_name,
		updated_at = CURRENT_TIMESTAMP;
	`
	_, err := r.db.ExecContext(ctx, query, name, cleanName)
	return err
}

// GetTrackedArtists возвращает список всех отслеживаемых артистов со статистикой пробелов.
func (r *SQLiteRepository) GetTrackedArtists(ctx context.Context, onlyFollowed bool) ([]TrackedArtist, error) {
	whereClause := ""
	if onlyFollowed {
		whereClause = "WHERE ta.is_followed = 1"
	}

	query := fmt.Sprintf(`
	SELECT 
		ta.id, ta.name, ta.clean_name, ta.is_followed, ta.auto_download,
		COALESCE(ta.custom_search_alias, ''),
		COALESCE(ta.deezer_id, ''),
		COALESCE(ta.musicbrainz_id, ''),
		COALESCE(ta.yandex_id, ''),
		ta.last_checked_at,
		COALESCE(al_cnt.cnt, 0) as album_count,
		COALESCE(tr_cnt.cnt, 0) as track_count,
		COALESCE(gap_cnt.cnt, 0) as gap_count,
		ta.created_at, ta.updated_at
	FROM tracked_artists ta
	LEFT JOIN (
		SELECT ar.clean_name, COUNT(DISTINCT a.id) as cnt
		FROM artists ar
		JOIN albums a ON (a.artist_id = ar.id OR a.id IN (SELECT album_id FROM tracks WHERE artist_id = ar.id))
		GROUP BY ar.clean_name
	) al_cnt ON al_cnt.clean_name = ta.clean_name
	LEFT JOIN (
		SELECT ar.clean_name, COUNT(t.id) as cnt
		FROM artists ar
		JOIN tracks t ON t.artist_id = ar.id
		GROUP BY ar.clean_name
	) tr_cnt ON tr_cnt.clean_name = ta.clean_name
	LEFT JOIN (
		SELECT clean_artist_name, COUNT(*) as cnt
		FROM radar_releases
		WHERE status = 'missing'
		GROUP BY clean_artist_name
	) gap_cnt ON gap_cnt.clean_artist_name = ta.clean_name
	%s
	ORDER BY ta.name COLLATE NOCASE ASC;
	`, whereClause)

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TrackedArtist
	for rows.Next() {
		var a TrackedArtist
		var isFollowedInt, autoDownloadInt int
		var lastChecked sql.NullTime

		err := rows.Scan(
			&a.ID, &a.Name, &a.CleanName, &isFollowedInt, &autoDownloadInt,
			&a.CustomSearchAlias, &a.DeezerID, &a.MusicBrainzID, &a.YandexID,
			&lastChecked, &a.AlbumCount, &a.TrackCount, &a.GapCount, &a.CreatedAt, &a.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		a.IsFollowed = isFollowedInt == 1
		a.AutoDownload = autoDownloadInt == 1
		if lastChecked.Valid {
			a.LastCheckedAt = &lastChecked.Time
		}
		result = append(result, a)
	}
	if result == nil {
		result = []TrackedArtist{}
	}
	return result, rows.Err()
}

// GetTrackedArtistByName находит запись артиста по его имени.
func (r *SQLiteRepository) GetTrackedArtistByName(ctx context.Context, name string) (*TrackedArtist, error) {
	cleanName := cleanKey(name)
	query := `
	SELECT 
		ta.id, ta.name, ta.clean_name, ta.is_followed, ta.auto_download,
		COALESCE(ta.custom_search_alias, ''),
		COALESCE(ta.deezer_id, ''),
		COALESCE(ta.musicbrainz_id, ''),
		COALESCE(ta.yandex_id, ''),
		ta.last_checked_at,
		COALESCE(al_cnt.cnt, 0) as album_count,
		COALESCE(tr_cnt.cnt, 0) as track_count,
		COALESCE(gap_cnt.cnt, 0) as gap_count,
		ta.created_at, ta.updated_at
	FROM tracked_artists ta
	LEFT JOIN (
		SELECT ar.clean_name, COUNT(DISTINCT a.id) as cnt
		FROM artists ar
		JOIN albums a ON (a.artist_id = ar.id OR a.id IN (SELECT album_id FROM tracks WHERE artist_id = ar.id))
		GROUP BY ar.clean_name
	) al_cnt ON al_cnt.clean_name = ta.clean_name
	LEFT JOIN (
		SELECT ar.clean_name, COUNT(t.id) as cnt
		FROM artists ar
		JOIN tracks t ON t.artist_id = ar.id
		GROUP BY ar.clean_name
	) tr_cnt ON tr_cnt.clean_name = ta.clean_name
	LEFT JOIN (
		SELECT clean_artist_name, COUNT(*) as cnt
		FROM radar_releases
		WHERE status = 'missing'
		GROUP BY clean_artist_name
	) gap_cnt ON gap_cnt.clean_artist_name = ta.clean_name
	WHERE ta.clean_name = ?
	LIMIT 1;
	`
	var a TrackedArtist
	var isFollowedInt, autoDownloadInt int
	var lastChecked sql.NullTime

	err := r.db.QueryRowContext(ctx, query, cleanName).Scan(
		&a.ID, &a.Name, &a.CleanName, &isFollowedInt, &autoDownloadInt,
		&a.CustomSearchAlias, &a.DeezerID, &a.MusicBrainzID, &a.YandexID,
		&lastChecked, &a.AlbumCount, &a.TrackCount, &a.GapCount, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	a.IsFollowed = isFollowedInt == 1
	a.AutoDownload = autoDownloadInt == 1
	if lastChecked.Valid {
		a.LastCheckedAt = &lastChecked.Time
	}
	return &a, nil
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
		conditions = append(conditions, "(rr.artist_name LIKE ? OR rr.title LIKE ?)")
		pattern := "%" + search + "%"
		args = append(args, pattern, pattern)
	}

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	countQuery := fmt.Sprintf("SELECT COUNT(DISTINCT COALESCE(NULLIF(rr.deezer_id, ''), rr.clean_title)) FROM radar_releases rr %s", whereClause)
	var total int
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	dataQuery := fmt.Sprintf(`
	SELECT 
		MIN(rr.id), rr.artist_name, rr.clean_artist_name, rr.title, rr.clean_title,
		rr.release_type, COALESCE(rr.release_date, ''), rr.year, COALESCE(rr.cover_url, ''),
		rr.track_count, COALESCE(rr.deezer_id, ''), COALESCE(rr.musicbrainz_id, ''),
		COALESCE(rr.shazam_id, ''), rr.source_url, rr.status, rr.is_radar,
		rr.created_at, rr.updated_at
	FROM radar_releases rr
	%s
	GROUP BY COALESCE(NULLIF(rr.deezer_id, ''), rr.clean_title)
	ORDER BY rr.release_date DESC, rr.created_at DESC
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

// GetArtistDiscography возвращает все релизы дискографии артиста.
func (r *SQLiteRepository) GetArtistDiscography(ctx context.Context, artistName string) ([]RadarRelease, error) {
	cleanName := cleanKey(artistName)
	query := `
	SELECT 
		MIN(rr.id), rr.artist_name, rr.clean_artist_name, rr.title, rr.clean_title,
		rr.release_type, COALESCE(rr.release_date, ''), rr.year, COALESCE(rr.cover_url, ''),
		rr.track_count, COALESCE(rr.deezer_id, ''), COALESCE(rr.musicbrainz_id, ''),
		COALESCE(rr.shazam_id, ''), rr.source_url, rr.status, rr.is_radar,
		rr.created_at, rr.updated_at
	FROM radar_releases rr
	WHERE rr.clean_artist_name = ? OR rr.clean_artist_name LIKE ? OR rr.artist_name LIKE ?
	GROUP BY COALESCE(NULLIF(rr.deezer_id, ''), rr.clean_title)
	ORDER BY rr.year DESC, rr.release_date DESC;
	`
	pattern := "%" + cleanName + "%"
	rawPattern := "%" + artistName + "%"
	rows, err := r.db.QueryContext(ctx, query, cleanName, pattern, rawPattern)
	if err != nil {
		return nil, err
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
			return nil, err
		}
		rel.IsRadar = isRadarInt == 1
		result = append(result, rel)
	}
	if result == nil {
		result = []RadarRelease{}
	}
	return result, rows.Err()
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
