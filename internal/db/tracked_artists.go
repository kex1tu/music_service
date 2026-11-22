package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type artistWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// RegisterTrackedArtist registers a spelling only when the normalized artist
// does not already have a subscription, preserving existing display names.
func RegisterTrackedArtist(ctx context.Context, writer artistWriter, name string) error {
	key := CleanArtistKey(name)
	if key == "" {
		return fmt.Errorf("artist name cannot be empty")
	}
	_, err := writer.ExecContext(ctx, `
		INSERT INTO tracked_artists (name, clean_name)
		SELECT ?1, ?2
		WHERE NOT EXISTS (
			SELECT 1 FROM tracked_artists
			WHERE artist_id = `+ArtistIDLookup("?1")+` OR name = ?1
			   OR (artist_id IS NULL AND clean_name = ?2)
		)`, name, key)
	return err
}

func RegisterTrackedArtistID(ctx context.Context, writer artistWriter, id int64) error {
	_, err := writer.ExecContext(ctx, `INSERT INTO tracked_artists (name, clean_name, artist_id)
		SELECT name, clean_name, id FROM artists
		WHERE id = ?1 AND NOT EXISTS (SELECT 1 FROM tracked_artists WHERE artist_id = ?1)
		ON CONFLICT DO NOTHING`, id)
	return err
}

func (db *DB) trackedArtistID(ctx context.Context, name string) (int64, error) {
	var id int64
	// Exact spelling identifies a legacy caller's selection. Otherwise only
	// allow an unambiguous lookup across case/normalization variants.
	err := db.conn.QueryRowContext(ctx, `
		WITH requested(id) AS (SELECT `+ArtistIDLookup("?2")+`)
		SELECT COALESCE(CASE WHEN COUNT(CASE WHEN name = ?2 THEN 1 END) = 1
			THEN MAX(CASE WHEN name = ?2 THEN ta.id END) END,
			CASE WHEN COUNT(*) = 1 THEN MIN(ta.id) END, 0)
		FROM tracked_artists ta CROSS JOIN requested
		WHERE (requested.id IS NOT NULL OR NOT EXISTS(SELECT 1 FROM artists WHERE clean_name = ?1 OR name = ?2))
		AND (ta.artist_id = requested.id OR (ta.artist_id IS NULL AND (ta.name = ?2 OR ta.clean_name = ?1)))`,
		CleanArtistKey(name), strings.TrimSpace(name)).Scan(&id)
	if err == nil && id == 0 {
		return 0, fmt.Errorf("unknown or ambiguous artist %q; select the subscription by ID", name)
	}
	return id, err
}

func updateTrackedArtist(ctx context.Context, writer artistWriter, id int64, assignments string, args ...any) error {
	query := "UPDATE tracked_artists SET " + assignments + " WHERE id = ?"
	result, err := writer.ExecContext(ctx, query, append(args, id)...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return sql.ErrNoRows
	}
	return err
}

func SetTrackedArtistLinks(ctx context.Context, writer artistWriter, id int64, alias, deezerID, mbID, yandexID string) error {
	return updateTrackedArtist(ctx, writer, id, `custom_search_alias = ?, deezer_id = ?, musicbrainz_id = ?,
		yandex_id = ?, updated_at = CURRENT_TIMESTAMP`, alias, deezerID, mbID, yandexID)
}

// SetLocalArtistLinks updates the same settings row selected by the library's
// artist listing. Lower IDs break ties among legacy rows for one artist.
func SetLocalArtistLinks(ctx context.Context, writer artistWriter, artistID int64, name, alias, deezerID, mbID, yandexID string) error {
	result, err := writer.ExecContext(ctx, `UPDATE tracked_artists
		SET custom_search_alias = ?, deezer_id = ?, musicbrainz_id = ?, yandex_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = COALESCE((
			SELECT ta.id FROM tracked_artists ta JOIN artists ar ON ar.id = ta.artist_id
			WHERE ar.id = ?5 ORDER BY (ta.name = ar.name) DESC, ta.id LIMIT 1
		), (
			SELECT ta.id FROM tracked_artists ta LEFT JOIN artists ar ON ar.id = ta.artist_id
			WHERE ta.id = ?5 AND COALESCE(ar.name, ta.name) = ?6
			  AND NOT EXISTS (SELECT 1 FROM artists WHERE id = ?5)
		))`, alias, deezerID, mbID, yandexID, artistID, name)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return sql.ErrNoRows
	}
	return err
}
