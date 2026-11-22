package db

import (
	"database/sql"
	_ "embed"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed testdata/schema_c827982.sql
var historicalSchema string

func TestSQLiteMigrationRetriesAfterDiskFull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.db")
	ctx := t.Context()
	conn, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1) // max_page_count applies to this connection.

	if _, err := conn.ExecContext(ctx, historicalSchema+`
		INSERT INTO artists(id, name, clean_name) VALUES (1, 'Artist', 'artist');
		INSERT INTO albums(id, artist_id, title, clean_title, folder_path)
			VALUES (2, 1, 'Album', 'album', 'Artist/Album');
		INSERT INTO missing_tracks(id, album_id, title, artist, track_number, status, created_at)
			VALUES (3, 2, 'Missing', 'Artist', 1, 'queued', '2025-01-02 03:04:05');
	`); err != nil {
		t.Fatal(err)
	}
	var pages int
	if err := conn.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	// Inject SQLITE_FULL as migration 1 starts creating the target schema.
	if _, err := conn.ExecContext(ctx, "PRAGMA max_page_count = "+strconv.Itoa(pages)); err != nil {
		t.Fatal(err)
	}
	var sqliteErr *sqlite.Error
	err = (&DB{conn: conn}).migrate()
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != sqlite3.SQLITE_FULL ||
		!strings.Contains(err.Error(), "создание таблиц и индексов") {
		t.Fatalf("expected SQLITE_FULL after applying the ALTERs, got %v", err)
	}

	var version, addedColumns int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != 0 {
		t.Fatalf("user_version=%d err=%v", version, err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('missing_tracks')
		WHERE name IN ('updated_at', 'pinned_source_url')`).Scan(&addedColumns); err != nil || addedColumns != 0 {
		t.Fatalf("new columns=%d err=%v", addedColumns, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	// The page limit is connection-local. A normal reopen retries against the
	// same on-disk content and completes without any data repair.
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := libraryRowCount(t, reopened.Conn(), "SELECT COUNT(*) FROM release_tracklist"); got != 0 {
		t.Fatalf("legacy gap unexpectedly became a catalog snapshot: %d", got)
	}
	if err := reopened.Conn().QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("user_version=%d err=%v", version, err)
	}
}
