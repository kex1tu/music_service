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
		INSERT INTO tracked_artists (name, clean_name, is_followed)
		SELECT ?1, ?2, 1
		WHERE NOT EXISTS (
			SELECT 1 FROM tracked_artists
			WHERE artist_id = `+ArtistIDLookup("?1")+`
			   OR (artist_id IS NULL AND clean_name = ?2)
		)
		ON CONFLICT(name) DO NOTHING`, name, key)
	return err
}

func (db *DB) trackedArtistID(ctx context.Context, name string) (int64, error) {
	var id int64
	// Exact spelling identifies a legacy caller's selection. Otherwise only
	// allow an unambiguous lookup across case/normalization variants.
	err := db.conn.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(CASE WHEN name = ?2 THEN id END),
			CASE WHEN COUNT(*) = 1 THEN MIN(id) END, 0)
		FROM tracked_artists
		WHERE artist_id = `+ArtistIDLookup("?2")+`
		   OR name = ?2
		   OR (artist_id IS NULL AND clean_name = ?1)`, CleanArtistKey(name), strings.TrimSpace(name)).Scan(&id)
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

func SetTrackedArtistFollowed(ctx context.Context, writer artistWriter, id int64, followed bool) error {
	return updateTrackedArtist(ctx, writer, id, "is_followed = ?, updated_at = CURRENT_TIMESTAMP", followed)
}

func SetTrackedArtistAutoDownload(ctx context.Context, writer artistWriter, id int64, enabled bool) error {
	return updateTrackedArtist(ctx, writer, id, "auto_download = ?, updated_at = CURRENT_TIMESTAMP", enabled)
}

func SetTrackedArtistLinks(ctx context.Context, writer artistWriter, id int64, alias, deezerID, mbID, yandexID string) error {
	return updateTrackedArtist(ctx, writer, id, `custom_search_alias = ?, deezer_id = ?, musicbrainz_id = ?,
		yandex_id = ?, updated_at = CURRENT_TIMESTAMP`, alias, deezerID, mbID, yandexID)
}
