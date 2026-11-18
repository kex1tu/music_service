package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RadarReleaseEntity представляет релиз из каталогов Радара или дискографии.
type RadarReleaseEntity struct {
	ID              int64     `json:"id"`
	ArtistName      string    `json:"artist_name"`
	CleanArtistName string    `json:"clean_artist_name"`
	Title           string    `json:"title"`
	CleanTitle      string    `json:"clean_title"`
	ReleaseType     string    `json:"release_type"` // album, single, ep, compilation
	ReleaseDate     string    `json:"release_date"`
	Year            int       `json:"year"`
	CoverURL        string    `json:"cover_url"`
	TrackCount      int       `json:"track_count"`
	DeezerID        string    `json:"deezer_id,omitempty"`
	MusicBrainzID   string    `json:"musicbrainz_id,omitempty"`
	ShazamID        string    `json:"shazam_id,omitempty"`
	SourceURL       string    `json:"source_url"`
	Status          string    `json:"status"` // new, in_library, version_exists, downloaded, ignored, missing
	IsRadar         bool      `json:"is_radar"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// RadarReleaseItem является алиасом к RadarReleaseEntity.
type RadarReleaseItem = RadarReleaseEntity

// UpsertRadarRelease добавляет или обновляет релиз в таблице radar_releases.
func (db *DB) UpsertRadarRelease(ctx context.Context, r *RadarReleaseEntity) error {
	if r.ArtistName == "" || r.Title == "" {
		return fmt.Errorf("artist name and title are required")
	}
	r.CleanArtistName = CleanArtistKey(r.ArtistName)
	if r.CleanTitle == "" {
		r.CleanTitle = strings.ToLower(strings.TrimSpace(r.Title))
	}
	if r.ReleaseType == "" {
		r.ReleaseType = "album"
	}
	isRadarVal := 0
	if r.IsRadar {
		isRadarVal = 1
	}

	// 1. Дедупликация: если релиз с таким Deezer ID уже существует, обновляем его
	if r.DeezerID != "" {
		var existingID int64
		var existingArtist string
		err := db.conn.QueryRowContext(ctx, "SELECT id, artist_name FROM radar_releases WHERE deezer_id = ? LIMIT 1", r.DeezerID).Scan(&existingID, &existingArtist)
		if err == nil && existingID > 0 {
			finalArtist := r.ArtistName
			if len(existingArtist) > len(finalArtist) || (strings.Contains(existingArtist, ",") && !strings.Contains(finalArtist, ",")) {
				finalArtist = existingArtist
			}
			query := `
			UPDATE radar_releases SET
				artist_name = ?,
				title = ?,
				release_date = CASE WHEN ? != '' THEN ? ELSE release_date END,
				year = CASE WHEN ? > 0 THEN ? ELSE year END,
				cover_url = CASE WHEN ? != '' THEN ? ELSE cover_url END,
				track_count = CASE WHEN ? > 0 THEN ? ELSE track_count END,
				source_url = ?,
				status = CASE
					WHEN radar_releases.status IN ('downloaded', 'ignored') THEN radar_releases.status
					ELSE ?
				END,
				is_radar = CASE
					WHEN radar_releases.status IN ('downloaded', 'ignored') OR ? IN ('downloaded', 'ignored', 'in_library', 'version_exists', 'missing') THEN 0
					ELSE ?
				END,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?;
			`
			_, err = db.conn.ExecContext(ctx, query,
				finalArtist, r.Title,
				r.ReleaseDate, r.ReleaseDate,
				r.Year, r.Year,
				r.CoverURL, r.CoverURL,
				r.TrackCount, r.TrackCount,
				r.SourceURL,
				r.Status,
				r.Status, isRadarVal,
				existingID,
			)
			return err
		}
	}

	query := `
	INSERT INTO radar_releases (
		artist_name, clean_artist_name, title, clean_title, release_type,
		release_date, year, cover_url, track_count, deezer_id, musicbrainz_id,
		shazam_id, source_url, status, is_radar, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(clean_artist_name, clean_title, release_type) DO UPDATE SET
		artist_name = excluded.artist_name,
		title = excluded.title,
		release_date = CASE WHEN excluded.release_date != '' THEN excluded.release_date ELSE radar_releases.release_date END,
		year = CASE WHEN excluded.year > 0 THEN excluded.year ELSE radar_releases.year END,
		cover_url = CASE WHEN excluded.cover_url != '' THEN excluded.cover_url ELSE radar_releases.cover_url END,
		track_count = CASE WHEN excluded.track_count > 0 THEN excluded.track_count ELSE radar_releases.track_count END,
		deezer_id = CASE WHEN excluded.deezer_id != '' THEN excluded.deezer_id ELSE radar_releases.deezer_id END,
		musicbrainz_id = CASE WHEN excluded.musicbrainz_id != '' THEN excluded.musicbrainz_id ELSE radar_releases.musicbrainz_id END,
		shazam_id = CASE WHEN excluded.shazam_id != '' THEN excluded.shazam_id ELSE radar_releases.shazam_id END,
		source_url = excluded.source_url,
		status = CASE
			WHEN radar_releases.status IN ('downloaded', 'ignored') THEN radar_releases.status
			ELSE excluded.status
		END,
		is_radar = CASE
			WHEN radar_releases.status IN ('downloaded', 'ignored') OR excluded.status IN ('downloaded', 'ignored', 'in_library', 'version_exists', 'missing') THEN 0
			ELSE excluded.is_radar
		END,
		updated_at = CURRENT_TIMESTAMP;
	`
	_, err := db.conn.ExecContext(ctx, query,
		r.ArtistName, r.CleanArtistName, r.Title, r.CleanTitle, r.ReleaseType,
		r.ReleaseDate, r.Year, r.CoverURL, r.TrackCount, r.DeezerID, r.MusicBrainzID,
		r.ShazamID, r.SourceURL, r.Status, isRadarVal,
	)
	return err
}

// GetRadarFeed возвращает ленту новинок (is_radar = 1) с фильтрацией и пагинацией.
func (db *DB) GetRadarFeed(ctx context.Context, releaseType, search string, limit, offset int) ([]RadarReleaseEntity, int, error) {
	if limit <= 0 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}

	var conditions []string
	var args []any

	// Показываем строго нескачанные новинки (is_radar = 1, status = 'new')
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
	if err := db.conn.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
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
	rows, err := db.conn.QueryContext(ctx, dataQuery, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []RadarReleaseEntity
	for rows.Next() {
		var r RadarReleaseEntity
		var isRadarInt int
		err := rows.Scan(
			&r.ID, &r.ArtistName, &r.CleanArtistName, &r.Title, &r.CleanTitle,
			&r.ReleaseType, &r.ReleaseDate, &r.Year, &r.CoverURL,
			&r.TrackCount, &r.DeezerID, &r.MusicBrainzID,
			&r.ShazamID, &r.SourceURL, &r.Status, &isRadarInt,
			&r.CreatedAt, &r.UpdatedAt,
		)
		if err != nil {
			return nil, 0, err
		}
		r.IsRadar = isRadarInt == 1
		result = append(result, r)
	}

	return result, total, rows.Err()
}

// GetArtistDiscography возвращает все релизы дискографии артиста (включая пробелы и присутствующие на диске).
func (db *DB) GetArtistDiscography(ctx context.Context, artistName string) ([]RadarReleaseEntity, error) {
	cleanName := CleanArtistKey(artistName)
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
	rows, err := db.conn.QueryContext(ctx, query, cleanName, pattern, rawPattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []RadarReleaseEntity
	for rows.Next() {
		var r RadarReleaseEntity
		var isRadarInt int
		err := rows.Scan(
			&r.ID, &r.ArtistName, &r.CleanArtistName, &r.Title, &r.CleanTitle,
			&r.ReleaseType, &r.ReleaseDate, &r.Year, &r.CoverURL,
			&r.TrackCount, &r.DeezerID, &r.MusicBrainzID,
			&r.ShazamID, &r.SourceURL, &r.Status, &isRadarInt,
			&r.CreatedAt, &r.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		r.IsRadar = isRadarInt == 1
		result = append(result, r)
	}
	return result, rows.Err()
}

// SetRadarReleaseStatus меняет статус релиза (например: 'ignored', 'downloaded', 'new').
func (db *DB) SetRadarReleaseStatus(ctx context.Context, releaseID int64, status string) error {
	isRadar := 0
	if status == "new" {
		isRadar = 1
	}
	query := `UPDATE radar_releases SET status = ?, is_radar = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;`
	_, err := db.conn.ExecContext(ctx, query, status, isRadar, releaseID)
	return err
}

// GetRadarUnreadCount возвращает количество нескачанных и нескрытых новинок Радара.
func (db *DB) GetRadarUnreadCount(ctx context.Context) (int, error) {
	query := `SELECT COUNT(*) FROM radar_releases WHERE is_radar = 1 AND status = 'new';`
	var count int
	err := db.conn.QueryRowContext(ctx, query).Scan(&count)
	return count, err
}

// GetRadarReleaseByID возвращает релиз по числовому ID.
func (db *DB) GetRadarReleaseByID(ctx context.Context, id int64) (*RadarReleaseEntity, error) {
	query := `
	SELECT
		rr.id, rr.artist_name, rr.clean_artist_name, rr.title, rr.clean_title,
		rr.release_type, COALESCE(rr.release_date, ''), rr.year, COALESCE(rr.cover_url, ''),
		rr.track_count, COALESCE(rr.deezer_id, ''), COALESCE(rr.musicbrainz_id, ''),
		COALESCE(rr.shazam_id, ''), rr.source_url, rr.status, rr.is_radar,
		rr.created_at, rr.updated_at
	FROM radar_releases rr
	WHERE rr.id = ?
	LIMIT 1;
	`
	var r RadarReleaseEntity
	var isRadarInt int
	err := db.conn.QueryRowContext(ctx, query, id).Scan(
		&r.ID, &r.ArtistName, &r.CleanArtistName, &r.Title, &r.CleanTitle,
		&r.ReleaseType, &r.ReleaseDate, &r.Year, &r.CoverURL,
		&r.TrackCount, &r.DeezerID, &r.MusicBrainzID,
		&r.ShazamID, &r.SourceURL, &r.Status, &isRadarInt,
		&r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	r.IsRadar = isRadarInt == 1
	return &r, nil
}
