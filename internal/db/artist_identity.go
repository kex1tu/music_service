package db

import (
	"context"
	"database/sql"
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
	return `(SELECT id FROM artists WHERE name != '' AND name = trim(` + nameSQL + `))`
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
		SELECT ?1, music_search_key(?1) WHERE `+ArtistIDLookup("?1")+` IS NULL
		ON CONFLICT(name) DO NOTHING`, name); err != nil {
		return 0, err
	}
	var id int64
	if err := store.QueryRowContext(ctx, "SELECT "+ArtistIDLookup("?1"), name).Scan(&id); err != nil {
		return 0, err
	}
	return id, RegisterTrackedArtist(ctx, store, name)
}

// Durable artist references have RESTRICT foreign keys. Library cleanup removes
// only artists without local media or a surviving reference in these tables.
const unusedArtist = `
	NOT EXISTS (SELECT 1 FROM albums WHERE artist_id = artists.id)
	AND NOT EXISTS (SELECT 1 FROM tracks WHERE artist_id = artists.id)
	AND NOT EXISTS (SELECT 1 FROM tracked_artists WHERE artist_id = artists.id)
	AND NOT EXISTS (SELECT 1 FROM radar_release_artists WHERE artist_id = artists.id)
	AND NOT EXISTS (SELECT 1 FROM pinned_track_sources WHERE artist_id = artists.id)
	AND NOT EXISTS (SELECT 1 FROM soundcloud_uncensored_cache WHERE artist_id = artists.id AND is_manual = 1)
	AND NOT EXISTS (SELECT 1 FROM artist_relations WHERE artist_id = artists.id)
	AND NOT EXISTS (SELECT 1 FROM artist_relations WHERE related_artist_id = artists.id)`
