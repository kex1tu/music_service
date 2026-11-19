package db

import (
	"context"
	"database/sql"
	"time"

	"music-manager/internal/core/textkey"
)

// TrackedArtistEntity представляет персистентную запись отслеживаемого артиста.
type TrackedArtistEntity struct {
	ID                int64      `json:"id"`
	ArtistID          int64      `json:"artist_id,omitempty"`
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
	return textkey.Search(s)
}

// UpsertTrackedArtist добавляет артиста на отслеживание или обновляет метку времени.
func (db *DB) UpsertTrackedArtist(ctx context.Context, name string) error {
	return RegisterTrackedArtist(ctx, db.conn, name)
}

const trackedArtistsQuery = `
	SELECT ta.id, COALESCE(ta.artist_id, 0), COALESCE(ar.name, ta.name), COALESCE(ar.clean_name, ta.clean_name),
	       ta.is_followed, ta.auto_download, COALESCE(ta.custom_search_alias, ''),
	       COALESCE(ta.deezer_id, ''), COALESCE(ta.musicbrainz_id, ''), COALESCE(ta.yandex_id, ''), ta.last_checked_at,
	       COALESCE(albums_by_artist.total, 0), COALESCE(tracks_by_artist.total, 0),
	       CASE WHEN ta.artist_id IS NULL THEN (
	           SELECT COUNT(*) FROM radar_releases rr WHERE rr.status = 'missing' AND music_radar_artist_matches(rr.artist_name, ta.name)
	       ) ELSE COALESCE(gaps_by_artist.total, 0) END,
	       ta.created_at, ta.updated_at
	FROM tracked_artists ta LEFT JOIN artists ar ON ar.id = ta.artist_id
	LEFT JOIN (
		SELECT artist_id, COUNT(*) AS total FROM (
			SELECT artist_id, id AS album_id FROM albums
			UNION SELECT artist_id, album_id FROM tracks
		) GROUP BY artist_id
	) albums_by_artist ON albums_by_artist.artist_id = ta.artist_id
	LEFT JOIN (
		SELECT artist_id, COUNT(*) AS total FROM tracks GROUP BY artist_id
	) tracks_by_artist ON tracks_by_artist.artist_id = ta.artist_id
	LEFT JOIN (
		SELECT credit.artist_id, COUNT(*) AS total FROM radar_release_artists credit
		JOIN radar_releases rr ON rr.id = credit.release_id WHERE rr.status = 'missing'
		GROUP BY credit.artist_id
	) gaps_by_artist ON gaps_by_artist.artist_id = ta.artist_id`

func readTrackedArtist(row interface{ Scan(...any) error }) (TrackedArtistEntity, error) {
	var artist TrackedArtistEntity
	var lastChecked sql.NullTime
	err := row.Scan(&artist.ID, &artist.ArtistID, &artist.Name, &artist.CleanName, &artist.IsFollowed, &artist.AutoDownload,
		&artist.CustomSearchAlias, &artist.DeezerID, &artist.MusicBrainzID, &artist.YandexID, &lastChecked,
		&artist.AlbumCount, &artist.TrackCount, &artist.GapCount, &artist.CreatedAt, &artist.UpdatedAt)
	if lastChecked.Valid {
		artist.LastCheckedAt = &lastChecked.Time
	}
	return artist, err
}

