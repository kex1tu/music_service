package db

import (
	"context"
	"database/sql"
	"time"

	"music-manager/internal/core/textkey"
)

// TrackedArtistEntity содержит настройки поиска и статистику артиста для Радара.
type TrackedArtistEntity struct {
	ID                int64      `json:"id"`
	ArtistID          int64      `json:"artist_id,omitempty"`
	Name              string     `json:"name"`
	CleanName         string     `json:"clean_name"`
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
	       COALESCE(ta.custom_search_alias, ''),
	       COALESCE(NULLIF(ta.deezer_id, ''), ar.deezer_id, ''), COALESCE(ta.musicbrainz_id, ''), COALESCE(ta.yandex_id, ''), ta.last_checked_at,
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
			UNION SELECT artist_id, album_id FROM album_artists
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
	err := row.Scan(&artist.ID, &artist.ArtistID, &artist.Name, &artist.CleanName,
		&artist.CustomSearchAlias, &artist.DeezerID, &artist.MusicBrainzID, &artist.YandexID, &lastChecked,
		&artist.AlbumCount, &artist.TrackCount, &artist.GapCount, &artist.CreatedAt, &artist.UpdatedAt)
	if lastChecked.Valid {
		artist.LastCheckedAt = &lastChecked.Time
	}
	return artist, err
}

func queryRadarArtists(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, filter string) ([]TrackedArtistEntity, error) {
	rows, err := queryer.QueryContext(ctx, trackedArtistsQuery+filter+`
		ORDER BY COALESCE(ar.name, ta.name) COLLATE NOCASE ASC, ta.id`)
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

func (db *DB) GetTrackedArtists(ctx context.Context) ([]TrackedArtistEntity, error) {
	return queryRadarArtists(ctx, db.conn, "")
}

// QueryRadarLibraryArtists lists artists credited on an indexed track or album.
func QueryRadarLibraryArtists(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([]TrackedArtistEntity, error) {
	return queryRadarArtists(ctx, queryer, `
		WHERE (albums_by_artist.total > 0 OR tracks_by_artist.total > 0)
		  AND ta.id = COALESCE(
			(SELECT MIN(own.id) FROM tracked_artists own WHERE own.artist_id = ar.id AND own.name = ar.name),
			(SELECT MIN(own.id) FROM tracked_artists own WHERE own.artist_id = ar.id)
		  )`)
}

func (db *DB) GetRadarLibraryArtists(ctx context.Context) ([]TrackedArtistEntity, error) {
	return QueryRadarLibraryArtists(ctx, db.conn)
}

// QueryTrackedArtist resolves the local identity before reading its settings.
// The original spelling disambiguates legacy settings with the same identity.
func QueryTrackedArtist(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, name string) (*TrackedArtistEntity, error) {
	artist, err := readTrackedArtist(queryer.QueryRowContext(ctx, trackedArtistsQuery+`
		CROSS JOIN (SELECT `+ArtistIDLookup("?1")+` AS id) requested
		WHERE (requested.id IS NOT NULL OR NOT EXISTS(SELECT 1 FROM artists WHERE name = trim(?1) OR clean_name = music_search_key(?1)))
		AND (ta.artist_id = requested.id OR (ta.artist_id IS NULL AND (ta.name = trim(?1) OR ta.clean_name = music_search_key(?1))))
		ORDER BY ta.name = trim(?1) DESC, ta.id LIMIT 1`, name))
	if err != nil {
		return nil, err
	}
	return &artist, nil
}

func (db *DB) GetTrackedArtistByName(ctx context.Context, name string) (*TrackedArtistEntity, error) {
	return QueryTrackedArtist(ctx, db.conn, name)
}

// GetTrackedArtistByID reads the preferred settings for a local artist.
func (db *DB) GetTrackedArtistByID(ctx context.Context, artistID int64) (TrackedArtistEntity, error) {
	return readTrackedArtist(db.conn.QueryRowContext(ctx, trackedArtistsQuery+`
		WHERE ta.artist_id = ? ORDER BY (ta.name = ar.name) DESC, ta.id LIMIT 1`, artistID))
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
