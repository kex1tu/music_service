package db

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"regexp"
	"sort"
	"time"
)

var (
	legacyAlbumIdentity = regexp.MustCompile(`(?i)unique\s*\(\s*artist_id\s*,\s*title\s*\)`)
	legacyRadarIdentity = regexp.MustCompile(`(?i)unique\s*\(\s*clean_artist_name\s*,\s*clean_title\s*,\s*release_type\s*\)`)
	createTablePrefix   = regexp.MustCompile(`(?i)^create\s+table\s+[^\(]+\(`)
)

// migrateReleaseModel uses its own pinned connection because SQLite requires
// foreign_keys to be disabled before the transaction that replaces parent tables.
func (db *DB) migrateReleaseModel() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := db.conn.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON")
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")

	var albumDDL, radarDDL string
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'albums'),
		(SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'radar_releases')`).Scan(&albumDDL, &radarDDL); err != nil {
		return err
	}
	legacyAlbums, legacyRadar := legacyAlbumIdentity.MatchString(albumDDL), legacyRadarIdentity.MatchString(radarDDL)
	var hasSelection int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info('download_jobs') WHERE name = 'backfill_selection'").Scan(&hasSelection); err != nil {
		return err
	}
	if !legacyAlbums && !legacyRadar && hasSelection != 0 {
		return nil
	}
	var gapTimestamp int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info('missing_tracks') WHERE name = 'updated_at'").Scan(&gapTimestamp); err != nil {
		return err
	}
	if gapTimestamp == 0 {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE missing_tracks ADD COLUMN updated_at DATETIME;
			UPDATE missing_tracks SET updated_at = COALESCE(created_at, CURRENT_TIMESTAMP)`); err != nil {
			return err
		}
	}
	if legacyAlbums {
		if err := migrateLocalAlbums(ctx, conn, albumDDL); err != nil {
			return fmt.Errorf("local albums: %w", err)
		}
	}
	if legacyRadar {
		if err := migrateRadarIdentity(ctx, conn, radarDDL); err != nil {
			return fmt.Errorf("radar identities: %w", err)
		}
	}
	if err := migrateTrackedArtists(ctx, conn); err != nil {
		return fmt.Errorf("tracked artists: %w", err)
	}
	if hasSelection == 0 {
		if _, err := conn.ExecContext(ctx, "ALTER TABLE download_jobs ADD COLUMN backfill_selection TEXT"); err != nil {
			return err
		}
	}

	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	if rows.Next() {
		var table, parent string
		var row sql.NullInt64
		var fk int
		err := rows.Scan(&table, &row, &parent, &fk)
		rows.Close()
		if err != nil {
			return err
		}
		return fmt.Errorf("foreign key violation in %s (row %v, references %s)", table, row, parent)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func migrateLocalAlbums(ctx context.Context, conn *sql.Conn, ddl string) error {
	create := createTablePrefix.ReplaceAllString(ddl, "CREATE TABLE local_albums (")
	create = legacyAlbumIdentity.ReplaceAllString(create, "UNIQUE(folder_path)")
	if _, err := conn.ExecContext(ctx, create); err != nil {
		return err
	}

	type legacyAlbum struct {
		id     int64
		root   string
		tracks map[string][]int64
	}
	albums := make(map[int64]*legacyAlbum)
	var order []int64
	discNumbers := make(map[int64]int)
	rows, err := conn.QueryContext(ctx, `SELECT a.id, a.folder_path, t.id, t.file_path
		FROM albums a LEFT JOIN tracks t ON t.album_id = a.id ORDER BY a.id, t.file_path`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var root string
		var trackID sql.NullInt64
		var file sql.NullString
		if err := rows.Scan(&id, &root, &trackID, &file); err != nil {
			rows.Close()
			return err
		}
		if albums[id] == nil {
			albums[id] = &legacyAlbum{root: AlbumRoot(root), tracks: make(map[string][]int64)}
			order = append(order, id)
		}
		if trackID.Valid {
			parent := path.Dir(file.String)
			directory := AlbumRoot(parent)
			if parent != directory {
				discNumbers[trackID.Int64] = DiscFolderNumber(path.Base(parent))
			}
			albums[id].tracks[directory] = append(albums[id].tracks[directory], trackID.Int64)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}

	for id, disc := range discNumbers {
		if _, err := conn.ExecContext(ctx, "UPDATE tracks SET disc_number = ? WHERE id = ? AND COALESCE(disc_number, 1) <> ?", disc, id, disc); err != nil {
			return err
		}
	}

	const metadata = `artist_id, title, clean_title, album_artist, year, cover_path, has_cover, cover_mime,
		is_complete, expected_tracks_count, present_tracks_count, total_tracks, total_discs, is_path_valid,
		deezer_id, spotify_id, youtube_id, yandex_id, genius_id, created_at, updated_at`
	owners := make(map[string]int64)
	dirty := make(map[int64]bool)
	for _, id := range order {
		a := albums[id]
		if _, ok := a.tracks[a.root]; !ok && len(a.tracks) > 0 {
			roots := make([]string, 0, len(a.tracks))
			for root := range a.tracks {
				roots = append(roots, root)
			}
			sort.Strings(roots)
			a.root = roots[0]
		}
		owner, exists := owners[a.root]
		if !exists {
			owner = id
			owners[a.root] = owner
			if _, err := conn.ExecContext(ctx, `INSERT INTO local_albums (id, folder_path, `+metadata+`)
				SELECT id, ?, `+metadata+` FROM albums WHERE id = ?`, a.root, id); err != nil {
				return err
			}
		} else {
			dirty[owner] = true
			// Preserve gaps and prefer their explicit pins when duplicate album rows
			// described the same folder before the migration.
			if _, err := conn.ExecContext(ctx, `UPDATE missing_tracks AS dest SET
				pinned_source_url = COALESCE(NULLIF(dest.pinned_source_url, ''), src.pinned_source_url),
				status = CASE WHEN COALESCE(dest.status, 'missing') = 'missing' THEN src.status ELSE dest.status END
				FROM missing_tracks AS src
				WHERE dest.album_id = ? AND src.album_id = ?
				AND dest.track_number = src.track_number AND dest.disc_number = src.disc_number`, owner, id); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, "UPDATE OR IGNORE missing_tracks SET album_id = ? WHERE album_id = ?", owner, id); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, "DELETE FROM missing_tracks WHERE album_id = ?", id); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, "UPDATE audit_issues SET entity_id = ? WHERE entity_type = 'album' AND entity_id = ?", owner, id); err != nil {
				return err
			}
		}
	}

	for _, id := range order {
		a := albums[id]
		roots := make([]string, 0, len(a.tracks))
		for root := range a.tracks {
			roots = append(roots, root)
		}
		sort.Strings(roots)
		for _, root := range roots {
			owner, exists := owners[root]
			if !exists {
				// Catalog matches, gaps and folder-level covers belong to the original root.
				var err error
				err = conn.QueryRowContext(ctx, `INSERT INTO local_albums
					(folder_path, artist_id, title, clean_title, album_artist, year, has_cover)
					SELECT ?, artist_id, title, clean_title, album_artist, year, 0 FROM albums WHERE id = ?
					RETURNING id`, root, id).Scan(&owner)
				if err != nil {
					return err
				}
				owners[root] = owner
			}
			if owner == id {
				continue
			}
			dirty[owner] = true
			dirty[owners[a.root]] = true
			for _, trackID := range a.tracks[root] {
				if _, err := conn.ExecContext(ctx, "UPDATE tracks SET album_id = ? WHERE id = ?", owner, trackID); err != nil {
					return err
				}
			}
		}
	}
	for id := range dirty {
		if _, err := conn.ExecContext(ctx, `UPDATE local_albums SET
			present_tracks_count = (SELECT count(*) FROM tracks WHERE album_id = ?1),
			total_discs = (SELECT max(1, COALESCE(MAX(disc_number), 1)) FROM tracks WHERE album_id = ?1),
			has_cover = max(COALESCE(has_cover, 0), COALESCE((SELECT MAX(has_embedded_cover) FROM tracks WHERE album_id = ?1), 0)),
			is_complete = NOT EXISTS(SELECT 1 FROM missing_tracks WHERE album_id = ?1)
				AND (SELECT count(*) FROM tracks WHERE album_id = ?1) >= COALESCE(expected_tracks_count, 0)
			WHERE id = ?1`, id); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, `
		DROP TABLE albums;
		ALTER TABLE local_albums RENAME TO albums;
		CREATE INDEX idx_albums_clean ON albums(clean_title);
		CREATE INDEX idx_albums_complete ON albums(is_complete);
		CREATE INDEX idx_albums_deezer ON albums(deezer_id);
		CREATE INDEX IF NOT EXISTS idx_tracks_album_id ON tracks(album_id);
	`)
	return err
}

