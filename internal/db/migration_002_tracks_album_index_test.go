package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// This is the same UPDATE measured in the November 18 database audit.
const auditAlbumAggregateUpdate = `
	UPDATE albums SET
		present_tracks_count = (SELECT COUNT(*) FROM tracks WHERE album_id = albums.id),
		total_tracks = CASE WHEN total_tracks <= 0 THEN
			COALESCE((SELECT MAX(track_total) FROM tracks WHERE album_id = albums.id), 0)
			ELSE total_tracks END,
		updated_at = CURRENT_TIMESTAMP
`

func TestTracksAlbumIndexUpgrade(t *testing.T) {
	path := seedAlbumIndexFixture(t, 2)
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	conn := database.Conn()
	checkAlbumIndexPlan(t, conn, true)
	var version int
	if err := conn.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("user_version = %d, err = %v, want %d", version, err, len(migrations))
	}
	if _, err := conn.Exec(auditAlbumAggregateUpdate); err != nil {
		t.Fatal(err)
	}
	checkAlbumIndexCounters(t, conn, 2)
}

func BenchmarkAuditAlbumAggregate(b *testing.B) {
	for _, tc := range []struct {
		name    string
		indexed bool
	}{
		{"without_index", false},
		{"with_index", true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			const albums = 1000
			path := seedAlbumIndexFixture(b, albums)
			if tc.indexed {
				database, err := Open(path)
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { database.Close() })
				runAlbumIndexBenchmark(b, database.Conn(), albums, true)
				return
			}
			runAlbumIndexBenchmark(b, openAlbumIndexFixture(b, path), albums, false)
		})
	}
}

func runAlbumIndexBenchmark(b *testing.B, conn *sql.DB, albums int, indexed bool) {
	var version string
	if err := conn.QueryRow("SELECT sqlite_version()").Scan(&version); err != nil {
		b.Fatal(err)
	}
	b.Logf("SQLite %s", version)
	checkAlbumIndexPlan(b, conn, indexed)
	for b.Loop() {
		b.StopTimer()
		if _, err := conn.Exec("UPDATE albums SET total_tracks = 0"); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if _, err := conn.Exec(auditAlbumAggregateUpdate); err != nil {
			b.Fatal(err)
		}
	}
	checkAlbumIndexCounters(b, conn, albums)
}

func seedAlbumIndexFixture(t testing.TB, albums int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	conn := openAlbumIndexFixture(t, path)
	if _, err := conn.Exec(schemaV1 + "\nPRAGMA user_version = 1;"); err != nil {
		t.Fatal(err)
	}
	// Match the audit seed. tracks.track_total defaults to 1.
	if _, err := conn.Exec("INSERT INTO artists(name,clean_name) VALUES('Owner','owner'),('Guest','guest')"); err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct {
		query string
		count int
	}{
		{`WITH RECURSIVE s(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM s WHERE i<?)
			INSERT INTO albums(artist_id,title,clean_title,folder_path)
			SELECT 1, CASE i WHEN 1 THEN 'Release' ELSE 'A'||i END,
				CASE i WHEN 1 THEN 'release' ELSE 'a'||i END,
				CASE i WHEN 1 THEN 'Release' ELSE 'p'||i END FROM s`, albums},
		{`WITH RECURSIVE s(i) AS (VALUES(0) UNION ALL SELECT i+1 FROM s WHERE i<?)
			INSERT INTO tracks(album_id,artist_id,title,clean_title,file_size,mtime,file_path)
			SELECT a.id,1,'T','t',1,1,a.folder_path||'/f'||i||'.mp3'
			FROM s JOIN albums a ON a.id = i/10+1`, 10*albums - 1},
	} {
		if _, err := conn.Exec(seed.query, seed.count); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func openAlbumIndexFixture(t testing.TB, path string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func checkAlbumIndexPlan(t testing.TB, conn *sql.DB, indexed bool) {
	t.Helper()
	rows, err := conn.Query("EXPLAIN QUERY PLAN " + auditAlbumAggregateUpdate)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := "SCAN tracks"
	if indexed {
		want = "SEARCH tracks USING INDEX idx_tracks_album (album_id=?)"
	}
	var found bool
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Log(detail)
		found = found || strings.Contains(detail, want)
		if indexed && strings.Contains(detail, "SCAN tracks") {
			t.Fatalf("track scan after applying index: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("expected %q in query plan", want)
	}
}

func checkAlbumIndexCounters(t testing.TB, conn *sql.DB, albums int) {
	t.Helper()
	var matching, total int
	if err := conn.QueryRow(`
		SELECT COUNT(*) FILTER (WHERE present_tracks_count = 10 AND total_tracks = 1 AND is_complete), COUNT(*)
		FROM albums
	`).Scan(&matching, &total); err != nil {
		t.Fatal(err)
	}
	if total != albums || matching != albums {
		t.Fatalf("albums = %d, with expected counters = %d, want both %d", total, matching, albums)
	}
}
