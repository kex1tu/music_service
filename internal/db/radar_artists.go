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
	TrackCount        int        `json:"track_count"`
	GapCount          int        `json:"gap_count"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

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
	var a TrackedArtistEntity
	var isFollowedInt, autoDownloadInt int
	var lastChecked sql.NullTime

	err := db.conn.QueryRowContext(ctx, query, cleanName).Scan(
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