func migrateRadarIdentity(ctx context.Context, conn *sql.Conn, ddl string) error {
	create := createTablePrefix.ReplaceAllString(ddl, "CREATE TABLE catalog_releases (")
	create = legacyRadarIdentity.ReplaceAllString(create, "identity_key TEXT UNIQUE, tracklist TEXT")
	if _, err := conn.ExecContext(ctx, create); err != nil {
		return err
	}
	rows, err := conn.QueryContext(ctx, `SELECT id, artist_name, title, release_type,
		COALESCE(deezer_id, ''), COALESCE(musicbrainz_id, ''), COALESCE(shazam_id, '')
		FROM radar_releases ORDER BY CASE status WHEN 'ignored' THEN 0 WHEN 'downloaded' THEN 1 ELSE 2 END, id`)
	if err != nil {
		return err
	}
	type entry struct {
		id  int64
		key string
	}
	var entries []entry
	for rows.Next() {
		var id int64
		var artist, title, kind, deezer, mb, shazam string
		if err := rows.Scan(&id, &artist, &title, &kind, &deezer, &mb, &shazam); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, entry{id, RadarIdentityKey(artist, title, kind, deezer, mb, shazam)})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, err := conn.ExecContext(ctx, `INSERT INTO catalog_releases SELECT
			id, artist_name, music_search_key(artist_name), title, clean_title, release_type, release_date, year, cover_url,
			track_count, deezer_id, musicbrainz_id, shazam_id, source_url, status,
			CASE WHEN status = 'new' THEN is_radar ELSE 0 END,
			created_at, updated_at, ?, NULL FROM radar_releases WHERE id = ?
			ON CONFLICT(identity_key) DO NOTHING`, e.key, e.id); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, `
		DROP TABLE radar_releases;
		ALTER TABLE catalog_releases RENAME TO radar_releases;
		CREATE INDEX idx_radar_releases_artist ON radar_releases(clean_artist_name);
		CREATE INDEX idx_radar_releases_status ON radar_releases(status);
		CREATE INDEX idx_radar_releases_is_radar ON radar_releases(is_radar);
		CREATE INDEX idx_radar_releases_artist_status ON radar_releases(clean_artist_name, status);
		CREATE INDEX idx_radar_releases_clean_title ON radar_releases(clean_title);
	`)
	return err
}

