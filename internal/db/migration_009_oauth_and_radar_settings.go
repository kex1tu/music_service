package db

import (
	"context"
	"database/sql"
)

func migrateOAuthAndRadarSettings(ctx context.Context, conn *sql.Conn) error {
	// Preserve the reader's precedence and move the four fields as one session.
	_, err := conn.ExecContext(ctx, `
		DELETE FROM app_settings
		WHERE key IN ('mb_access_token','mb_refresh_token','mb_expires_at','mb_username')
		  AND NOT EXISTS (SELECT 1 FROM app_settings WHERE key = 'mb_access_token' AND value <> '')
		  AND EXISTS (SELECT 1 FROM app_settings WHERE key = 'musicbrainz_access_token' AND value <> '');

		INSERT INTO app_settings (key, value, updated_at)
		SELECT 'mb_' || substr(key, length('musicbrainz_') + 1), value, updated_at
		FROM app_settings
		WHERE key IN ('musicbrainz_access_token','musicbrainz_refresh_token','musicbrainz_expires_at','musicbrainz_username')
		  AND NOT EXISTS (SELECT 1 FROM app_settings WHERE key = 'mb_access_token' AND value <> '')
		  AND EXISTS (SELECT 1 FROM app_settings WHERE key = 'musicbrainz_access_token' AND value <> '');

		DELETE FROM app_settings
		WHERE key IN ('musicbrainz_access_token','musicbrainz_refresh_token','musicbrainz_expires_at','musicbrainz_username');

		INSERT INTO tracked_artists (name, clean_name, artist_id)
		SELECT ar.name, ar.clean_name, ar.id FROM artists ar
		WHERE ar.id IN (
			SELECT artist_id FROM tracks UNION SELECT artist_id FROM albums UNION SELECT artist_id FROM album_artists
		)
		AND NOT EXISTS (SELECT 1 FROM tracked_artists ta WHERE ta.artist_id = ar.id);
	`)
	return err
}