// QueryTrackedArtists returns settings together with counts grouped by the common artist ID.
func QueryTrackedArtists(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, onlyFollowed bool) ([]TrackedArtistEntity, error) {
	rows, err := queryer.QueryContext(ctx, trackedArtistsQuery+`
		WHERE (? = 0 OR ta.is_followed = 1) ORDER BY COALESCE(ar.name, ta.name) COLLATE NOCASE ASC`, onlyFollowed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	artists := []TrackedArtistEntity{}
	for rows.Next() {
		artist, err := readTrackedArtist(rows)
		if err != nil {
			return nil, err
		}
		artists = append(artists, artist)
	}
	return artists, rows.Err()
}

func (db *DB) GetTrackedArtists(ctx context.Context, onlyFollowed bool) ([]TrackedArtistEntity, error) {
	return QueryTrackedArtists(ctx, db.conn, onlyFollowed)
}

// QueryTrackedArtist resolves the local identity before reading its settings.
// The original spelling disambiguates legacy settings with the same identity.
func QueryTrackedArtist(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, name string) (*TrackedArtistEntity, error) {
	artist, err := readTrackedArtist(queryer.QueryRowContext(ctx, trackedArtistsQuery+`
		WHERE ta.artist_id = `+ArtistIDLookup("?1")+`
		   OR ta.name = trim(?1)
		   OR (ta.artist_id IS NULL AND ta.clean_name = music_search_key(?1))
		ORDER BY ta.name = trim(?1) DESC, ta.id LIMIT 1`, name))
	if err != nil {
		return nil, err
	}
	return &artist, nil
}

func (db *DB) GetTrackedArtistByName(ctx context.Context, name string) (*TrackedArtistEntity, error) {
	return QueryTrackedArtist(ctx, db.conn, name)
}

// SetArtistFollowed включает или отключает отслеживание артиста.
func (db *DB) SetArtistFollowed(ctx context.Context, artistName string, followed bool) error {
	id, err := db.trackedArtistID(ctx, artistName)
	if err != nil {
		return err
	}
	return SetTrackedArtistFollowed(ctx, db.conn, id, followed)
}

// SetArtistAutoDownload включает или отключает автоматическую загрузку новинок артиста.
func (db *DB) SetArtistAutoDownload(ctx context.Context, artistName string, auto bool) error {
	id, err := db.trackedArtistID(ctx, artistName)
	if err != nil {
		return err
	}
	return SetTrackedArtistAutoDownload(ctx, db.conn, id, auto)
}

// UpdateArtistCustomLinks сохраняет пользовательские связи с каталогами и псевдоним.
func (db *DB) UpdateArtistCustomLinks(ctx context.Context, artistName, alias, deezerID, mbID, yandexID string) error {
	id, err := db.trackedArtistID(ctx, artistName)
	if err != nil {
		return err
	}
	return SetTrackedArtistLinks(ctx, db.conn, id, alias, deezerID, mbID, yandexID)
}

// UpdateArtistLastChecked обновляет время последней проверки дискографии артиста.
func (db *DB) UpdateArtistLastChecked(ctx context.Context, artistName string) error {
	id, err := db.trackedArtistID(ctx, artistName)
	if err != nil {
		return err
	}
	return db.UpdateArtistLastCheckedByID(ctx, id)
}

func (db *DB) UpdateArtistLastCheckedByID(ctx context.Context, id int64) error {
	return updateTrackedArtist(ctx, db.conn, id, "last_checked_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP")
}

// SaveArtistRelations сохраняет список похожих артистов в таблице artist_relations.
func (db *DB) SaveArtistRelations(ctx context.Context, artistName string, relations []ArtistRelationItem) error {
	if len(relations) == 0 {
		return nil
	}
	cleanArtist := CleanArtistKey(artistName)
	query := `
	WITH ids(artist_id, related_artist_id) AS (SELECT ` + ArtistIDLookup("?1") + `, ` + ArtistIDLookup("?2") + `)
	INSERT INTO artist_relations (artist_name, related_name, score, source, artist_id, related_artist_id, updated_at)
	SELECT COALESCE((SELECT name FROM artists WHERE id = ids.artist_id), music_search_key(?1)),
	       COALESCE((SELECT name FROM artists WHERE id = ids.related_artist_id), music_search_key(?2)),
	       ?3, ?4, ids.artist_id, ids.related_artist_id, CURRENT_TIMESTAMP
	FROM ids WHERE ids.artist_id IS NULL OR ids.artist_id IS NOT ids.related_artist_id
	ON CONFLICT(artist_name, related_name) DO UPDATE SET
		score = excluded.score,
		source = excluded.source,
		artist_id = COALESCE(excluded.artist_id, artist_relations.artist_id),
		related_artist_id = COALESCE(excluded.related_artist_id, artist_relations.related_artist_id),
		updated_at = CURRENT_TIMESTAMP;
	`
	for _, rel := range relations {
		cleanRelated := CleanArtistKey(rel.RelatedName)
		if cleanArtist == cleanRelated {
			continue
		}
		_, err := db.conn.ExecContext(ctx, query, artistName, rel.RelatedName, rel.Score, rel.Source)
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
	query := `
	WITH requested(id) AS (SELECT ` + ArtistIDLookup("?1") + `)
	SELECT COALESCE(related.name, rel.related_name)
	FROM artist_relations rel
	LEFT JOIN artists related ON related.id = rel.related_artist_id
	CROSS JOIN requested
	WHERE rel.artist_id = requested.id OR (rel.artist_id IS NULL
		AND rel.artist_name = music_search_key(?1)
		AND NOT EXISTS (SELECT 1 FROM artists WHERE clean_name = music_search_key(?1)))
	GROUP BY rel.related_artist_id, COALESCE(related.name, rel.related_name)
	ORDER BY MAX(rel.score) DESC, COALESCE(related.name, rel.related_name)
	LIMIT ?2;
	`
	rows, err := db.conn.QueryContext(ctx, query, artistName, limit)
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