func migrateTrackedArtists(ctx context.Context, conn *sql.Conn) error {
	// Merge equivalent rows and preserve separate provider IDs or settings.
	// The surviving row retains its original display name and stable ID.
	_, err := conn.ExecContext(ctx, `
		UPDATE artists SET clean_name = music_search_key(name);
		UPDATE tracked_artists SET clean_name = music_search_key(name);
		CREATE TEMP TABLE equivalent_subscriptions AS
		SELECT id, MIN(id) OVER subscriptions AS keep_id FROM tracked_artists
		WINDOW subscriptions AS (PARTITION BY clean_name, is_followed, auto_download,
			COALESCE(custom_search_alias, ''), COALESCE(deezer_id, ''),
			COALESCE(musicbrainz_id, ''), COALESCE(yandex_id, ''));
		UPDATE tracked_artists AS ta SET last_checked_at = (
			SELECT MAX(old.last_checked_at) FROM tracked_artists AS old
			JOIN equivalent_subscriptions AS eq ON eq.id = old.id WHERE eq.keep_id = ta.id
		) WHERE id IN (SELECT keep_id FROM equivalent_subscriptions);
		DELETE FROM tracked_artists WHERE id IN (
			SELECT id FROM equivalent_subscriptions WHERE id <> keep_id
		);
		DROP TABLE equivalent_subscriptions;
	`)
	return err
}
