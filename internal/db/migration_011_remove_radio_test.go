package db

import (
	"path/filepath"
	"testing"
)

func TestMigrationRemoveRadio(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "library.db")
	old, err := openTestSchemaVersion(ctx, path, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	mustReleaseSQL(t, old.Conn(), `
		INSERT INTO artists (id, name, clean_name) VALUES (1, 'Local', 'local'), (2, 'Related', 'related');
		INSERT INTO albums (id, artist_id, title, clean_title, folder_path)
		VALUES (11, 1, 'Album', 'album', 'Local/Album');
		INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, mtime)
		VALUES (31, 11, 1, 'Track', 'track', 'Local/Album/Track.mp3', 1, 0);
		INSERT INTO radio_history (track_id, station_type, station_param, station_artist_id)
		VALUES (31, 'artist', 'Local', 1);
		INSERT INTO artist_relations (artist_name, related_name, score, source, artist_id, related_artist_id)
		VALUES ('local', 'related', 0.9, 'legacy', 1, 2);
		INSERT INTO tracked_artists (id, name, clean_name) VALUES (21, 'Future', 'future');
		INSERT INTO radar_releases (id, artist_name, clean_artist_name, title, clean_title, release_type, source_url, status)
		VALUES (41, 'Future', 'future', 'Release', 'release', 'single', 'catalog', 'ignored');
	`)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	conn := database.Conn()
	mustReleaseSQL(t, conn, "INSERT INTO artists (id, name, clean_name) VALUES (3, 'Future', 'future')")
	for _, check := range []struct {
		query string
		want  int
	}{
		{"PRAGMA user_version", len(migrations)},
		{"SELECT count(*) FROM sqlite_schema WHERE name IN ('radio_history', 'artist_relations')", 0},
		{"SELECT count(*) FROM tracks WHERE id = 31 AND artist_id = 1", 1},
		{"SELECT count(*) FROM tracked_artists WHERE id = 21 AND artist_id = 3", 1},
		{"SELECT count(*) FROM radar_release_artists WHERE release_id = 41 AND artist_id = 3", 1},
		{"SELECT count(*) FROM radar_releases WHERE id = 41 AND status = 'ignored'", 1},
		{"SELECT count(*) FROM pragma_foreign_key_check", 0},
	} {
		if got := libraryRowCount(t, conn, check.query); got != check.want {
			t.Errorf("%s = %d, want %d", check.query, got, check.want)
		}
	}
}
