package db

import (
	"context"
	"database/sql"
	"strings"
)

func migrateTrackSources(ctx context.Context, conn *sql.Conn) error {
	// These triggers were installed at v5. Recreate the surviving ones with
	// lookups that reject duplicate spellings after the identity change.
	rows, err := conn.QueryContext(ctx, `SELECT name, tbl_name, sql FROM sqlite_schema
		WHERE type = 'trigger' AND name LIKE 'link_%' ORDER BY name`)
	if err != nil {
		return err
	}
	type trigger struct{ name, table, sql string }
	var triggers []trigger
	for rows.Next() {
		var t trigger
		if err := rows.Scan(&t.name, &t.table, &t.sql); err != nil {
			rows.Close()
			return err
		}
		triggers = append(triggers, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, t := range triggers {
		if _, err := conn.ExecContext(ctx, "DROP TRIGGER "+t.name); err != nil {
			return err
		}
	}

	if _, err := conn.ExecContext(ctx, `
		CREATE TEMP TABLE artist_sequences AS
			SELECT name, seq FROM sqlite_sequence WHERE name IN ('artists', 'tracked_artists');
		CREATE TABLE artists_v7 (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			clean_name TEXT NOT NULL,
			deezer_id TEXT, spotify_id TEXT, youtube_id TEXT, yandex_id TEXT, genius_id TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO artists_v7 SELECT * FROM artists;
		DROP TABLE artists;
		ALTER TABLE artists_v7 RENAME TO artists;
		CREATE INDEX idx_artists_clean ON artists(clean_name);
		CREATE INDEX idx_artists_name ON artists(name);
		CREATE INDEX idx_artists_deezer_id ON artists(deezer_id) WHERE deezer_id IS NOT NULL;

		CREATE TABLE tracked_artists_v7 (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL, clean_name TEXT NOT NULL, is_followed BOOLEAN DEFAULT 1,
			auto_download BOOLEAN DEFAULT 0, custom_search_alias TEXT, deezer_id TEXT,
			musicbrainz_id TEXT, yandex_id TEXT, last_checked_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			artist_id INTEGER REFERENCES artists(id) ON DELETE RESTRICT
		);
		INSERT INTO tracked_artists_v7 SELECT * FROM tracked_artists;
		DROP TABLE tracked_artists;
		ALTER TABLE tracked_artists_v7 RENAME TO tracked_artists;
		CREATE INDEX idx_tracked_artists_clean ON tracked_artists(clean_name);
		CREATE INDEX idx_tracked_artists_followed ON tracked_artists(is_followed);
		CREATE INDEX idx_tracked_artists_artist_id ON tracked_artists(artist_id) WHERE artist_id IS NOT NULL;
		CREATE INDEX idx_tracked_artists_artist_id_unlinked ON tracked_artists(music_search_key(name)) WHERE artist_id IS NULL;
		INSERT INTO sqlite_sequence(name, seq)
			SELECT old.name, old.seq FROM artist_sequences old
			WHERE NOT EXISTS(SELECT 1 FROM sqlite_sequence current WHERE current.name = old.name);
		UPDATE sqlite_sequence SET seq = MAX(seq, COALESCE((SELECT seq FROM artist_sequences
			WHERE name = sqlite_sequence.name), 0)) WHERE name IN ('artists', 'tracked_artists');
		DROP TABLE artist_sequences;

		DROP INDEX IF EXISTS idx_tracks_path;
		ALTER TABLE tracks DROP COLUMN pinned_source_url;
		ALTER TABLE tracks ADD COLUMN source_url TEXT;
		ALTER TABLE albums ADD COLUMN catalog_checked_at DATETIME;
		UPDATE albums SET is_complete = 0, expected_tracks_count = 0,
			present_tracks_count = (SELECT COUNT(*) FROM tracks WHERE album_id = albums.id);
		CREATE TABLE release_tracklist (
			album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
			disc_number INTEGER NOT NULL CHECK (disc_number > 0),
			track_number INTEGER NOT NULL CHECK (track_number > 0),
			title TEXT NOT NULL,
			artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE RESTRICT,
			artist_credit TEXT NOT NULL,
			expected_duration INTEGER NOT NULL DEFAULT 0,
			deezer_id TEXT NOT NULL DEFAULT '',
			deezer_url TEXT NOT NULL DEFAULT '',
			isrc TEXT NOT NULL DEFAULT '',
			is_present INTEGER NOT NULL DEFAULT 0 CHECK (is_present IN (0, 1)),
			PRIMARY KEY (album_id, disc_number, track_number)
		);
		CREATE INDEX idx_release_tracklist_artist ON release_tracklist(artist_id);
		CREATE TABLE library_artist_conflicts (
			file_path TEXT NOT NULL,
			artist_name TEXT NOT NULL,
			album_artist INTEGER NOT NULL,
			resolved_artist_id INTEGER REFERENCES artists(id) ON DELETE SET NULL,
			PRIMARY KEY (file_path, album_artist)
		);
		DROP TABLE missing_tracks;
		DROP TABLE pinned_track_sources;
		DELETE FROM soundcloud_uncensored_cache WHERE is_manual = 1 OR status = 'manual';
		ALTER TABLE soundcloud_uncensored_cache DROP COLUMN is_manual;
	`); err != nil {
		return err
	}

	for _, t := range triggers {
		switch t.table {
		case "missing_tracks", "pinned_track_sources":
			continue
		case "artists":
			var statements []string
			for _, statement := range strings.Split(t.sql, ";") {
				if !strings.Contains(statement, "UPDATE missing_tracks ") && !strings.Contains(statement, "UPDATE pinned_track_sources ") {
					statements = append(statements, statement)
				}
			}
			t.sql = strings.Join(statements, ";")
		}
		t.sql = strings.ReplaceAll(t.sql, "SELECT id FROM artists WHERE name != ''", "SELECT CASE WHEN COUNT(*) = 1 THEN MIN(id) END FROM artists WHERE name != ''")
		if _, err := conn.ExecContext(ctx, t.sql); err != nil {
			return err
		}
	}
	return nil
}
