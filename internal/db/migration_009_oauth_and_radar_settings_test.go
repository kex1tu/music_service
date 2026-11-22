package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOAuthAndRadarSettingsMigration(t *testing.T) {
	for _, tc := range []struct {
		name       string
		canonical  string
		wantAccess string
		wantUser   string
	}{
		{"legacy session replaces partial canonical data", "", "legacy-access", "legacy-user"},
		{"canonical session takes precedence as a whole", "current-access", "current-access", "current-user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "library.db")
			old, err := openTestSchemaVersion(ctx, path, 8)
			if err != nil {
				t.Fatal(err)
			}
			mustReleaseSQL(t, old.Conn(), `
				INSERT INTO app_settings(key, value) VALUES
				  ('musicbrainz_access_token','legacy-access'), ('musicbrainz_username','legacy-user'),
				  ('mb_username','current-user'), ('theme','dark');
				INSERT INTO artists(id, name, clean_name) VALUES (17,'Artist','artist'), (18,'Guest','guest');
				INSERT INTO albums(id, artist_id, title, clean_title, folder_path) VALUES (19,17,'Album','album','Album');
				INSERT INTO album_artists(album_id, artist_id, position) VALUES (19,18,1);
				INSERT INTO tracked_artists(artist_id, name, clean_name, custom_search_alias, is_followed, auto_download)
				  VALUES (17,'Artist','artist','Alias',0,1);
			`)
			if tc.canonical != "" {
				if _, err := old.Conn().ExecContext(ctx, "INSERT INTO app_settings(key, value) VALUES ('mb_access_token', ?)", tc.canonical); err != nil {
					t.Fatal(err)
				}
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}

			for range 2 {
				migrated, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				checkMigratedOAuthAndRadarSettings(t, ctx, migrated, tc.wantAccess, tc.wantUser)
				if err := migrated.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func checkMigratedOAuthAndRadarSettings(t *testing.T, ctx context.Context, db *DB, wantAccess, wantUser string) {
	t.Helper()
	for key, want := range map[string]string{"mb_access_token": wantAccess, "mb_username": wantUser, "theme": "dark"} {
		var got string
		if err := db.Conn().QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key = ?", key).Scan(&got); err != nil || got != want {
			t.Fatalf("unexpected value for %s: %q, %v", key, got, err)
		}
	}
	var count int
	if err := db.Conn().QueryRowContext(ctx, "SELECT count(*) FROM app_settings WHERE key GLOB 'musicbrainz_*'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy OAuth keys: %d, %v", count, err)
	}
	if err := db.Conn().QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key = 'mb_refresh_token'").Scan(new(string)); err != sql.ErrNoRows {
		t.Fatalf("unexpected refresh token for a partial session: %v", err)
	}
	artists, err := db.GetRadarLibraryArtists(ctx)
	if err != nil || len(artists) != 2 || artists[0].CustomSearchAlias != "Alias" || artists[1].ArtistID != 18 {
		t.Fatalf("library settings after migration: %+v, %v", artists, err)
	}
}
