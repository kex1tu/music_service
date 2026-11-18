package db_test

import (
	"database/sql"
	_ "embed"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/features/audit"
)

//go:embed testdata/schema_c827982.sql
var historicalDDL string

//go:embed testdata/schema_local_2026_11_18.sql
var localDDL string

//go:embed schema.sql
var canonicalDDL string

const seedLibrary = `
	INSERT INTO artists(id, name, clean_name) VALUES (11, 'Artist', 'artist');
	INSERT INTO albums(id, artist_id, title, clean_title, folder_path)
		VALUES (21, 11, 'Album', 'album', 'Artist/Album');
	INSERT INTO tracks(id, album_id, artist_id, title, clean_title, file_path, file_size, mtime)
		VALUES (31, 21, 11, 'Track', 'track', 'Artist/Album/Track.mp3', 1, 0);
`

const preserveArtist = `
	UPDATE tracked_artists SET clean_name = 'custom-clean', is_followed = 0, auto_download = 1,
		custom_search_alias = 'alias', deezer_id = '123', musicbrainz_id = 'mb-123', yandex_id = 'ya-123',
		last_checked_at = '2025-02-03 00:00:00', updated_at = '2025-02-03 01:00:00'
	WHERE name = 'Artist'
`

func execMigrationSQL(t *testing.T, conn *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := conn.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

func requireScalar[T comparable](t *testing.T, conn *sql.DB, query string, want T, args ...any) {
	t.Helper()
	var got T
	if err := conn.QueryRowContext(t.Context(), query, args...).Scan(&got); err != nil {
		t.Fatalf("query %s: %v", query, err)
	}
	if got != want {
		t.Fatalf("query %s: got %v, want %v", query, got, want)
	}
}

func rawMigrationDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { conn.Close() })
	return conn
}

