package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"music-manager/internal/core/textkey"
)

// RadarReleaseEntity представляет релиз из каталогов Радара или дискографии.
type RadarReleaseEntity struct {
	Tracklist           []CatalogTrack       `json:"-"`
	CatalogCredits      []string             `json:"-"`
	ProviderCredits     []RadarCredit        `json:"-"`
	MusicBrainzMetadata *RadarSourceMetadata `json:"-"`
	ID                  int64                `json:"id"`
	ArtistName          string               `json:"artist_name"`
	CleanArtistName     string               `json:"clean_artist_name"`
	Title               string               `json:"title"`
	CleanTitle          string               `json:"clean_title"`
	ReleaseType         string               `json:"release_type"` // album, single, ep, compilation
	ReleaseDate         string               `json:"release_date"`
	Year                int                  `json:"year"`
	CoverURL            string               `json:"cover_url"`
	TrackCount          int                  `json:"track_count"`
	DeezerID            string               `json:"deezer_id,omitempty"`
	MusicBrainzID       string               `json:"musicbrainz_id,omitempty"`
	ShazamID            string               `json:"shazam_id,omitempty"`
	SourceURL           string               `json:"source_url"`
	Status              string               `json:"status"` // new, in_library, version_exists, downloaded, ignored, missing
	IsRadar             bool                 `json:"is_radar"`
	CreatedAt           time.Time            `json:"created_at"`
	UpdatedAt           time.Time            `json:"updated_at"`
}

var ErrInvalidRadarStatus = errors.New("invalid radar release status")

