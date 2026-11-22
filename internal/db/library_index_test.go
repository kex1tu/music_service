package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func libraryDeletionTestDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.Conn(), context.Background()
}

func libraryRowCount(t *testing.T, conn *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := conn.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("query %q failed: %v", query, err)
	}
	return count
}

func TestDeleteLibraryTracks_CompilationGuestTrack(t *testing.T) {
	conn, ctx := libraryDeletionTestDB(t)

	_, err := conn.ExecContext(ctx, `
		INSERT INTO artists (id, name, clean_name) VALUES
		(10, 'Various Artists', 'various artists'),
		(20, 'Guest One', 'guest one'),
		(30, 'Guest Two', 'guest two');

		INSERT INTO albums (id, artist_id, title, clean_title, folder_path, present_tracks_count, total_tracks, is_complete)
		VALUES (101, 10, 'Compilation', 'compilation', '/music/comp', 2, 2, 1);

		INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, mtime) VALUES
		(101, 101, 20, 'Guest Track 1', 'guest track 1', '/music/comp/01.mp3', 1024, 100),
		(102, 101, 30, 'Guest Track 2', 'guest track 2', '/music/comp/02.mp3', 2048, 100);

		INSERT INTO genres (id, name) VALUES (501, 'Rock'), (502, 'Electronic');
		INSERT INTO track_genres (track_id, genre_id) VALUES (101, 501), (102, 502);


		-- Typed issue for track 101 and numerically overlapping album issue for album 101
		INSERT INTO audit_issues (issue_type, entity_type, entity_id, file_path) VALUES
		('missing_tag', 'track', 101, '/music/comp/01.mp3'),
		('missing_art', 'album', 101, '/music/comp/01.mp3');
	`)
	if err != nil {
		t.Fatalf("failed to insert fixtures: %v", err)
	}

	deleted, err := DeleteLibraryTracks(ctx, conn, "/music/comp/01.mp3", false)
	if err != nil {
		t.Fatalf("DeleteLibraryTracks failed: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted track, got %d", deleted)
	}

	// Deleted track is removed
	if c := libraryRowCount(t, conn, `SELECT COUNT(*) FROM tracks WHERE id = ?`, 101); c != 0 {
		t.Fatalf("expected deleted track to be removed, found %d", c)
	}

	// Guest track 2 and its genre associations are preserved
	if c := libraryRowCount(t, conn, `SELECT COUNT(*) FROM tracks WHERE id = ?`, 102); c != 1 {
		t.Fatalf("expected guest track 2 to be preserved, found %d", c)
	}
	if c := libraryRowCount(t, conn, `SELECT COUNT(*) FROM track_genres WHERE track_id = ? AND genre_id = ?`, 102, 502); c != 1 {
		t.Fatalf("expected guest track 2 genre mapping to be preserved, found %d", c)
	}
	if c := libraryRowCount(t, conn, `SELECT COUNT(*) FROM genres WHERE id = ?`, 502); c != 1 {
		t.Fatalf("expected genre 2 to be preserved, found %d", c)
	}

	if c := libraryRowCount(t, conn, "SELECT COUNT(*) FROM artists WHERE id = ?", 20); c != 0 {
		t.Fatalf("orphan guest artist count = %d", c)
	}

	// Album counts refreshed
	var presentTracks, totalTracks, isComplete int
	err = conn.QueryRowContext(ctx, `SELECT present_tracks_count, total_tracks, is_complete FROM albums WHERE id = ?`, 101).
		Scan(&presentTracks, &totalTracks, &isComplete)
	if err != nil {
		t.Fatalf("failed to query album: %v", err)
	}
	if presentTracks != 1 || totalTracks != 1 || isComplete != 0 {
		t.Fatalf("expected album counts (present=1, total=1, is_complete=0), got (present=%d, total=%d, is_complete=%d)",
			presentTracks, totalTracks, isComplete)
	}

	// Deletes typed issue for track, preserves numerically overlapping album issue
	if c := libraryRowCount(t, conn, `SELECT COUNT(*) FROM audit_issues WHERE entity_type = 'track' AND entity_id = ?`, 101); c != 0 {
		t.Fatalf("expected track audit issue to be deleted, found %d", c)
	}
	if c := libraryRowCount(t, conn, `SELECT COUNT(*) FROM audit_issues WHERE entity_type = 'album' AND entity_id = ?`, 101); c != 1 {
		t.Fatalf("expected album audit issue with overlapping ID to be preserved, found %d", c)
	}
}