func openMigrated(t *testing.T, path string) *db.DB {
	t.Helper()
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestSQLiteMigrationVariants(t *testing.T) {
	for _, fixture := range []struct {
		name      string
		ddl       string
		legacyLog bool
		pin       string
	}{
		{name: "empty"},
		{name: "historical", ddl: historicalDDL},
		{name: "local", ddl: localDDL, legacyLog: true, pin: "https://example.com/local"},
		{name: "canonical-unversioned", ddl: canonicalDDL, pin: "https://example.com/current"},
		{name: "partially-upgraded", ddl: historicalDDL + `
			ALTER TABLE tracks ADD COLUMN pinned_source_url TEXT;
			ALTER TABLE missing_tracks ADD COLUMN pinned_source_url TEXT;
			ALTER TABLE missing_tracks ADD COLUMN updated_at DATETIME;`, pin: "https://example.com/partial"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "library.db")
			if fixture.ddl != "" {
				raw := rawMigrationDB(t, path)
				execMigrationSQL(t, raw, fixture.ddl)
				execMigrationSQL(t, raw, seedLibrary)
				execMigrationSQL(t, raw, `INSERT INTO missing_tracks
					(id, album_id, title, artist, track_number, disc_number, status, created_at) VALUES
					(41, 21, 'Missing', 'Artist', 2, 1, 'queued', '2025-01-02 03:04:05'),
					(42, 21, 'Missing disc two', 'Artist', 2, 2, 'ignored', NULL)`)
				if fixture.pin != "" {
					execMigrationSQL(t, raw, "UPDATE missing_tracks SET pinned_source_url = ? WHERE id = 41", fixture.pin)
				}
				if fixture.legacyLog {
					execMigrationSQL(t, raw, `INSERT INTO audit_issues
						(id, issue_type, entity_type, entity_id, is_resolved, is_auto_fixed, created_at,
						 description, severity, suggested_action, details)
						VALUES (51, 'invalid_path', 'track', 31, 1, 1, '2025-01-02 03:04:06',
							'Old description', 'error', 'Old action', '{"legacy":true}')`)
					execMigrationSQL(t, raw, "UPDATE sqlite_sequence SET seq = 500 WHERE name = 'audit_issues'")
					execMigrationSQL(t, raw, "INSERT INTO tracked_artists(id, name, clean_name) VALUES (71, 'Artist', 'artist')")
					execMigrationSQL(t, raw, preserveArtist)
					execMigrationSQL(t, raw, "INSERT INTO artists(id, name, clean_name) VALUES (12, 'New Artist', 'new artist')")
				} else {
					execMigrationSQL(t, raw, `INSERT INTO audit_issues
						(id, issue_type, entity_type, entity_id, file_path, details, is_resolved, is_auto_fixed, created_at, updated_at)
						VALUES (51, 'invalid_path', 'track', 31, 'Artist/Album/Track.mp3', 'old details',
							1, 1, '2025-01-02 03:04:06', '2025-01-03 03:04:06')`)
				}
				if err := raw.Close(); err != nil {
					t.Fatal(err)
				}
			}

			database := openMigrated(t, path)
			conn := database.Conn()
			requireScalar(t, conn, "PRAGMA user_version", 4)
			requireScalar(t, conn, "SELECT count(*) FROM pragma_table_info('albums') WHERE name = 'youtube_id'", 1)
			requireScalar(t, conn, "SELECT count(*) FROM pragma_foreign_key_check", 0)
			if fixture.ddl == "" {
				execMigrationSQL(t, conn, seedLibrary)
				execMigrationSQL(t, conn, "INSERT INTO tracked_artists(name, clean_name) VALUES ('Artist', 'artist')")
			} else {
				requireScalar(t, conn, `SELECT count(*) FROM audit_issues WHERE id = 51 AND entity_type = 'track'
					AND entity_id = 31 AND issue_type = 'invalid_path' AND is_resolved = 1 AND is_auto_fixed = 1`, 1)
				requireScalar(t, conn, `SELECT count(*) FROM missing_tracks WHERE
					(id = 41 AND album_id = 21 AND track_number = 2 AND disc_number = 1 AND status = 'queued')
					OR (id = 42 AND album_id = 21 AND track_number = 2 AND disc_number = 2 AND status = 'ignored')`, 2)
				requireScalar(t, conn, "SELECT COALESCE(pinned_source_url, '') FROM missing_tracks WHERE id = 41", fixture.pin)
			}
			switch fixture.name {
			case "historical", "partially-upgraded":
				requireScalar(t, conn, "SELECT CAST(updated_at AS TEXT) FROM missing_tracks WHERE id = 41", "2025-01-02 03:04:05")
				requireScalar(t, conn, "SELECT count(*) FROM missing_tracks WHERE id = 42 AND updated_at IS NOT NULL", 1)
			}
			if fixture.legacyLog {
				requireScalar(t, conn, `SELECT count(*) FROM audit_issues WHERE id = 51 AND file_path IS NULL
					AND json_extract(details, '$.description') = 'Old description'
					AND json_extract(details, '$.severity') = 'error'
					AND json_extract(details, '$.suggested_action') = 'Old action'
					AND json_extract(details, '$.details') = '{"legacy":true}'
					AND updated_at = created_at`, 1)
				requireScalar(t, conn, "SELECT seq FROM sqlite_sequence WHERE name = 'audit_issues'", 500)
				requireScalar(t, conn, `SELECT count(*) FROM tracked_artists
					WHERE name = 'New Artist' AND clean_name = 'new artist' AND is_followed = 1`, 1)
				requireScalar(t, conn, `SELECT count(*) FROM sqlite_schema
					WHERE name IN ('idx_audit_issues_unresolved', 'idx_missing_tracks_status')`, 2)
				requireTrackedArtistSettings(t, conn)
				requireScalar(t, conn, "SELECT id FROM tracked_artists WHERE name = 'Artist'", 71)
			} else {
				if fixture.ddl != "" {
					requireScalar(t, conn, `SELECT count(*) FROM audit_issues WHERE id = 51
						AND file_path = 'Artist/Album/Track.mp3' AND details = 'old details'
						AND updated_at = '2025-01-03 03:04:06'`, 1)
				}
				requireScalar(t, conn, `SELECT count(*) FROM tracked_artists
					WHERE name = 'Artist' AND clean_name = 'artist' AND is_followed = 1`, 1)
				execMigrationSQL(t, conn, preserveArtist)
			}

			checkMigrationQueries(t, database, 4)
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openMigrated(t, path)
			requireScalar(t, reopened.Conn(), "PRAGMA user_version", 4)
			requireTrackedArtistSettings(t, reopened.Conn())
			requireScalar(t, reopened.Conn(), "SELECT count(*) FROM tracked_artists WHERE name = 'Artist'", 1)
			checkMigrationQueries(t, reopened, 5)
		})
	}
}