func validRadarStatus(status string) bool {
	switch status {
	case "new", "in_library", "version_exists", "downloaded", "ignored", "missing":
		return true
	default:
		return false
	}
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
	if !validRadarStatus(r.Status) {
		return ErrInvalidRadarStatus
	}
	var tracklist any
	if len(r.Tracklist) > 0 {
		raw, err := json.Marshal(r.Tracklist)
		if err != nil {
			return err
		}
		tracklist = string(raw)
	}
	hasCatalogCredits := r.CatalogCredits != nil || r.ProviderCredits != nil ||
		(r.MusicBrainzMetadata != nil && r.MusicBrainzMetadata.Credits != nil)
	key := RadarIdentityKey(r.ArtistName, r.Title, r.ReleaseType, r.DeezerID, r.MusicBrainzID, r.ShazamID)
	fallback := RadarIdentityKey(r.ArtistName, r.Title, r.ReleaseType, "", "", "")
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	err = tx.QueryRowContext(ctx, `
		INSERT INTO radar_releases (artist_name, clean_artist_name, title, clean_title, release_type,
			release_date, year, cover_url, track_count, deezer_id, musicbrainz_id, shazam_id,
			source_url, status, is_radar, identity_key, tracklist, has_catalog_credits)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15,
			COALESCE(`+radarSourceMatch+`, CASE
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
			END), ?17, ?19)
		ON CONFLICT(identity_key) DO UPDATE SET
			identity_key = CASE WHEN (?10 <> '' OR ?12 <> '')
				AND (radar_releases.identity_key GLOB 'local:*' OR radar_releases.identity_key GLOB 'musicbrainz:*')
				THEN ?16 ELSE radar_releases.identity_key END,
			artist_name = CASE WHEN NOT `+radarGroupRefresh+` AND (?19 OR length(excluded.artist_name) > length(radar_releases.artist_name))
				THEN excluded.artist_name ELSE radar_releases.artist_name END,
			clean_artist_name = CASE WHEN NOT `+radarGroupRefresh+` AND (?19 OR length(excluded.artist_name) > length(radar_releases.artist_name))
				THEN excluded.clean_artist_name ELSE radar_releases.clean_artist_name END,
			title = CASE WHEN `+radarGroupRefresh+` THEN radar_releases.title ELSE excluded.title END,
			clean_title = CASE WHEN `+radarGroupRefresh+` THEN radar_releases.clean_title ELSE excluded.clean_title END,
			release_date = CASE WHEN `+radarGroupRefresh+` THEN radar_releases.release_date ELSE COALESCE(NULLIF(excluded.release_date, ''), radar_releases.release_date) END,
			year = CASE WHEN `+radarGroupRefresh+` THEN radar_releases.year ELSE COALESCE(NULLIF(excluded.year, 0), radar_releases.year) END,
			cover_url = CASE WHEN `+radarGroupRefresh+` THEN radar_releases.cover_url ELSE COALESCE(NULLIF(excluded.cover_url, ''), radar_releases.cover_url) END,
			track_count = CASE WHEN `+radarGroupRefresh+` THEN radar_releases.track_count ELSE COALESCE(NULLIF(excluded.track_count, 0), radar_releases.track_count) END,
			deezer_id = COALESCE(NULLIF(excluded.deezer_id, ''), radar_releases.deezer_id),
			musicbrainz_id = COALESCE(NULLIF(excluded.musicbrainz_id, ''), radar_releases.musicbrainz_id),
			shazam_id = COALESCE(NULLIF(excluded.shazam_id, ''), radar_releases.shazam_id),
			tracklist = CASE WHEN `+radarGroupRefresh+` THEN radar_releases.tracklist ELSE COALESCE(excluded.tracklist, radar_releases.tracklist) END,
			source_url = CASE WHEN `+radarGroupRefresh+` OR (COALESCE(radar_releases.deezer_id, '') <> '' AND excluded.deezer_id = '')
				THEN radar_releases.source_url ELSE COALESCE(NULLIF(excluded.source_url, ''), radar_releases.source_url) END,
			has_catalog_credits = MAX(excluded.has_catalog_credits, radar_releases.has_catalog_credits),
			status = CASE WHEN `+radarGroupRefresh+` OR radar_releases.status IN ('downloaded', 'ignored')
				THEN radar_releases.status ELSE excluded.status END,
			is_radar = CASE WHEN `+radarGroupRefresh+` THEN radar_releases.is_radar
				ELSE excluded.is_radar AND radar_releases.status NOT IN ('downloaded', 'ignored') END,
			updated_at = CURRENT_TIMESTAMP
		RETURNING id, status, is_radar`,
		r.ArtistName, r.CleanArtistName, r.Title, r.CleanTitle, r.ReleaseType,
		r.ReleaseDate, r.Year, r.CoverURL, r.TrackCount, r.DeezerID, r.MusicBrainzID, r.ShazamID,
		r.SourceURL, r.Status, r.IsRadar && r.Status == "new", key, tracklist, fallback, hasCatalogCredits,
		r.MusicBrainzMetadata != nil && r.MusicBrainzMetadata.MultipleEditions,
	).Scan(&r.ID, &r.Status, &r.IsRadar)
	if err != nil {
		return err
	}
	if err := saveRadarReleaseSources(ctx, tx, r, tracklist); err != nil {
		return err
	}
	if err := mergeRadarGroupPlaceholder(ctx, tx, r); err != nil {
		return err
	}
	if hasCatalogCredits || r.MusicBrainzID != "" {
		if err := refreshRadarCatalogCredits(ctx, tx, r); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetRadarFeed возвращает ленту новинок (is_radar = 1) с фильтрацией и пагинацией.
func (db *DB) GetRadarFeed(ctx context.Context, releaseType, search string, limit, offset int) ([]RadarReleaseEntity, int, error) {
	return QueryRadarFeed(ctx, db.conn, releaseType, search, limit, offset)
}

func QueryRadarFeed(ctx context.Context, conn interface {
	catalogQuerier
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, releaseType, search string, limit, offset int) ([]RadarReleaseEntity, int, error) {
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
	if err := conn.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	dataQuery := fmt.Sprintf(`
	SELECT
		rr.id, rr.artist_name, rr.clean_artist_name, rr.title, rr.clean_title,
		rr.release_type, COALESCE(rr.release_date, ''), COALESCE(rr.year, 0), COALESCE(rr.cover_url, ''),
		COALESCE(rr.track_count, 0), COALESCE(rr.deezer_id, ''), COALESCE(rr.musicbrainz_id, ''),
		COALESCE(rr.shazam_id, ''), rr.source_url, rr.status, rr.is_radar,
		rr.created_at, rr.updated_at
	FROM radar_releases rr
	%s
	ORDER BY rr.release_date DESC, rr.created_at DESC, rr.id
	LIMIT ? OFFSET ?;
	`, whereClause)

	queryArgs := append(args, limit, offset)
	rows, err := conn.QueryContext(ctx, dataQuery, queryArgs...)
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
}, artistName string, artistID ...int64) ([]RadarReleaseEntity, error) {
	var id int64
	if len(artistID) > 0 {
		id = artistID[0]
	}
	query := `
	WITH requested(id) AS (SELECT COALESCE(NULLIF(?2, 0), ` + ArtistIDLookup("?1") + `))
	SELECT
		rr.id, rr.artist_name, rr.clean_artist_name, rr.title, rr.clean_title,
		rr.release_type, COALESCE(rr.release_date, ''), COALESCE(rr.year, 0), COALESCE(rr.cover_url, ''),
		COALESCE(rr.track_count, 0), COALESCE(rr.deezer_id, ''), COALESCE(rr.musicbrainz_id, ''),
		COALESCE(rr.shazam_id, ''), rr.source_url, rr.status, rr.is_radar,
		rr.created_at, rr.updated_at
	FROM radar_releases rr CROSS JOIN requested
	WHERE EXISTS (SELECT 1 FROM radar_release_artists credit WHERE credit.release_id = rr.id AND credit.artist_id = requested.id)
	   OR (requested.id IS NULL AND NOT EXISTS(SELECT 1 FROM artists WHERE name = trim(?1) OR clean_name = music_search_key(?1))
	       AND ((rr.has_catalog_credits = 0 AND music_radar_artist_matches(rr.artist_name, ?1)) OR
	           (rr.has_catalog_credits = 1 AND EXISTS (
	               SELECT 1 FROM radar_release_artists credit WHERE credit.release_id = rr.id
	               AND music_search_key(credit.credit_name) = music_search_key(?1)
	           ))))
	ORDER BY rr.year DESC, rr.release_date DESC;
	`
	rows, err := queryer.QueryContext(ctx, query, artistName, id)
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
	return UpdateRadarReleaseStatus(ctx, db.conn, releaseID, status)
}

func UpdateRadarReleaseStatus(ctx context.Context, conn artistWriter, releaseID int64, status string) error {
	if !validRadarStatus(status) {
		return ErrInvalidRadarStatus
	}
	result, err := conn.ExecContext(ctx, `UPDATE radar_releases SET status = ?, is_radar = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, status == "new", releaseID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

// GetRadarUnreadCount returns the number of new entries in the feed.
func (db *DB) GetRadarUnreadCount(ctx context.Context) (int, error) {
	return QueryRadarUnreadCount(ctx, db.conn)
}

func QueryRadarUnreadCount(ctx context.Context, conn interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int, error) {
	var count int
	err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM radar_releases WHERE is_radar = 1 AND status = 'new'`).Scan(&count)
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
