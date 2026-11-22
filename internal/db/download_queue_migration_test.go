package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestDownloadQueueMigrationV8(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "v7.db")
	old, err := openReleaseMigrationV6(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := old.Conn().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateTrackSources(ctx, conn); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	mustReleaseSQL(t, old.Conn(), `
		PRAGMA user_version = 7;
		INSERT INTO download_jobs(id,status,source,url,artist_name) VALUES
			('legacy','QUEUED','deezer','release','New Artist'), ('plain','READY','deezer','release','New Artist'), ('manual','WAITING_SOURCE','deezer','release','New Artist'),
			('redo','READY','release_picker','','New Artist');
		INSERT INTO download_source_selections(job_id,payload_json) VALUES
			('plain','{"tracks":[{"converted_path":"plain/converted/song.mp3","status":"ready"}]}'),
			('manual','{"tracks":[{"audio_source_url":"https://soundcloud.com/a/b","version_status":"manual"}]}'),
			('redo','{"redownload_targets":[{"original_path":"a.mp3","confirmed":true}]}');
		INSERT INTO soundcloud_uncensored_cache(cache_key,track_title,artist_name,status,duration)
			VALUES ('cache','Song','New Artist','likely',180);
	`)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	for attempt := range 2 {
		database, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 0 {
			mustReleaseSQL(t, database.Conn(), "INSERT INTO artists(name,clean_name) VALUES ('New Artist','new artist')")
		}
		for query, want := range map[string]int{
			"PRAGMA user_version":                           len(migrations),
			"SELECT COUNT(*) FROM pragma_foreign_key_check": 0,
			"SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('download_source_selections','soundcloud_uncensored_cache','idx_sc_cache_key')": 0,
			"SELECT COUNT(*) FROM download_jobs WHERE artist_id IS NULL":                                                                       4,
			"SELECT COUNT(*) FROM download_jobs WHERE id='legacy' AND selection_json IS NULL":                                                  1,
		} {
			if got := libraryRowCount(t, database.Conn(), query); got != want {
				t.Fatalf("%s: %d, want %d", query, got, want)
			}
		}
		for id, want := range map[string]string{
			"plain":  `{"tracks":[{"converted_path":"plain/converted/song.mp3","status":"ready"}]}`,
			"manual": `{"tracks":[{"audio_source_url":"https://soundcloud.com/a/b","version_status":"manual"}]}`,
			"redo":   `{"redownload_targets":[{"original_path":"a.mp3","confirmed":true}]}`,
		} {
			var raw sql.NullString
			err := database.Conn().QueryRowContext(ctx, "SELECT selection_json FROM download_jobs WHERE id=?", id).Scan(&raw)
			if err != nil || !raw.Valid || raw.String != want {
				t.Fatalf("%s payload: %+v, %v", id, raw, err)
			}
		}
		database.Close()
	}
}
