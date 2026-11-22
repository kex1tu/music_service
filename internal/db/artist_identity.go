package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type artistStore interface {
	artistWriter
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ArtistIDLookup returns the SQL expression for the matching local artist.
// Exact spelling wins. A normalized spelling is used only if it identifies one row.
func ArtistIDLookup(nameSQL string) string {
	return `COALESCE(` + exactArtistIDLookup(nameSQL) + `, ` + normalizedArtistIDLookup(nameSQL) + `)`
}

func exactArtistIDLookup(nameSQL string) string {
	return `(SELECT CASE WHEN COUNT(*) = 1 THEN MIN(id) END FROM artists WHERE name != '' AND name = trim(` + nameSQL + `))`
}

func normalizedArtistIDLookup(nameSQL string) string {
	return `(SELECT CASE WHEN COUNT(*) = 1 THEN MIN(id) END
		FROM artists WHERE clean_name != '' AND clean_name = music_search_key(` + nameSQL + `))`
}

// GetOrCreateLibraryArtist reuses the ID of an artist retained by durable references.
func GetOrCreateLibraryArtist(ctx context.Context, store artistStore, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Unknown Artist"
	}
	if _, err := store.ExecContext(ctx, `
		INSERT INTO artists(name, clean_name)
		SELECT ?1, music_search_key(?1) WHERE NOT EXISTS (
			SELECT 1 FROM artists WHERE clean_name = music_search_key(?1) OR name = ?1
		)`, name); err != nil {
		return 0, err
	}
	var id sql.NullInt64
	if err := store.QueryRowContext(ctx, "SELECT "+ArtistIDLookup("?1"), name).Scan(&id); err != nil {
		return 0, err
	}
	if !id.Valid {
		return 0, fmt.Errorf("%w: %q", ErrAmbiguousArtist, name)
	}
	return id.Int64, RegisterTrackedArtistID(ctx, store, id.Int64)
}

var ErrAmbiguousArtist = errors.New("ambiguous artist identity")

// ResolveCatalogArtist runs under the catalog write transaction. Deezer IDs
// can distinguish different performers who have the same name.
func ResolveCatalogArtist(ctx context.Context, tx *sql.Tx, name, deezerID string, albumArtistID int64) (int64, error) {
	name, deezerID = strings.TrimSpace(name), strings.TrimSpace(deezerID)
	if deezerID != "" {
		var id int64
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MIN(id), 0), COUNT(*) FROM artists WHERE deezer_id = ?", deezerID).Scan(&id, &count); err != nil {
			return 0, err
		}
		if count > 1 {
			return 0, fmt.Errorf("%w: Deezer artist %s", ErrAmbiguousArtist, deezerID)
		}
		if count == 1 {
			return id, nil
		}
	}
	if name == "" {
		return albumArtistID, nil
	}
	var id sql.NullInt64
	var existingProvider string
	if err := tx.QueryRowContext(ctx, `SELECT local.id, COALESCE(a.deezer_id, '')
		FROM (SELECT `+ArtistIDLookup("?1")+` AS id) local LEFT JOIN artists a ON a.id = local.id`, name).Scan(&id, &existingProvider); err != nil {
		return 0, err
	}
	if id.Valid && (deezerID == "" || existingProvider == "" || existingProvider == deezerID) {
		if deezerID != "" && existingProvider == "" {
			if _, err := tx.ExecContext(ctx, "UPDATE artists SET deezer_id = ? WHERE id = ?", deezerID, id.Int64); err != nil {
				return 0, err
			}
		}
		return id.Int64, nil
	}
	if deezerID == "" {
		var matches bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM artists WHERE name = ?1 OR clean_name = music_search_key(?1))", name).Scan(&matches); err != nil {
			return 0, err
		}
		if matches {
			return 0, fmt.Errorf("%w: %q", ErrAmbiguousArtist, name)
		}
	}
	var created int64
	err := tx.QueryRowContext(ctx, `INSERT INTO artists(name, clean_name, deezer_id)
		VALUES (?1, music_search_key(?1), NULLIF(?2, '')) RETURNING id`, name, deezerID).Scan(&created)
	return created, err
}

// Durable artist references have RESTRICT foreign keys. Library cleanup removes
// only artists without local media or a surviving reference in these tables.
const unusedArtist = `
	NOT EXISTS (SELECT 1 FROM albums WHERE artist_id = artists.id)
	AND NOT EXISTS (SELECT 1 FROM album_artists WHERE artist_id = artists.id)
	AND NOT EXISTS (SELECT 1 FROM tracks WHERE artist_id = artists.id)
	AND NOT EXISTS (SELECT 1 FROM tracked_artists WHERE artist_id = artists.id)
	AND NOT EXISTS (SELECT 1 FROM radar_release_artists WHERE artist_id = artists.id)
	AND NOT EXISTS (SELECT 1 FROM release_tracklist WHERE artist_id = artists.id)`
