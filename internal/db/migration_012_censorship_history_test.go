package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrationCensorshipHistory(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "library.db")
	old, err := openTestSchemaVersion(ctx, path, 11)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	mustReleaseSQL(t, old.Conn(), `
		INSERT INTO censorship_runs(id,options_json) VALUES('run','{}');
		INSERT INTO censorship_items(id,run_id,file_path,reference_path,state,result_json,manual_decision,manual_note)
		VALUES (1,'run','track.mp3','ref.mp3','done','{"audio_hash":"hash","evidence":[{"kind":"beep"}]}','uncertain','old note'),
		       (2,'run','other.mp3','','done','{"evidence":[]}','cleared','keep this');
		INSERT INTO censorship_evidence(item_id,evidence_json)
		VALUES (1,'{"kind":"beep"}'),(2,'{"kind":"stale"}'),(2,'{"kind":"stale"}');
		INSERT INTO censorship_cache(cache_key,result_json) VALUES('cached','{}');
		INSERT INTO censorship_transcripts(cache_key,transcript_json) VALUES('transcript','{}');
		INSERT INTO censorship_cloud_usage(provider,day,seconds) VALUES('gemini','2026-11-21',42);
		CREATE TRIGGER refuse_censorship_migration BEFORE UPDATE ON censorship_items
		BEGIN SELECT RAISE(ABORT, 'test rollback'); END;
	`)
	if database, err := Open(path); err == nil {
		database.Close()
		t.Fatal("expected migration failure")
	}
	if version := libraryRowCount(t, old.Conn(), "PRAGMA user_version"); version != 11 {
		t.Fatalf("rollback version = %d", version)
	}
	if count := libraryRowCount(t, old.Conn(), "SELECT count(*) FROM pragma_table_info('censorship_items') WHERE name='audio_hash'"); count != 0 {
		t.Fatal("failed migration left a new column")
	}
	mustReleaseSQL(t, old.Conn(), "DROP TRIGGER refuse_censorship_migration")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	conn := database.Conn()
	for _, check := range []struct {
		query string
		want  int
	}{
		{"PRAGMA user_version", len(migrations)},
		{"SELECT count(*) FROM sqlite_schema WHERE name='censorship_evidence'", 0},
		{"SELECT count(*) FROM censorship_evidence_archive", 1},
		{"SELECT sum(json_array_length(evidence_rows_json)) FROM censorship_evidence_archive WHERE item_id=2", 2},
		{"SELECT count(*) FROM censorship_items WHERE manual_note!='' AND manual_audio_hash='' AND decided_at IS NULL", 2},
		{"SELECT count(*) FROM censorship_items WHERE id=1 AND audio_hash='hash' AND current_file_path='track.mp3' AND current_reference_path='ref.mp3'", 1},
		{"SELECT count(*) FROM censorship_cloud_usage WHERE seconds=42", 1},
		{"SELECT count(*) FROM censorship_transcripts", 1},
		{"SELECT count(*) FROM censorship_cache WHERE last_used_at>0", 1},
		{"SELECT count(*) FROM pragma_foreign_key_check", 0},
	} {
		if n := libraryRowCount(t, conn, check.query); n != check.want {
			t.Errorf("%s = %d, want %d", check.query, n, check.want)
		}
	}
}

func TestCensorshipPathsFollowLibraryMove(t *testing.T) {
	ctx := t.Context()
	database, err := Open(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	conn := database.Conn()
	mustReleaseSQL(t, conn, `
		INSERT INTO censorship_runs(id,options_json) VALUES('run','{}');
		INSERT INTO censorship_items(run_id,file_path,reference_path)
			VALUES ('run','song.mp3','ref.mp3'),('run','ref.mp3','song.mp3');
	`)
	move := func(from, to string, merged bool) {
		t.Helper()
		tx, err := conn.BeginTx(ctx, &sql.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, _, err = PrepareLibraryTrackUpdate(ctx, tx, from, to, merged); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	move("song.mp3", "renamed.mp3", false)
	move("ref.mp3", "renamed reference.mp3", false)
	move("renamed.mp3", "another audio.mp3", true)
	if n := libraryRowCount(t, conn, `SELECT count(*) FROM censorship_items
		WHERE (file_path='song.mp3' AND reference_path='ref.mp3' AND current_file_path='renamed.mp3' AND current_reference_path='renamed reference.mp3')
		   OR (file_path='ref.mp3' AND reference_path='song.mp3' AND current_file_path='renamed reference.mp3' AND current_reference_path='renamed.mp3')`); n != 2 {
		t.Fatalf("recorded moves = %d, want 2", n)
	}
}
