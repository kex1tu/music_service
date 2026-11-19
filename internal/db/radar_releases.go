package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"music-manager/internal/core/textkey"
)

// RadarReleaseEntity представляет релиз из каталогов Радара или дискографии.
type RadarReleaseEntity struct {
	Tracklist       []CatalogTrack `json:"-"`
	ScannedArtistID int64          `json:"-"`
	ID              int64          `json:"id"`
	ArtistName      string         `json:"artist_name"`
	CleanArtistName string         `json:"clean_artist_name"`
	Title           string         `json:"title"`
	CleanTitle      string         `json:"clean_title"`
	ReleaseType     string         `json:"release_type"` // album, single, ep, compilation
	ReleaseDate     string         `json:"release_date"`
	Year            int            `json:"year"`
	CoverURL        string         `json:"cover_url"`
	TrackCount      int            `json:"track_count"`
	DeezerID        string         `json:"deezer_id,omitempty"`
	MusicBrainzID   string         `json:"musicbrainz_id,omitempty"`
	ShazamID        string         `json:"shazam_id,omitempty"`
	SourceURL       string         `json:"source_url"`
	Status          string         `json:"status"` // new, in_library, version_exists, downloaded, ignored, missing
	IsRadar         bool           `json:"is_radar"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
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
		r.CleanTitle = textkey.Search(r.Title)
	}
	if r.ReleaseType == "" {
		r.ReleaseType = "album"
	}
	if r.Status == "" {
		r.Status = "new"
	}
	var tracklist any
	if len(r.Tracklist) > 0 {
		raw, err := json.Marshal(r.Tracklist)
		if err != nil {
			return err
		}
		tracklist = string(raw)
	}
	key := RadarIdentityKey(r.ArtistName, r.Title, r.ReleaseType, r.DeezerID, r.MusicBrainzID, r.ShazamID)
	fallback := RadarIdentityKey(r.ArtistName, r.Title, r.ReleaseType, "", "", "")
	var writer interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	} = db.conn
	var tx *sql.Tx
	if r.ScannedArtistID > 0 {
		var err error
		tx, err = db.conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		writer = tx
	}
	// Credits from the provider are linked by the same SQLite statement.
	err := writer.QueryRowContext(ctx, `
		INSERT INTO radar_releases (artist_name, clean_artist_name, title, clean_title, release_type,
			release_date, year, cover_url, track_count, deezer_id, musicbrainz_id, shazam_id,
			source_url, status, is_radar, identity_key, tracklist)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15,
			CASE
				WHEN ?16 = ?18 THEN COALESCE((
					SELECT MIN(identity_key) FROM radar_releases
					WHERE clean_artist_name = ?2 AND search_title = music_search_key(?3)
						AND release_type = ?5 AND identity_key IS NOT NULL
					HAVING COUNT(*) = 1
				), ?16)
				WHEN NOT EXISTS (SELECT 1 FROM radar_releases WHERE identity_key = ?16)
					AND EXISTS (SELECT 1 FROM radar_releases WHERE identity_key = ?18)
				THEN ?18
				ELSE ?16
			END, ?17)
		ON CONFLICT(identity_key) DO UPDATE SET
			identity_key = CASE WHEN ?16 <> ?18 THEN ?16 ELSE radar_releases.identity_key END,
			artist_name = CASE WHEN length(excluded.artist_name) > length(radar_releases.artist_name)
				THEN excluded.artist_name ELSE radar_releases.artist_name END,
			clean_artist_name = CASE WHEN length(excluded.artist_name) > length(radar_releases.artist_name)
				THEN excluded.clean_artist_name ELSE radar_releases.clean_artist_name END,
			title = excluded.title, clean_title = excluded.clean_title,
			release_date = COALESCE(NULLIF(excluded.release_date, ''), radar_releases.release_date),
			year = COALESCE(NULLIF(excluded.year, 0), radar_releases.year),
			cover_url = COALESCE(NULLIF(excluded.cover_url, ''), radar_releases.cover_url),
			track_count = COALESCE(NULLIF(excluded.track_count, 0), radar_releases.track_count),
			deezer_id = COALESCE(NULLIF(excluded.deezer_id, ''), radar_releases.deezer_id),
			musicbrainz_id = COALESCE(NULLIF(excluded.musicbrainz_id, ''), radar_releases.musicbrainz_id),
			shazam_id = COALESCE(NULLIF(excluded.shazam_id, ''), radar_releases.shazam_id),
			tracklist = COALESCE(excluded.tracklist, radar_releases.tracklist),
			source_url = COALESCE(NULLIF(excluded.source_url, ''), radar_releases.source_url),
			status = CASE WHEN radar_releases.status IN ('downloaded', 'ignored')
				THEN radar_releases.status ELSE excluded.status END,
			is_radar = excluded.is_radar AND radar_releases.status NOT IN ('downloaded', 'ignored'),
			updated_at = CURRENT_TIMESTAMP
		RETURNING id, status, is_radar`,
		r.ArtistName, r.CleanArtistName, r.Title, r.CleanTitle, r.ReleaseType,
		r.ReleaseDate, r.Year, r.CoverURL, r.TrackCount, r.DeezerID, r.MusicBrainzID, r.ShazamID,
		r.SourceURL, r.Status, r.IsRadar && r.Status == "new", key, tracklist, fallback,
	).Scan(&r.ID, &r.Status, &r.IsRadar)
	if err != nil || tx == nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO radar_release_artists(release_id, credit_name, artist_id)
		SELECT ?, name, id FROM artists WHERE id = ? ON CONFLICT DO NOTHING`, r.ID, r.ScannedArtistID); err != nil {
		return err
	}
	return tx.Commit()
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
		conditions = append(conditions, "(instr(rr.clean_artist_name, ?) > 0 OR instr(rr.search_title, ?) > 0 OR instr(rr.search_clean_title, ?) > 0)")
		pattern := textkey.Search(search)
		args = append(args, pattern, pattern, pattern)
	}

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM radar_releases rr %s", whereClause)
	var total int
	if err := db.conn.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
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
	return QueryArtistDiscography(ctx, db.conn, artistName)
}

func QueryArtistDiscography(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, artistName string) ([]RadarReleaseEntity, error) {
	query := `
	WITH requested(id) AS (SELECT ` + ArtistIDLookup("?1") + `)
	SELECT
		rr.id, rr.artist_name, rr.clean_artist_name, rr.title, rr.clean_title,
		rr.release_type, COALESCE(rr.release_date, ''), COALESCE(rr.year, 0), COALESCE(rr.cover_url, ''),
		COALESCE(rr.track_count, 0), COALESCE(rr.deezer_id, ''), COALESCE(rr.musicbrainz_id, ''),
		COALESCE(rr.shazam_id, ''), rr.source_url, rr.status, rr.is_radar,
		rr.created_at, rr.updated_at
	FROM radar_releases rr CROSS JOIN requested
	WHERE EXISTS (SELECT 1 FROM radar_release_artists credit WHERE credit.release_id = rr.id AND credit.artist_id = requested.id)
	   OR (requested.id IS NULL AND music_radar_artist_matches(rr.artist_name, ?1))
	ORDER BY rr.year DESC, rr.release_date DESC;
	`
	rows, err := queryer.QueryContext(ctx, query, artistName)
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
