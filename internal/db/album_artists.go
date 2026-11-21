package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"music-manager/internal/id3"
)

// SearchAlbumIDsByArtist accepts a normalized name fragment as SQL parameter ?1.
const SearchAlbumIDsByArtist = `SELECT aa.album_id FROM artists credited
	JOIN album_artists aa ON aa.artist_id = credited.id WHERE instr(credited.clean_name, ?1) > 0`

// FirstCreditedAlbumPath finds a folder for the outer artists row aliased as ar.
const FirstCreditedAlbumPath = `SELECT MIN(al.folder_path) FROM album_artists aa
	JOIN albums al ON al.id = aa.album_id WHERE aa.artist_id = ar.id`

func migrateAlbumArtists(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `
		CREATE TABLE album_artists (
			album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
			artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE RESTRICT,
			position INTEGER NOT NULL CHECK (position >= 1),
			PRIMARY KEY (album_id, artist_id),
			UNIQUE (album_id, position)
		);
		CREATE INDEX idx_album_artists_artist ON album_artists(artist_id, album_id);
	`); err != nil {
		return err
	}

	rows, err := conn.QueryContext(ctx, `SELECT id, COALESCE(album_artist, '') FROM albums ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var albumID int64
		var credit string
		if err := rows.Scan(&albumID, &credit); err != nil {
			return err
		}
		if err := syncAlbumArtists(ctx, conn, albumID, credit); err != nil {
			return err
		}
	}
	return rows.Err()
}

// syncAlbumArtists builds the ordered index from the same credit written to the album.
// Names inside a single tag value may contain punctuation such as AC/DC or Earth, Wind & Fire.
func syncAlbumArtists(ctx context.Context, store artistStore, albumID int64, credit string) error {
	names := id3.ParseMultiValue(credit)
	var artistIDs []int64
	seen := make(map[int64]bool)
	for _, name := range names {
		if id3.IsCompilationArtist(name) || strings.EqualFold(name, "Unknown Artist") {
			continue
		}
		var id sql.NullInt64
		if err := store.QueryRowContext(ctx, `SELECT `+ArtistIDLookup("?1"), name).Scan(&id); err != nil {
			return err
		}
		if !id.Valid {
			// A lone unresolved credit may be a free-form string such as "A feat. B".
			// Only explicit lists justify creating additional artist identities.
			if len(names) == 1 {
				continue
			}
			newID, err := GetOrCreateLibraryArtist(ctx, store, name)
			if err != nil {
				return err
			}
			id.Int64 = newID
		}
		if !seen[id.Int64] {
			artistIDs = append(artistIDs, id.Int64)
			seen[id.Int64] = true
		}
	}
	if len(artistIDs) == 0 {
		_, err := store.ExecContext(ctx, `DELETE FROM album_artists WHERE album_id = ?`, albumID)
		return err
	}
	// Reuse unchanged links. Removing moved rows first avoids position conflicts when reordering.
	orderedIDs, err := json.Marshal(artistIDs)
	if err != nil {
		return err
	}
	if _, err := store.ExecContext(ctx, `DELETE FROM album_artists WHERE album_id = ?1
		AND NOT EXISTS (SELECT 1 FROM json_each(?2) desired
			WHERE desired.value = album_artists.artist_id AND desired.key + 1 = album_artists.position)`,
		albumID, string(orderedIDs)); err != nil {
		return err
	}
	_, err = store.ExecContext(ctx, `INSERT INTO album_artists (album_id, artist_id, position)
		SELECT ?1, value, key + 1 FROM json_each(?2) WHERE true
		ON CONFLICT (album_id, artist_id) DO NOTHING`, albumID, string(orderedIDs))
	return err
}
