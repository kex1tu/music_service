package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestTrackSourcesMigrationV7(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "v6.db")
	old, err := openReleaseMigrationV6(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	mustReleaseSQL(t, old.conn, `
		INSERT INTO artists (id, name, clean_name, deezer_id) VALUES (101, 'Twin', 'twin', '111');
		INSERT INTO albums (id, artist_id, title, clean_title, folder_path) VALUES (21, 101, 'Album', 'album', 'Album');
		INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, mtime, pinned_source_url)
			VALUES (31, 21, 101, 'Song', 'song', 'Album/Song.mp3', 1, 0, 'https://youtu.be/old');
		INSERT INTO pinned_track_sources (cache_key, artist_name, track_title, pinned_url, source_type)
			VALUES ('pin', 'Twin', 'Song', 'https://youtu.be/old', 'manual');
		INSERT INTO soundcloud_uncensored_cache
			(cache_key, track_title, artist_name, selected_url, status, duration, is_manual)
			VALUES ('query', 'Song', 'Twin', 'search-url', 'likely', 180, 0),
			       ('manual', 'Song', 'Twin', 'manual-url', 'manual', 180, 1);
		UPDATE sqlite_sequence SET seq = 200 WHERE name = 'artists';
	`)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mustReleaseSQL(t, database.conn, `INSERT INTO artists(name, clean_name, deezer_id) VALUES ('Twin', 'twin', '222')`)
	for query, want := range map[string]int{
		"PRAGMA user_version":                                                                  len(migrations),
		"SELECT COUNT(*) FROM pragma_foreign_key_check":                                        0,
		"SELECT COUNT(*) FROM artists WHERE name = 'Twin'":                                     2,
		"SELECT COUNT(*) FROM artists WHERE name = 'Twin' AND id > 200":                        1,
		"SELECT COUNT(*) FROM tracks WHERE id = 31 AND artist_id = 101 AND source_url IS NULL": 1,
		"SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('idx_tracks_path', 'pinned_track_sources', 'missing_tracks', 'soundcloud_uncensored_cache', 'idx_sc_cache_key')": 0,
	} {
		if got := libraryRowCount(t, database.conn, query); got != want {
			t.Fatalf("%s = %d, want %d", query, got, want)
		}
	}
	_, err = database.conn.ExecContext(ctx, `INSERT INTO tracks(album_id, artist_id, title, clean_title, file_path, file_size, mtime)
		VALUES (21, 101, 'other', 'other', 'Album/Song.mp3', 1, 0)`)
	if err == nil {
		t.Fatal("duplicate file path was accepted")
	}
	copies, err := filepath.Glob(path + ".pre-v7-*.sqlite")
	if err != nil || len(copies) != 1 {
		t.Fatalf("backup copies=%v, error=%v", copies, err)
	}
	backup, err := sql.Open("sqlite", copies[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if got := libraryRowCount(t, backup, "SELECT COUNT(*) FROM pinned_track_sources WHERE cache_key = 'pin'"); got != 1 {
		t.Fatalf("legacy source was lost from backup")
	}
}

func TestCatalogHomonymsAndFallback(t *testing.T) {
	ctx := t.Context()
	database, err := Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mustReleaseSQL(t, database.conn, `
		INSERT INTO artists(id, name, clean_name, deezer_id) VALUES (1,'Twin','twin','111');
		INSERT INTO albums(id, artist_id, title, clean_title, folder_path) VALUES (1,1,'Release','release','Release');
		INSERT INTO tracks(album_id, artist_id, title, clean_title, track_number, disc_number, file_path, file_size, mtime)
			VALUES (1,1,'Same','same',1,1,'Release/CD1/01.mp3',1,0),
			       (1,1,'Same','same',1,2,'Release/CD2/01.mp3',1,0),
			       (1,1,'Same','same',1,2,'Release/CD2/duplicate.mp3',1,0);
	`)
	catalog := []CatalogTrack{
		{BackfillSelection: BackfillSelection{TrackPosition: ReleasePosition(1, 1)}, Title: "Same"},
		{BackfillSelection: BackfillSelection{TrackPosition: ReleasePosition(2, 1)}, Title: "Same", Artist: "Twin", DeezerArtistID: "222"},
	}
	present, err := VerifyCatalog(ctx, database.conn, 1, "77", catalog)
	if err != nil || len(present) != 1 || !present[ReleasePosition(1, 1)] {
		t.Fatalf("wrong present slots: %v, %v", present, err)
	}
	positions, err := ReleaseTracklist(ctx, database.conn, 1, false)
	if err != nil || len(positions) != 2 || positions[0].ArtistID != 1 || positions[1].ArtistID == 1 {
		t.Fatalf("wrong artist links: %v, %v", positions, err)
	}
	catalog[1].DeezerArtistID = ""
	catalog[1].Title = "Ignored"
	if _, err := VerifyCatalog(ctx, database.conn, 1, "77", catalog); !errors.Is(err, ErrAmbiguousArtist) {
		t.Fatalf("want ambiguity, got %v", err)
	}
	if got := libraryRowCount(t, database.conn, "SELECT COUNT(*) FROM release_tracklist WHERE title = 'Same'"); got != 2 {
		t.Fatalf("failed verification replaced snapshot: %d positions left", got)
	}
	if _, err := DeleteLibraryTracks(ctx, database.conn, "Release/CD1/01.mp3", false); err != nil {
		t.Fatal(err)
	}
	if got := libraryRowCount(t, database.conn, "SELECT COUNT(*) FROM release_tracklist WHERE is_present = 0"); got != 2 {
		t.Fatalf("expected 2 missing positions after deletion, got %d", got)
	}
}