func requireTrackedArtistSettings(t *testing.T, conn *sql.DB) {
	t.Helper()
	requireScalar(t, conn, `SELECT count(*) FROM tracked_artists
		WHERE name = 'Artist' AND is_followed = 0 AND auto_download = 1
		AND custom_search_alias = 'alias' AND deezer_id = '123' AND musicbrainz_id = 'mb-123'
		AND yandex_id = 'ya-123' AND last_checked_at = '2025-02-03 00:00:00'
		AND updated_at = '2025-02-03 01:00:00'`, 1)
}

func checkMigrationQueries(t *testing.T, database *db.DB, position int) {
	t.Helper()
	conn := database.Conn()
	repo := audit.NewRepository(conn)
	ctx := t.Context()
	trackID := int64(31)
	path := "Artist/Album/Track.mp3"
	issueID, err := repo.LogAuditIssue(ctx, audit.AuditIssue{
		IssueType: "bad_tags", EntityType: "track", EntityID: &trackID, FilePath: path, Details: "test details",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ResolveAuditIssueByPath(ctx, path, "bad_tags"); err != nil {
		t.Fatal(err)
	}
	requireScalar(t, conn, "SELECT count(*) FROM audit_issues WHERE id = ? AND is_resolved = 1 AND updated_at IS NOT NULL", 1, issueID)
	if err := repo.DeleteAuditIssueByPath(ctx, path, "bad_tags"); err != nil {
		t.Fatal(err)
	}
	requireScalar(t, conn, "SELECT count(*) FROM audit_issues WHERE id = ?", 0, issueID)

	track := db.CatalogTrack{
		BackfillSelection: db.BackfillSelection{TrackPosition: db.ReleasePosition(1, position)},
		Title:             "First", Artist: "Artist", Duration: 180,
	}
	updated := track
	updated.Title = "Updated"
	discTwo := updated
	discTwo.DiscNumber = 2
	if _, err := repo.VerifyCatalog(ctx, 21, "123", []db.CatalogTrack{track, discTwo}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.VerifyCatalog(ctx, 21, "123", []db.CatalogTrack{updated, discTwo}); err != nil {
		t.Fatal(err)
	}
	missing, err := repo.GetMissingTracksForAlbum(ctx, "Artist/Album")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 2 {
		t.Fatalf("expected two discs after upsert, got %+v", missing)
	}
	requireScalar(t, conn, "SELECT title FROM missing_tracks WHERE album_id = 21 AND track_number = ? AND disc_number = 1", "Updated", position)
	requireScalar(t, conn, "SELECT count(*) FROM missing_tracks WHERE updated_at IS NOT NULL", 2)
	requireScalar(t, conn, "SELECT dflt_value FROM pragma_table_info('missing_tracks') WHERE name = 'updated_at'", "CURRENT_TIMESTAMP")
	execMigrationSQL(t, conn, "INSERT INTO missing_tracks(album_id, title, artist, track_number) VALUES (21, 'Defaults', 'Artist', ?)", position+1)
	requireScalar(t, conn, "SELECT count(*) FROM missing_tracks WHERE track_number = ? AND updated_at IS NOT NULL", 1, position+1)
}

func TestSQLiteMigrationUnknownLayout(t *testing.T) {
	for _, fixture := range []struct{ name, ddl, want string }{
		{"unexpected table", historicalDDL + "CREATE TABLE surprise (id INTEGER);", "surprise"},
		{"missing key", strings.Replace(historicalDDL, ",\n    UNIQUE(album_id, track_number, disc_number)", "", 1), "missing_tracks"},
		{"unknown constraint", strings.Replace(localDDL, "description TEXT NOT NULL", "description TEXT CHECK(length(description) > 1) NOT NULL", 1), "audit_issues"},
		{"future version", canonicalDDL + "PRAGMA user_version = 11;", "user_version=11"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "library.db")
			raw := rawMigrationDB(t, path)
			execMigrationSQL(t, raw, fixture.ddl)
			execMigrationSQL(t, raw, seedLibrary)
			opened, err := db.Open(path)
			if opened != nil {
				opened.Close()
			}
			if err == nil || !strings.Contains(err.Error(), fixture.want) {
				t.Fatalf("expected error identifying %q, got %v", fixture.want, err)
			}
			requireScalar(t, raw, "SELECT count(*) FROM tracks WHERE id = 31", 1)
		})
	}
}

func TestSQLiteMigrationRollsBackOnForeignKeyFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.db")
	raw := rawMigrationDB(t, path)
	execMigrationSQL(t, raw, historicalDDL)
	execMigrationSQL(t, raw, seedLibrary)
	execMigrationSQL(t, raw, `
		PRAGMA foreign_keys = OFF;
		INSERT INTO tracks(id, album_id, artist_id, title, clean_title, file_path, file_size, mtime)
			VALUES (32, 999, 11, 'Orphan', 'orphan', 'Orphan.mp3', 1, 0);
		PRAGMA foreign_keys = ON;
		INSERT INTO missing_tracks(id, album_id, title, artist, track_number, status)
			VALUES (41, 21, 'Missing', 'Artist', 2, 'queued');
	`)
	// This orphan survives the data copy and is discovered by the final check.
	database, err := db.Open(path)
	if database != nil {
		database.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "foreign_key_check") {
		t.Fatalf("expected foreign_key_check to fail, got %v", err)
	}
	requireScalar(t, raw, "PRAGMA user_version", 0)
	requireScalar(t, raw, "SELECT count(*) FROM sqlite_schema WHERE name IN ('tracked_artists', 'missing_tracks_v0')", 0)
	requireScalar(t, raw, "SELECT count(*) FROM pragma_table_info('missing_tracks') WHERE name IN ('updated_at', 'pinned_source_url')", 0)
	requireScalar(t, raw, "SELECT status FROM missing_tracks WHERE id = 41", "queued")

	execMigrationSQL(t, raw, "UPDATE tracks SET album_id = 21 WHERE id = 32")
	reopened := openMigrated(t, path)
	requireScalar(t, reopened.Conn(), "PRAGMA user_version", 4)
	requireScalar(t, reopened.Conn(), "SELECT status FROM missing_tracks WHERE id = 41", "queued")
	requireScalar(t, reopened.Conn(), "SELECT count(*) FROM pragma_foreign_key_check", 0)
}

