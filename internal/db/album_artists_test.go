package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func assertAlbumCredits(t *testing.T, conn *sql.DB, albumID int64, want string) {
	t.Helper()
	var got string
	err := conn.QueryRow(`SELECT COALESCE(group_concat(name, ' / '), '') FROM (
		SELECT ar.name FROM album_artists aa JOIN artists ar ON ar.id = aa.artist_id
		WHERE aa.album_id = ? ORDER BY aa.position)`, albumID).Scan(&got)
	if err != nil || got != want {
		t.Fatalf("credits for album %d = %q, want %q: %v", albumID, got, want, err)
	}
}

func TestAlbumArtistMigrationFromV5(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "v5.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { old.Close() })
	conn, err := old.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, migrate := range migrations[:5] {
		if err := migrate(ctx, conn); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.ExecContext(ctx, `
		PRAGMA user_version = 5;
		INSERT INTO artists(id, name, clean_name) VALUES (1, 'AC/DC', 'ac/dc'), (2, 'Various Artists', 'various artists');
		INSERT INTO albums(id, artist_id, title, clean_title, folder_path, album_artist) VALUES
			(11, 1, 'Together', 'together', 'Together', 'AC/DC\\Earth, Wind & Fire\\AC/DC'),
			(12, 2, 'Collection', 'collection', 'Collection', 'Various Artists'),
			(13, 1, 'Untagged', 'untagged', 'Untagged', NULL),
			(14, 1, 'Unresolved', 'unresolved', 'Unresolved', 'AC/DC feat. Unknown Band');
	`); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	assertAlbumCredits(t, database.Conn(), 11, "AC/DC / Earth, Wind & Fire")
	assertAlbumCredits(t, database.Conn(), 12, "")
	assertAlbumCredits(t, database.Conn(), 13, "")
	assertAlbumCredits(t, database.Conn(), 14, "")
	if got := libraryRowCount(t, database.Conn(), `SELECT COUNT(*) FROM album_artists WHERE album_id = 11 AND artist_id = 1 AND position = 1`); got != 1 {
		t.Fatalf("primary credit did not retain artist ID 1 and position 1")
	}
	if got := libraryRowCount(t, database.Conn(), `SELECT COUNT(*) FROM pragma_foreign_key_check`); got != 0 {
		t.Fatalf("migration left %d invalid foreign keys", got)
	}
}

func TestAlbumArtistSearchAndRadar(t *testing.T) {
	ctx := t.Context()
	database, err := Open(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	conn := database.Conn()
	mustReleaseSQL(t, conn, `INSERT INTO artists(id, name, clean_name) VALUES
		(1, 'AC/DC', 'ac/dc'), (2, 'AC', 'ac'), (3, 'Various Artists', 'various artists')`)
	albumID, err := UpsertLocalAlbum(ctx, conn, "Together", 1, "AC/DC\x00Гость", 2026, "Together", false)
	if err != nil {
		t.Fatal(err)
	}
	assertAlbumCredits(t, conn, albumID, "AC/DC / Гость")
	compilationID, err := UpsertLocalAlbum(ctx, conn, "Collection", 3, "Various Artists", 2026, "Collection", false)
	if err != nil {
		t.Fatal(err)
	}
	assertAlbumCredits(t, conn, compilationID, "")
	_, err = conn.ExecContext(ctx, `INSERT INTO tracks (album_id, artist_id, title, clean_title, file_path, file_size, mtime)
		VALUES (?1, 1, 'Duet', 'duet', 'Together/01.mp3', 1, 1),
		       (?2, (SELECT id FROM artists WHERE name = 'Гость'), 'Guest Solo', 'guest solo', 'Collection/01.mp3', 1, 1)`, albumID, compilationID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := database.SearchLibrary(ctx, "ГОСТЬ", 10)
	if err != nil || len(res.Albums) != 1 || res.Albums[0].ID != albumID || len(res.Tracks) != 1 || res.Tracks[0].Title != "Guest Solo" {
		t.Fatalf("album participant or compilation track missing from search: %+v, %v", res, err)
	}
	track := CatalogTrack{BackfillSelection: BackfillSelection{TrackPosition: ReleasePosition(1, 1)}, Title: "Duet"}
	for _, tc := range []struct {
		artist, kind, title string
		tracklist           []CatalogTrack
		want                bool
	}{
		{artist: "Гость", title: "Together", kind: "album", tracklist: []CatalogTrack{track}, want: true},
		{artist: "Гость", title: "Together", kind: "album"},
		{artist: "AC", title: "Together", kind: "album", tracklist: []CatalogTrack{track}},
		{artist: "Гость", title: "Guest Solo", kind: "single", want: true},
	} {
		release := RadarReleaseEntity{ArtistName: tc.artist, Title: tc.title, ReleaseType: tc.kind, Tracklist: tc.tracklist}
		got, err := database.RadarReleaseInLibrary(ctx, release)
		if err != nil || got != tc.want {
			t.Fatalf("radar for %q / %q with %d catalog tracks = %v, want %v: %v", tc.artist, tc.title, len(tc.tracklist), got, tc.want, err)
		}
	}
	stored := RadarReleaseEntity{ArtistName: "Гость", Title: "Together", ReleaseType: "album", Tracklist: []CatalogTrack{track}}
	if err := database.UpsertRadarRelease(ctx, &stored); err != nil {
		t.Fatal(err)
	}
	mustReleaseSQL(t, conn, `UPDATE artists SET name = 'Новое имя', clean_name = 'новое имя' WHERE name = 'Гость'`)
	if got, err := database.RadarReleaseInLibrary(ctx, stored); err != nil || !got {
		t.Fatalf("saved radar artist lost its ID after rename: %v, %v", got, err)
	}
	const retag = "Новое имя\\AC/DC\\Third Artist"
	id, err := UpsertLocalAlbum(ctx, conn, "Together", 1, retag, 2026, "Together", false)
	if err != nil || id != albumID {
		t.Fatalf("retag changed album identity: %d, %v", id, err)
	}
	assertAlbumCredits(t, conn, albumID, "Новое имя / AC/DC / Third Artist")
	mustReleaseSQL(t, conn, `CREATE TRIGGER reject_credits BEFORE INSERT ON album_artists BEGIN SELECT RAISE(ABORT, 'reject credits'); END`)
	if _, err := UpsertLocalAlbum(ctx, conn, "Failing retag", 1, "AC/DC", 2026, "Together", false); err == nil {
		t.Fatal("expected injected failure during credit replacement")
	}
	assertAlbumCredits(t, conn, albumID, "Новое имя / AC/DC / Third Artist")
	if got := libraryRowCount(t, conn, `SELECT COUNT(*) FROM albums WHERE id = ? AND title = 'Together' AND album_artist = ?`, albumID, retag); got != 1 {
		t.Fatal("failed credit update changed album metadata")
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM albums WHERE id = ?`, albumID); err != nil {
		t.Fatal(err)
	}
	assertAlbumCredits(t, conn, albumID, "")
}
