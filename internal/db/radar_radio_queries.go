package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// TrackedArtistEntity представляет персистентную запись отслеживаемого артиста.
type TrackedArtistEntity struct {
	ID                int64      `json:"id"`
	Name              string     `json:"name"`
	CleanName         string     `json:"clean_name"`
	IsFollowed        bool       `json:"is_followed"`
	AutoDownload      bool       `json:"auto_download"`
	CustomSearchAlias string     `json:"custom_search_alias,omitempty"`
	DeezerID          string     `json:"deezer_id,omitempty"`
	MusicBrainzID     string     `json:"musicbrainz_id,omitempty"`
	YandexID          string     `json:"yandex_id,omitempty"`
	LastCheckedAt     *time.Time `json:"last_checked_at,omitempty"`
	AlbumCount        int        `json:"album_count"`
	GapCount          int        `json:"gap_count"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

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

// ArtistRelationItem представляет связь между двумя артистами в графе похожести.
type ArtistRelationItem struct {
	ArtistName  string  `json:"artist_name"`
	RelatedName string  `json:"related_name"`
	Score       float64 `json:"score"`
	Source      string  `json:"source"`
}

// CleanArtistKey приводит имя исполнителя к стандартному нижнему регистру для индексов.
func CleanArtistKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// UpsertTrackedArtist добавляет артиста на отслеживание или обновляет метку времени.
func (db *DB) UpsertTrackedArtist(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("artist name cannot be empty")
	}
	cleanName := CleanArtistKey(name)
	query := `
	INSERT INTO tracked_artists (name, clean_name, is_followed, updated_at)
	VALUES (?, ?, 1, CURRENT_TIMESTAMP)
	ON CONFLICT(name) DO UPDATE SET
		clean_name = excluded.clean_name,
		updated_at = CURRENT_TIMESTAMP;
	`
	_, err := db.conn.ExecContext(ctx, query, name, cleanName)
	return err
}

// GetTrackedArtists возвращает список всех отслеживаемых артистов со статистикой пробелов.
func (db *DB) GetTrackedArtists(ctx context.Context, onlyFollowed bool) ([]TrackedArtistEntity, error) {
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
		COALESCE((SELECT COUNT(DISTINCT a.id) FROM albums a WHERE LOWER(a.album_artist) = ta.clean_name OR LOWER(a.artist_id) = (SELECT id FROM artists WHERE LOWER(name) = ta.clean_name)), 0) as album_count,
		COALESCE((SELECT COUNT(*) FROM radar_releases rr WHERE rr.clean_artist_name = ta.clean_name AND rr.status = 'missing'), 0) as gap_count,
		ta.created_at, ta.updated_at
	FROM tracked_artists ta
	%s
	ORDER BY ta.name COLLATE NOCASE ASC;
	`, whereClause)

	rows, err := db.conn.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TrackedArtistEntity
	for rows.Next() {
		var a TrackedArtistEntity
		var isFollowedInt, autoDownloadInt int
		var lastChecked sql.NullTime

		err := rows.Scan(
			&a.ID, &a.Name, &a.CleanName, &isFollowedInt, &autoDownloadInt,
			&a.CustomSearchAlias, &a.DeezerID, &a.MusicBrainzID, &a.YandexID,
			&lastChecked, &a.AlbumCount, &a.GapCount, &a.CreatedAt, &a.UpdatedAt,
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
	return result, rows.Err()
}

// GetTrackedArtistByName находит запись артиста по его имени.
func (db *DB) GetTrackedArtistByName(ctx context.Context, name string) (*TrackedArtistEntity, error) {
	cleanName := CleanArtistKey(name)
	query := `
	SELECT 
		ta.id, ta.name, ta.clean_name, ta.is_followed, ta.auto_download,
		COALESCE(ta.custom_search_alias, ''),
		COALESCE(ta.deezer_id, ''),
		COALESCE(ta.musicbrainz_id, ''),
		COALESCE(ta.yandex_id, ''),
		ta.last_checked_at,
		COALESCE((SELECT COUNT(DISTINCT a.id) FROM albums a WHERE LOWER(a.album_artist) = ta.clean_name), 0) as album_count,
		COALESCE((SELECT COUNT(*) FROM radar_releases rr WHERE rr.clean_artist_name = ta.clean_name AND rr.status = 'missing'), 0) as gap_count,
		ta.created_at, ta.updated_at
	FROM tracked_artists ta
	WHERE ta.clean_name = ?
	LIMIT 1;
	`
	var a TrackedArtistEntity
	var isFollowedInt, autoDownloadInt int
	var lastChecked sql.NullTime

	err := db.conn.QueryRowContext(ctx, query, cleanName).Scan(
		&a.ID, &a.Name, &a.CleanName, &isFollowedInt, &autoDownloadInt,
		&a.CustomSearchAlias, &a.DeezerID, &a.MusicBrainzID, &a.YandexID,
		&lastChecked, &a.AlbumCount, &a.GapCount, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	a.IsFollowed = isFollowedInt == 1
	a.AutoDownload = autoDownloadInt == 1
	if lastChecked.Valid {
		a.LastCheckedAt = &lastChecked.Time
	}
	return &a, nil
}

// SetArtistFollowed включает или отключает отслеживание артиста.
func (db *DB) SetArtistFollowed(ctx context.Context, artistName string, followed bool) error {
	cleanName := CleanArtistKey(artistName)
	followedVal := 0
	if followed {
		followedVal = 1
	}
	query := `UPDATE tracked_artists SET is_followed = ?, updated_at = CURRENT_TIMESTAMP WHERE clean_name = ?;`
	_, err := db.conn.ExecContext(ctx, query, followedVal, cleanName)
	return err
}

// SetArtistAutoDownload включает или отключает автоматическую загрузку новинок артиста.
func (db *DB) SetArtistAutoDownload(ctx context.Context, artistName string, auto bool) error {
	cleanName := CleanArtistKey(artistName)
	autoVal := 0
	if auto {
		autoVal = 1
	}
	query := `UPDATE tracked_artists SET auto_download = ?, updated_at = CURRENT_TIMESTAMP WHERE clean_name = ?;`
	_, err := db.conn.ExecContext(ctx, query, autoVal, cleanName)
	return err
}

// UpdateArtistCustomLinks сохраняет пользовательские связи с каталогами и псевдоним.
func (db *DB) UpdateArtistCustomLinks(ctx context.Context, artistName, alias, deezerID, mbID, yandexID string) error {
	cleanName := CleanArtistKey(artistName)
	query := `
	UPDATE tracked_artists SET
		custom_search_alias = ?,
		deezer_id = ?,
		musicbrainz_id = ?,
		yandex_id = ?,
		updated_at = CURRENT_TIMESTAMP
	WHERE clean_name = ?;
	`
	_, err := db.conn.ExecContext(ctx, query, alias, deezerID, mbID, yandexID, cleanName)
	return err
}

// UpdateArtistLastChecked обновляет время последней проверки дискографии артиста.
func (db *DB) UpdateArtistLastChecked(ctx context.Context, artistName string) error {
	cleanName := CleanArtistKey(artistName)
	query := `UPDATE tracked_artists SET last_checked_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE clean_name = ?;`
	_, err := db.conn.ExecContext(ctx, query, cleanName)
	return err
}

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
	WHERE rr.clean_artist_name = ? OR LOWER(rr.artist_name) LIKE ?
	GROUP BY COALESCE(NULLIF(rr.deezer_id, ''), rr.clean_title)
	ORDER BY rr.year DESC, rr.release_date DESC;
	`
	pattern := "%" + cleanName + "%"
	rows, err := db.conn.QueryContext(ctx, query, cleanName, pattern)
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

// ReconcileAllRadarReleases проверяет все релизы со статусом 'new' или 'downloaded' и при наличии в медиатеке переводит в in_library.
func (db *DB) ReconcileAllRadarReleases(ctx context.Context) (int, error) {
	rows, err := db.conn.QueryContext(ctx, `
		SELECT id, clean_title
		FROM radar_releases
		WHERE is_radar = 1 OR status IN ('new', 'downloaded');
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type releaseRef struct {
		id         int64
		cleanTitle string
	}
	var toCheck []releaseRef
	for rows.Next() {
		var ref releaseRef
		if err := rows.Scan(&ref.id, &ref.cleanTitle); err == nil {
			toCheck = append(toCheck, ref)
		}
	}
	rows.Close()

	if len(toCheck) == 0 {
		return 0, nil
	}

	updatedCount := 0
	for _, ref := range toCheck {
		inLibrary := false

		// 1. Проверяем наличие в альбомах
		var dummy int
		if err := db.conn.QueryRowContext(ctx, "SELECT 1 FROM albums WHERE clean_title = ? OR ? LIKE '%' || clean_title || '%' LIMIT 1", ref.cleanTitle, ref.cleanTitle).Scan(&dummy); err == nil {
			inLibrary = true
		}

		// 2. Проверяем наличие в треках
		if !inLibrary {
			if err := db.conn.QueryRowContext(ctx, "SELECT 1 FROM tracks WHERE clean_title = ? OR ? LIKE '%' || clean_title || '%' LIMIT 1", ref.cleanTitle, ref.cleanTitle).Scan(&dummy); err == nil {
				inLibrary = true
			}
		}

		if inLibrary {
			_, _ = db.conn.ExecContext(ctx, "UPDATE radar_releases SET status = 'in_library', is_radar = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?", ref.id)
			updatedCount++
		}
	}

	return updatedCount, nil
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

// SaveArtistRelations сохраняет список похожих артистов в таблице artist_relations.
func (db *DB) SaveArtistRelations(ctx context.Context, artistName string, relations []ArtistRelationItem) error {
	if len(relations) == 0 {
		return nil
	}
	cleanArtist := CleanArtistKey(artistName)
	query := `
	INSERT INTO artist_relations (artist_name, related_name, score, source, updated_at)
	VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(artist_name, related_name) DO UPDATE SET
		score = excluded.score,
		source = excluded.source,
		updated_at = CURRENT_TIMESTAMP;
	`
	for _, rel := range relations {
		cleanRelated := CleanArtistKey(rel.RelatedName)
		if cleanArtist == cleanRelated {
			continue
		}
		_, err := db.conn.ExecContext(ctx, query, cleanArtist, cleanRelated, rel.Score, rel.Source)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetRelatedArtists возвращает имена связанных похожих артистов из кэша.
func (db *DB) GetRelatedArtists(ctx context.Context, artistName string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 10
	}
	cleanArtist := CleanArtistKey(artistName)
	query := `
	SELECT related_name
	FROM artist_relations
	WHERE artist_name = ?
	ORDER BY score DESC
	LIMIT ?;
	`
	rows, err := db.conn.QueryContext(ctx, query, cleanArtist, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		result = append(result, name)
	}
	return result, rows.Err()
}

// RecordRadioPlay фиксирует факт воспроизведения трека радиостанцией.
func (db *DB) RecordRadioPlay(ctx context.Context, trackID int64, stationType, stationParam string) error {
	query := `INSERT INTO radio_history (track_id, station_type, station_param) VALUES (?, ?, ?);`
	_, err := db.conn.ExecContext(ctx, query, trackID, stationType, stationParam)
	return err
}

// GetRecentRadioTracks возвращает ID треков, игравших за последние N часов.
func (db *DB) GetRecentRadioTracks(ctx context.Context, stationType string, hours int) ([]int64, error) {
	if hours <= 0 {
		hours = 6
	}
	query := `
	SELECT DISTINCT track_id
	FROM radio_history
	WHERE station_type = ? AND played_at >= datetime('now', ?);
	`
	timeMod := fmt.Sprintf("-%d hours", hours)
	rows, err := db.conn.QueryContext(ctx, query, stationType, timeMod)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

// GetRecentRadioArtists возвращает имена артистов последних N сыгранных треков.
func (db *DB) GetRecentRadioArtists(ctx context.Context, stationType string, count int) ([]string, error) {
	if count <= 0 {
		count = 5
	}
	query := `
	SELECT DISTINCT LOWER(a.name)
	FROM radio_history rh
	JOIN tracks t ON rh.track_id = t.id
	JOIN artists a ON t.artist_id = a.id
	WHERE rh.station_type = ?
	ORDER BY rh.played_at DESC
	LIMIT ?;
	`
	rows, err := db.conn.QueryContext(ctx, query, stationType, count)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		result = append(result, name)
	}
	return result, rows.Err()
}

// GetWaveCandidateTracks возвращает случайную выборку треков медиатеки для селектора радио.
func (db *DB) GetWaveCandidateTracks(ctx context.Context, limit int) ([]TrackEntity, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `
	SELECT t.id, t.album_id, t.artist_id, t.title, ar.name AS artist, al.title AS album,
	       t.track_number, t.duration, t.bitrate, t.file_path, t.file_size,
	       COALESCE(t.file_hash, ''), t.has_embedded_cover, t.lyrics_status,
	       al.has_cover, COALESCE(al.cover_path, '')
	FROM tracks t
	JOIN albums al ON t.album_id = al.id
	JOIN artists ar ON t.artist_id = ar.id
	ORDER BY RANDOM()
	LIMIT ?;
	`
	rows, err := db.conn.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TrackEntity
	for rows.Next() {
		var t TrackEntity
		err := rows.Scan(
			&t.ID, &t.AlbumID, &t.ArtistID, &t.Title, &t.Artist, &t.Album,
			&t.TrackNumber, &t.Duration, &t.Bitrate, &t.FilePath, &t.FileSize,
			&t.FileHash, &t.HasEmbeddedCover, &t.LyricsStatus,
			&t.HasCover, &t.CoverPath,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

// GetTracksByGenre возвращает случайную выборку треков указанного жанра.
func (db *DB) GetTracksByGenre(ctx context.Context, genreName string, limit int) ([]TrackEntity, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
	SELECT t.id, t.album_id, t.artist_id, t.title, ar.name AS artist, al.title AS album,
	       t.track_number, t.duration, t.bitrate, t.file_path, t.file_size,
	       COALESCE(t.file_hash, ''), t.has_embedded_cover, t.lyrics_status,
	       al.has_cover, COALESCE(al.cover_path, '')
	FROM tracks t
	JOIN albums al ON t.album_id = al.id
	JOIN artists ar ON t.artist_id = ar.id
	JOIN track_genres tg ON tg.track_id = t.id
	JOIN genres g ON tg.genre_id = g.id
	WHERE LOWER(g.name) = LOWER(?)
	ORDER BY RANDOM()
	LIMIT ?;
	`
	rows, err := db.conn.QueryContext(ctx, query, genreName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TrackEntity
	for rows.Next() {
		var t TrackEntity
		err := rows.Scan(
			&t.ID, &t.AlbumID, &t.ArtistID, &t.Title, &t.Artist, &t.Album,
			&t.TrackNumber, &t.Duration, &t.Bitrate, &t.FilePath, &t.FileSize,
			&t.FileHash, &t.HasEmbeddedCover, &t.LyricsStatus,
			&t.HasCover, &t.CoverPath,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

// GetTracksByArtists возвращает выборку треков указанных исполнителей.
func (db *DB) GetTracksByArtists(ctx context.Context, artistNames []string, limit int) ([]TrackEntity, error) {
	if len(artistNames) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}

	var placeholders []string
	var args []any
	for _, a := range artistNames {
		placeholders = append(placeholders, "?")
		args = append(args, strings.ToLower(strings.TrimSpace(a)))
	}
	args = append(args, limit)

	query := fmt.Sprintf(`
	SELECT t.id, t.album_id, t.artist_id, t.title, ar.name AS artist, al.title AS album,
	       t.track_number, t.duration, t.bitrate, t.file_path, t.file_size,
	       COALESCE(t.file_hash, ''), t.has_embedded_cover, t.lyrics_status,
	       al.has_cover, COALESCE(al.cover_path, '')
	FROM tracks t
	JOIN albums al ON t.album_id = al.id
	JOIN artists ar ON t.artist_id = ar.id
	WHERE LOWER(ar.name) IN (%s)
	ORDER BY RANDOM()
	LIMIT ?;
	`, strings.Join(placeholders, ", "))

	rows, err := db.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TrackEntity
	for rows.Next() {
		var t TrackEntity
		err := rows.Scan(
			&t.ID, &t.AlbumID, &t.ArtistID, &t.Title, &t.Artist, &t.Album,
			&t.TrackNumber, &t.Duration, &t.Bitrate, &t.FilePath, &t.FileSize,
			&t.FileHash, &t.HasEmbeddedCover, &t.LyricsStatus,
			&t.HasCover, &t.CoverPath,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

// GetAvailableGenres возвращает топ жанров медиатеки с количеством треков.
func (db *DB) GetAvailableGenres(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 30
	}
	query := `
	SELECT g.name
	FROM genres g
	JOIN track_genres tg ON tg.genre_id = g.id
	GROUP BY g.id, g.name
	ORDER BY COUNT(tg.track_id) DESC
	LIMIT ?;
	`
	rows, err := db.conn.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil && name != "" {
			result = append(result, name)
		}
	}
	return result, rows.Err()
}