func TestSQLiteMigrationRestoredWALBackup(t *testing.T) {
	paths, err := sandbox.NewPathSandbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(name string) string {
		t.Helper()
		path, err := paths.Resolve(name, false)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	sourcePath := resolve("source.db")
	raw := rawMigrationDB(t, sourcePath)
	execMigrationSQL(t, raw, historicalDDL)
	execMigrationSQL(t, raw, seedLibrary)
	requireScalar(t, raw, "PRAGMA journal_mode", "wal")
	execMigrationSQL(t, raw, "PRAGMA wal_checkpoint(TRUNCATE)")
	execMigrationSQL(t, raw, "PRAGMA wal_autocheckpoint = 0")
	execMigrationSQL(t, raw, `INSERT INTO missing_tracks(id, album_id, title, artist, track_number, status)
		VALUES (41, 21, 'WAL-only row', 'Artist', 2, 'queued')`)

	copyFile := func(from, to string) {
		t.Helper()
		contents, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A copy of just the main file shows that the last commit is still in WAL.
	barePath := resolve("main-file-only.db")
	copyFile(sourcePath, barePath)
	requireScalar(t, rawMigrationDB(t, barePath), "SELECT count(*) FROM missing_tracks WHERE id = 41", 0)

	backupPath := resolve("backup.db")
	execMigrationSQL(t, raw, "VACUUM INTO ?", backupPath)
	restoredPath := resolve("restored.db")
	copyFile(backupPath, restoredPath)
	restored := rawMigrationDB(t, restoredPath)
	requireScalar(t, restored, "PRAGMA integrity_check", "ok")
	requireScalar(t, restored, "SELECT count(*) FROM pragma_foreign_key_check", 0)
	requireScalar(t, restored, "SELECT title FROM missing_tracks WHERE id = 41", "WAL-only row")
	requireScalar(t, restored, "PRAGMA user_version", 0)
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}

	migrated := openMigrated(t, restoredPath)
	requireScalar(t, migrated.Conn(), "PRAGMA user_version", 4)
	requireScalar(t, migrated.Conn(), "PRAGMA integrity_check", "ok")
	requireScalar(t, migrated.Conn(), "SELECT count(*) FROM pragma_foreign_key_check", 0)
	requireScalar(t, migrated.Conn(), "SELECT status FROM missing_tracks WHERE id = 41", "queued")
	checkMigrationQueries(t, migrated, 3)
	requireScalar(t, raw, "PRAGMA user_version", 0)
	requireScalar(t, raw, "SELECT status FROM missing_tracks WHERE id = 41", "queued")
}
