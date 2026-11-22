package db

import (
	"context"
	"database/sql"
	"strings"
)

func migrateAuditConsistency(ctx context.Context, conn *sql.Conn) error {
	var relink string
	if err := conn.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE name = 'link_new_artist'").Scan(&relink); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, `
		DROP INDEX IF EXISTS idx_tracks_artist;
		DROP INDEX IF EXISTS idx_radar_releases_artist;
		DROP INDEX IF EXISTS idx_tracks_mtime_size;
		DROP INDEX IF EXISTS idx_audit_issues_unresolved;
		DROP INDEX idx_tracks_clean;
		CREATE INDEX idx_tracks_clean ON tracks(clean_title);
		CREATE INDEX idx_track_genres_genre ON track_genres(genre_id);
		CREATE INDEX idx_library_artist_conflicts_resolved ON library_artist_conflicts(resolved_artist_id) WHERE resolved_artist_id IS NOT NULL;
		CREATE INDEX idx_download_jobs_status_created ON download_jobs(status, created_at);
		DROP INDEX idx_download_jobs_status;

		CREATE INDEX idx_tracks_isrc ON tracks(music_search_key(COALESCE(isrc, '')));
		CREATE TRIGGER tracks_lyrics_status_insert BEFORE INSERT ON tracks
			WHEN NEW.lyrics_status NOT IN ('none','lrc','txt') BEGIN
				SELECT RAISE(ABORT, 'invalid lyrics status');
			END;
		CREATE TRIGGER tracks_lyrics_status_update BEFORE UPDATE OF lyrics_status ON tracks
			WHEN NEW.lyrics_status NOT IN ('none','lrc','txt') BEGIN
				SELECT RAISE(ABORT, 'invalid lyrics status');
			END;

		ALTER TABLE albums ADD COLUMN catalog_revision INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE radar_release_sources ADD COLUMN credits_json TEXT CHECK (credits_json IS NULL OR json_valid(credits_json));

		DROP TRIGGER link_new_artist;
		DROP TRIGGER link_radar_artists_insert;
		DROP TRIGGER link_radar_artists_update;
		CREATE TABLE radar_release_artists_v13 (
			release_id INTEGER NOT NULL REFERENCES radar_releases(id) ON DELETE CASCADE,
			credit_name TEXT NOT NULL,
			artist_id INTEGER REFERENCES artists(id) ON DELETE RESTRICT
		);
		INSERT INTO radar_release_artists_v13 SELECT release_id, credit_name, artist_id FROM radar_release_artists;
		DROP TABLE radar_release_artists;
		ALTER TABLE radar_release_artists_v13 RENAME TO radar_release_artists;
		CREATE UNIQUE INDEX idx_radar_release_artists_artist ON radar_release_artists(artist_id, release_id) WHERE artist_id IS NOT NULL;
		CREATE UNIQUE INDEX idx_radar_release_artists_unlinked_name ON radar_release_artists(release_id, credit_name) WHERE artist_id IS NULL;
		CREATE INDEX idx_radar_release_artists_release ON radar_release_artists(release_id);
		CREATE INDEX idx_radar_release_artists_unlinked ON radar_release_artists(music_search_key(credit_name)) WHERE artist_id IS NULL;
		CREATE TRIGGER link_radar_artists_insert AFTER INSERT ON radar_releases
			WHEN NEW.has_catalog_credits = 0 BEGIN `+linkLegacyRadarCredit+` END;
		CREATE TRIGGER link_radar_artists_update AFTER UPDATE OF artist_name ON radar_releases
			WHEN NEW.has_catalog_credits = 0 AND NEW.artist_name IS NOT OLD.artist_name
			BEGIN DELETE FROM radar_release_artists WHERE release_id = NEW.id;
				`+linkLegacyRadarCredit+` END;
	`)
	if err != nil {
		return err
	}
	// A name-only automatic relink could undo an explicit provider decision.
	relink = strings.Replace(relink, "WHERE artist_id IS NULL AND music_search_key(credit_name)", `WHERE artist_id IS NULL
		AND NOT EXISTS (SELECT 1 FROM radar_release_sources s
			WHERE s.release_id = radar_release_artists.release_id AND s.credits_json IS NOT NULL)
		AND music_search_key(credit_name)`, 1)
	_, err = conn.ExecContext(ctx, relink)
	return err
}