func TestDeleteLibraryTracks_DirectoryBoundaryAndWildcards(t *testing.T) {
	conn, ctx := libraryDeletionTestDB(t)

	_, err := conn.ExecContext(ctx, `
		INSERT INTO artists (id, name, clean_name) VALUES (1, 'Artist', 'artist');
		INSERT INTO albums (id, artist_id, title, clean_title, folder_path, present_tracks_count, total_tracks, is_complete)
		VALUES (1, 1, 'Album', 'album', 'root', 7, 7, 1);

		INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, mtime) VALUES
		(1, 1, 1, 'Target 1', 'target 1', 'A_B/track1.mp3', 100, 100),
		(2, 1, 1, 'Target 2', 'target 2', 'A_B/sub/track2.mp3', 100, 100),
		(3, 1, 1, 'Wildcard Underscore', 'wildcard underscore', 'AxB/track.mp3', 100, 100),
		(4, 1, 1, 'Case Mismatch', 'case mismatch', 'a_b/track.mp3', 100, 100),
		(5, 1, 1, 'Prefix Extension', 'prefix extension', 'A_BX/track.mp3', 100, 100),
		(6, 1, 1, 'Percent Literal', 'percent literal', 'A%B/track.mp3', 100, 100),
		(7, 1, 1, 'Percent Root', 'percent root', '%literal/track.mp3', 100, 100);
	`)
	if err != nil {
		t.Fatalf("failed to insert path fixtures: %v", err)
	}

	deleted, err := DeleteLibraryTracks(ctx, conn, "A_B", true)
	if err != nil {
		t.Fatalf("DeleteLibraryTracks directory delete failed: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("expected exactly 2 tracks deleted under A_B, got %d", deleted)
	}

	retainedIDs := []int{3, 4, 5, 6, 7}
	for _, id := range retainedIDs {
		if c := libraryRowCount(t, conn, `SELECT COUNT(*) FROM tracks WHERE id = ?`, id); c != 1 {
			t.Errorf("expected track id %d to be retained, found %d", id, c)
		}
	}
	deleted, err = DeleteLibraryTracks(ctx, conn, "A%B", true)
	if err != nil || deleted != 1 {
		t.Fatalf("literal %% directory deletion: count=%d err=%v", deleted, err)
	}
	if c := libraryRowCount(t, conn, "SELECT COUNT(*) FROM tracks WHERE id IN (3, 4, 5, 7)"); c != 4 {
		t.Fatalf("literal %% directory affected a sibling")
	}

}

func TestDeleteLibraryTracks_RollbackOnAlbumUpdate(t *testing.T) {
	conn, ctx := libraryDeletionTestDB(t)

	_, err := conn.ExecContext(ctx, `
		INSERT INTO artists (id, name, clean_name) VALUES (1, 'Artist', 'artist');
		INSERT INTO albums (id, artist_id, title, clean_title, folder_path, present_tracks_count, total_tracks, is_complete)
		VALUES (1, 1, 'Album', 'album', '/music/album', 2, 2, 1);
		INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, mtime)
		VALUES (1, 1, 1, 'Track 1', 'track 1', '/music/album/track.mp3', 100, 100),
		(2, 1, 1, 'Track 2', 'track 2', '/music/album/other.mp3', 100, 100);
		INSERT INTO genres (id, name) VALUES (1, 'Rock');
		INSERT INTO track_genres (track_id, genre_id) VALUES (1, 1);
		INSERT INTO audit_issues (issue_type, entity_type, entity_id, file_path)
		VALUES ('test_issue', 'track', 1, '/music/album/track.mp3');

		CREATE TRIGGER abort_album_update
		BEFORE UPDATE ON albums
		BEGIN
			SELECT RAISE(ABORT, 'aborted album update by trigger');
		END;
	`)
	if err != nil {
		t.Fatalf("failed to setup rollback fixture: %v", err)
	}

	_, err = DeleteLibraryTracks(ctx, conn, "/music/album/track.mp3", false)
	if err == nil {
		t.Fatal("expected DeleteLibraryTracks to fail due to trigger RAISE(ABORT), got nil")
	}

	// Verify complete rollback of all tables
	if c := libraryRowCount(t, conn, `SELECT COUNT(*) FROM tracks WHERE id = 1`); c != 1 {
		t.Fatalf("expected track to remain after rollback, found %d", c)
	}
	if c := libraryRowCount(t, conn, `SELECT COUNT(*) FROM track_genres WHERE track_id = 1`); c != 1 {
		t.Fatalf("expected track_genres to remain after rollback, found %d", c)
	}
	if c := libraryRowCount(t, conn, `SELECT COUNT(*) FROM audit_issues WHERE entity_id = 1 AND entity_type = 'track'`); c != 1 {
		t.Fatalf("expected audit issue to remain after rollback, found %d", c)
	}
	if c := libraryRowCount(t, conn, `SELECT present_tracks_count FROM albums WHERE id = 1`); c != 2 {
		t.Fatalf("expected album present_tracks_count to remain 2 after rollback, found %d", c)
	}
}
