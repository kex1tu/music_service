package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestReleaseIdentityMigration(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	mustReleaseSQL(t, raw, schemaV1)
	mustReleaseSQL(t, raw, "ALTER TABLE missing_tracks DROP COLUMN updated_at")
	mustReleaseSQL(t, raw, `
		INSERT INTO artists (id, name, clean_name) VALUES (1, 'Ёлка', 'ёлка');
		INSERT INTO albums (id, title, clean_title, artist_id, folder_path, deezer_id)
		VALUES (17, 'Live', 'live', 1, 'CD edition/CD1', '200');
		INSERT INTO tracks (id, title, clean_title, artist_id, album_id, track_number, file_path, file_size, mtime)
		VALUES (41, 'Intro', 'intro', 1, 17, 1, 'CD edition/CD1/01.mp3', 1, 1),
		       (42, 'Outro', 'outro', 1, 17, 1, 'CD edition/Disc 2/01.mp3', 1, 1),
		       (43, 'Intro', 'intro', 1, 17, 1, 'Digital edition/01.mp3', 1, 1);
		INSERT INTO missing_tracks (id, album_id, title, artist, track_number, disc_number, status, pinned_source_url)
		VALUES (48, 17, 'Gap', 'Ёлка', 2, 2, 'reviewed', 'pin');
		INSERT INTO tracked_artists (id, name, clean_name, is_followed, deezer_id)
		VALUES (1, 'Ёлка', 'ёлка', 0, '100'), (2, 'ЁЛКА', 'ёлка', 1, '200'),
		       (3, 'ёлка', 'ёлка', 0, '100');
		INSERT INTO radar_releases (id, artist_name, clean_artist_name, title, clean_title, release_type, deezer_id, source_url, status)
		VALUES (1, 'Ёлка', 'ёлка', 'Live', 'live', 'album', '200', 'example', 'ignored'),
		       (2, 'Другой', 'другой', 'Live', 'live', 'album', '200', 'example', 'new');
		INSERT INTO radio_history (track_id, station_type) VALUES (42, 'wave');
  UPDATE sqlite_sequence SET seq = 1000 WHERE name IN ('albums', 'radar_releases');
	`)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		db, err := openReleaseMigrationV6(ctx, file)
		if err != nil {
			t.Fatal(err)
		}
		var first, second, other, gapAlbum int64
		var disc, subscriptions, history int
		var pin, status, root, updated string
		if err := db.conn.QueryRowContext(ctx, `SELECT
			(SELECT album_id FROM tracks WHERE id=41), (SELECT album_id FROM tracks WHERE id=42),
			(SELECT album_id FROM tracks WHERE id=43), (SELECT album_id FROM missing_tracks WHERE id=48),
			(SELECT disc_number FROM tracks WHERE id=42), (SELECT count(*) FROM tracked_artists),
			(SELECT count(*) FROM radio_history WHERE track_id=42),
			(SELECT pinned_source_url FROM missing_tracks WHERE id=48),
			(SELECT status FROM radar_releases WHERE deezer_id='200'),
			(SELECT folder_path FROM albums WHERE id=17),
			(SELECT CAST(updated_at AS TEXT) FROM missing_tracks WHERE id=48)`).Scan(&first, &second, &other, &gapAlbum,
			&disc, &subscriptions, &history, &pin, &status, &root, &updated); err != nil {
			t.Fatal(err)
		}
		if first != 17 || second != first || other <= 1000 || gapAlbum != first ||
			disc != 2 || subscriptions != 2 || history != 1 || pin != "pin" || status != "ignored" || root != "CD edition" || updated == "" {
			t.Fatalf("migration lost identity or user state: album IDs %d/%d/%d, gap album %d, disc %d, subscriptions %d, radio %d, pin %q, status %q, root %q",
				first, second, other, gapAlbum, disc, subscriptions, history, pin, status, root)
		}
		var radarSequence int
		if err := db.conn.QueryRow("SELECT seq FROM sqlite_sequence WHERE name='radar_releases'").Scan(&radarSequence); err != nil || radarSequence < 1000 {
			t.Fatalf("radar sequence = %d, err = %v", radarSequence, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// This fixture exercises migration 4 legacy gap preservation before migration 7 drops the old table.
func openReleaseMigrationV6(ctx context.Context, path string) (*DB, error) {
	return openTestSchemaVersion(ctx, path, 6)
}

func openTestSchemaVersion(ctx context.Context, path string, target int) (*DB, error) {
	conn, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return nil, err
	}
	c, err := conn.Conn(ctx)
	if err != nil {
		conn.Close()
		return nil, err
	}
	defer c.Close()
	ok := false
	defer func() {
		if !ok {
			c.Close()
			conn.Close()
		}
	}()
	var version int
	if err := c.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	for _, migrate := range migrations[version:target] {
		if err := migrate(ctx, c); err != nil {
			return nil, err
		}
	}
	if _, err := c.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", target)); err != nil {
		return nil, err
	}
	ok = true
	return &DB{conn: conn}, nil
}

func TestVerifyCatalogKeepsSnapshotAndRollsBack(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "gaps.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustReleaseSQL(t, db.conn, `
		INSERT INTO artists (id, name, clean_name) VALUES (1, 'Band', 'band');
		INSERT INTO albums (id, title, clean_title, artist_id, folder_path) VALUES (1, 'Album', 'album', 1, 'Band/Album');
		INSERT INTO tracks (album_id, artist_id, title, clean_title, track_number, disc_number, duration, file_path, file_size, mtime)
		VALUES (1, 1, 'Intro', 'intro', 1, 2, 101, 'Band/Album/CD2/01.mp3', 1, 1);
		INSERT INTO release_tracklist (album_id, title, artist_id, artist_credit, disc_number, track_number)
		VALUES (1, 'Old title', 1, 'Band', 1, 1), (1, 'Intro', 1, 'Band', 2, 1);
		UPDATE albums SET catalog_checked_at = CURRENT_TIMESTAMP;
	`)
	first, second := ReleasePosition(1, 1), ReleasePosition(2, 1)
	catalog := []CatalogTrack{
		{BackfillSelection: BackfillSelection{TrackPosition: first, DeezerID: "2001"}, Title: "Intro", Artist: "Band", Duration: 100},
		{BackfillSelection: BackfillSelection{TrackPosition: second, DeezerID: "2002"}, Title: "Intro", Artist: "Band", Duration: 101},
	}
	mustReleaseSQL(t, db.conn, `CREATE TRIGGER refuse_verification BEFORE UPDATE OF expected_tracks_count ON albums
		BEGIN SELECT RAISE(ABORT, 'verification refused'); END`)
	if _, err := VerifyCatalog(ctx, db.conn, 1, "200", catalog); err == nil {
		t.Fatal("expected transaction failure")
	}
	var count int
	var title string
	if err := db.conn.QueryRow("SELECT count(*), min(CASE WHEN disc_number = 1 THEN title END) FROM release_tracklist").Scan(&count, &title); err != nil || count != 2 || title != "Old title" {
		t.Fatalf("failed verification changed gaps: count=%d title=%q err=%v", count, title, err)
	}
	mustReleaseSQL(t, db.conn, "DROP TRIGGER refuse_verification")
	matches, err := VerifyCatalog(ctx, db.conn, 1, "200", catalog)
	if err != nil || len(matches) != 1 || matches[first] || !matches[second] {
		t.Fatalf("incorrect multidisc matches: %v, %v", matches, err)
	}
	var artistID int64
	var externalID string
	var expected, present, countPresent int
	err = db.conn.QueryRow(`SELECT r.artist_id, r.deezer_id, a.expected_tracks_count, a.present_tracks_count,
		(SELECT COUNT(*) FROM release_tracklist WHERE is_present = 1)
		FROM release_tracklist r JOIN albums a ON a.id=r.album_id WHERE r.is_present = 0`).
		Scan(&artistID, &externalID, &expected, &present, &countPresent)
	if err != nil || artistID != 1 || externalID != "2001" || expected != 2 || present != 1 || countPresent != 1 {
		t.Fatalf("tracklist or aggregates changed incorrectly: %d %q %d/%d %d %v", artistID, externalID, present, expected, countPresent, err)
	}
}

func TestRadarIdentityAndLibraryMatching(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "radar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustReleaseSQL(t, db.conn, `
		INSERT INTO artists (id, name, clean_name) VALUES (1, 'Другой', 'другой'), (2, 'Первый', 'первый');
		INSERT INTO albums (id, title, clean_title, artist_id, folder_path) VALUES (1, 'Singles', 'singles', 1, 'Singles');
		INSERT INTO tracks (album_id, artist_id, title, clean_title, file_path, file_size, mtime)
		VALUES (1, 1, 'Intro', 'intro', 'Singles/01.mp3', 1, 1);
	`)
	add := func(artist, kind, deezer, mb, status string) RadarReleaseEntity {
		t.Helper()
		r := RadarReleaseEntity{ArtistName: artist, Title: "Intro", ReleaseType: kind, DeezerID: deezer,
			MusicBrainzID: mb, Status: status, IsRadar: status == "new"}
		if err := db.UpsertRadarRelease(ctx, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	add("Первый", "album", "", "", "new")
	add("Второй", "album", "", "", "new")
	add("Первый", "single", "12", "", "new")
	add("Первый, Третий", "single", "12", "", "new")
	add("Первый", "single", "", "12", "new")
	add("Третий", "album", "13", "", "new")
	add("Третий", "album", "14", "", "new")
	add("Другой", "single", "", "", "new")
	ignored := add("Другой", "single", "15", "", "ignored")
	if updated := add("Другой", "single", "15", "", "new"); updated.ID != ignored.ID || updated.Status != "ignored" {
		t.Fatalf("refresh changed ignored release: %+v", updated)
	}
	if _, err := db.ReconcileAllRadarReleases(ctx); err != nil {
		t.Fatal(err)
	}
	feed, total, err := db.GetRadarFeed(ctx, "all", "", 30, 0)
	// The one local single was promoted to a catalog identity and explicitly ignored.
	if err != nil || total != 6 || len(feed) != 6 {
		t.Fatalf("unexpected radar feed: count=%d items=%+v err=%v", total, feed, err)
	}
	for _, query := range []string{"ПЕРВЫЙ", "первый"} {
		_, total, err = db.GetRadarFeed(ctx, "all", query, 30, 0)
		if err != nil || total != 3 {
			t.Fatalf("Unicode feed search %q: total=%d err=%v", query, total, err)
		}
	}
}

func mustReleaseSQL(t *testing.T, conn *sql.DB, query string) {
	t.Helper()
	if _, err := conn.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func TestUnicodeLibrarySearchFollowsRetag(t *testing.T) {
	ctx := t.Context()
	db, err := Open(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustReleaseSQL(t, db.conn, `
		INSERT INTO artists (id, name, clean_name) VALUES (1, 'Ёлка', 'ёлка');
		INSERT INTO albums (id, title, clean_title, artist_id, folder_path) VALUES (1, 'Концерт', 'концерт', 1, 'Live');
		INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, mtime)
		VALUES (1, 1, 1, 'Песня', 'песня', 'Live/01.mp3', 1, 1);
	`)
	for _, query := range []string{"ПЕСНЯ", "песня"} {
		result, err := db.SearchLibrary(ctx, query, 10)
		if err != nil || len(result.Tracks) != 1 || result.Tracks[0].Title != "Песня" {
			t.Fatalf("Unicode search %q: %+v, %v", query, result, err)
		}
	}
	mustReleaseSQL(t, db.conn, "UPDATE tracks SET title = 'Съёмка', clean_title = 'съёмка' WHERE id = 1")
	result, err := db.SearchLibrary(ctx, "СЪЁМКА", 10)
	if err != nil || len(result.Tracks) != 1 || result.Tracks[0].Title != "Съёмка" {
		t.Fatalf("Unicode search after updating title: %+v, %v", result, err)
	}
}
