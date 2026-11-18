package scanner

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
)

func newIndexFixture(t *testing.T) (*Scanner, string) {
	t.Helper()
	dir := t.TempDir()
	musicDir := filepath.Join(dir, "music")
	if err := os.Mkdir(musicDir, 0755); err != nil {
		t.Fatal(err)
	}
	sb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "music.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return NewScanner(sb, database, nil), dbPath
}

func writeIndexFixtureTrack(t *testing.T, s *Scanner, path, title, genre string) string {
	t.Helper()
	full, err := s.musicSandbox.Resolve(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(full); os.IsNotExist(err) {
		if err := os.WriteFile(full, []byte("audio fixture"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := id3.WriteTags(full, id3.TrackTags{
		Title: title, Artist: "Artist", Album: "Album", Genre: genre, TrackNumber: 1,
	}, nil); err != nil {
		t.Fatal(err)
	}
	return full
}

func indexFixtureExec(t *testing.T, s *Scanner, query string, args ...any) {
	t.Helper()
	if _, err := s.database.Conn().Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func indexFixtureInt(t *testing.T, s *Scanner, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := s.database.Conn().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFullScanPreservesReindexedMoveAndUnreadSubtree(t *testing.T) {
	s, _ := newIndexFixture(t)
	ctx := context.Background()
	oldPath := "A/song.mp3"
	targetPath := "A/new.mp3"
	old := writeIndexFixtureTrack(t, s, oldPath, "Song", "Rock")
	target, err := s.musicSandbox.Resolve(targetPath, false)
	if err != nil {
		t.Fatal(err)
	}
	unreadPath := "Z/stale.mp3"
	unread := writeIndexFixtureTrack(t, s, unreadPath, "Unread", "Jazz")
	if n, err := s.ScanAll(ctx); err != nil || n != 2 {
		t.Fatalf("initial scan: count=%d err=%v", n, err)
	}
	oldID := indexFixtureInt(t, s, "SELECT id FROM tracks WHERE file_path = ?", oldPath)
	if err := os.Remove(unread); err != nil {
		t.Fatal(err)
	}

	// Inject a read failure even if the tests run as root. A failed subtree
	// must be protected independently of the per-file existence check.
	walk := func(root string, visit fs.WalkDirFunc) error {
		return safefs.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if filepath.Base(path) == "Z" {
				if err := visit(path, entry, fs.ErrPermission); err != nil {
					return err
				}
				return filepath.SkipDir
			}
			if err := visit(path, entry, err); err != nil {
				return err
			}
			if path == old {
				// The directory listing has already been read and won't include the new filename.
				_, err := organizer.MoveIndexedTrack(ctx, s, old, target, false, nil)
				return err
			}
			return nil
		})
	}
	if _, err := s.scanAll(ctx, walk); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("expected a partial scan error, got %v", err)
	}
	if id := indexFixtureInt(t, s, "SELECT id FROM tracks WHERE file_path = ?", targetPath); id != oldID {
		t.Fatalf("move changed the track ID from %d to %d", oldID, id)
	}
	if n := indexFixtureInt(t, s, "SELECT COUNT(*) FROM tracks WHERE file_path = ?", unreadPath); n != 1 {
		t.Fatalf("unread subtree lost its track: count=%d", n)
	}
	if _, err := s.ScanAll(ctx); err != nil {
		t.Fatal(err)
	}
	if n := indexFixtureInt(t, s, "SELECT COUNT(*) FROM tracks WHERE file_path = ?", unreadPath); n != 0 {
		t.Fatalf("successful walk did not remove stale track: count=%d", n)
	}
}

func TestIndexRollsBackGenresIssuesAndAlbumOnError(t *testing.T) {
	s, _ := newIndexFixture(t)
	path := "Album/song.mp3"
	full := writeIndexFixtureTrack(t, s, path, "Original", "Rock")
	if err := s.IndexFile(full, path); err != nil {
		t.Fatal(err)
	}
	writeIndexFixtureTrack(t, s, path, "Edited", "Jazz")
	for _, trigger := range []struct{ operation, table string }{
		{"INSERT", "track_genres"},
		{"INSERT", "audit_issues"},
		{"UPDATE OF present_tracks_count", "albums"},
	} {
		indexFixtureExec(t, s, "CREATE TRIGGER force_failure BEFORE "+trigger.operation+" ON "+trigger.table+" BEGIN SELECT RAISE(ABORT, 'fixture failure'); END")
		if err := s.IndexFile(full, path); err == nil || !strings.Contains(err.Error(), "fixture failure") {
			t.Fatalf("%s: expected trigger error, got %v", trigger.table, err)
		}
		if n := indexFixtureInt(t, s, "SELECT COUNT(*) FROM tracks WHERE title = 'Original'"); n != 1 {
			t.Fatalf("%s: previous track row lost", trigger.table)
		}
		if n := indexFixtureInt(t, s, "SELECT COUNT(*) FROM track_genres JOIN genres ON genres.id = genre_id WHERE genres.name = 'Rock'"); n != 1 {
			t.Fatalf("%s: previous genre association lost", trigger.table)
		}
		if n := indexFixtureInt(t, s, "SELECT COUNT(*) FROM audit_issues WHERE issue_type = 'missing_cover'"); n != 1 {
			t.Fatalf("%s: previous audit issue lost", trigger.table)
		}
		indexFixtureExec(t, s, "DROP TRIGGER force_failure")
	}
	if err := s.IndexFile(full, path); err != nil {
		t.Fatal(err)
	}
	if n := indexFixtureInt(t, s, "SELECT COUNT(*) FROM track_genres JOIN genres ON genres.id = genre_id WHERE genres.name = 'Jazz'"); n != 1 {
		t.Fatalf("retry did not write the new genres: count=%d", n)
	}
}

func TestRestartRecoversPendingMoveAndRadioHistory(t *testing.T) {
	s, dbPath := newIndexFixture(t)
	ctx := context.Background()
	sourceName, targetName := "Album/song.mp3", "Album/renamed.mp3"
	source := writeIndexFixtureTrack(t, s, sourceName, "Song", "Rock")
	target, err := s.musicSandbox.Resolve(targetName, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.IndexFile(source, sourceName); err != nil {
		t.Fatal(err)
	}
	id := indexFixtureInt(t, s, "SELECT id FROM tracks WHERE file_path = ?", sourceName)
	if err := s.database.RecordRadioPlay(ctx, id, "wave", ""); err != nil {
		t.Fatal(err)
	}
	indexFixtureExec(t, s, "CREATE TRIGGER fail_move BEFORE UPDATE ON tracks BEGIN SELECT RAISE(ABORT, 'index unavailable'); END")
	moved, err := organizer.MoveIndexedTrack(ctx, s, source, target, false, nil)
	if moved != target || err == nil || !strings.Contains(err.Error(), "queued") {
		t.Fatalf("expected partial move to %q with queued reconciliation, got (%q, %v)", target, moved, err)
	}
	if n := indexFixtureInt(t, s, "SELECT COUNT(*) FROM library_index_pending WHERE target_path = ?", targetName); n != 1 {
		t.Fatal("move was not journaled")
	}
	if err := s.database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := NewScanner(s.musicSandbox, reopened, nil)
	indexFixtureExec(t, restarted, "DROP TRIGGER fail_move")
	if err := restarted.RetryPending(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ScanAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := indexFixtureInt(t, restarted, "SELECT id FROM tracks WHERE file_path = ?", targetName); got != id {
		t.Fatalf("restart changed the track ID from %d to %d", id, got)
	}
	if n := indexFixtureInt(t, restarted, "SELECT COUNT(*) FROM radio_history WHERE track_id = ?", id); n != 1 {
		t.Fatalf("restart lost the radio event: count=%d", n)
	}
	if n := indexFixtureInt(t, restarted, "SELECT COUNT(*) FROM library_index_pending"); n != 0 {
		t.Fatalf("successful recovery left %d pending changes", n)
	}
}

func TestMoveWithTagWriteFailureIndexesActualTags(t *testing.T) {
	s, _ := newIndexFixture(t)
	ctx := context.Background()
	sourceName, targetName := "Album/song.mp3", "Album/renamed.mp3"
	source := writeIndexFixtureTrack(t, s, sourceName, "Original", "Rock")
	target, err := s.musicSandbox.Resolve(targetName, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.IndexFile(source, sourceName); err != nil {
		t.Fatal(err)
	}
	id := indexFixtureInt(t, s, "SELECT id FROM tracks WHERE file_path = ?", sourceName)
	writeFailure := errors.New("tag write rejected")
	moved, err := organizer.MoveIndexedTrack(ctx, s, source, target, false, func(actual string) error {
		if n := indexFixtureInt(t, s, "SELECT COUNT(*) FROM library_index_pending WHERE source_path = ? AND target_path = ?", sourceName, targetName); n != 1 {
			t.Error("filesystem mutation ran without a recorded intention")
		}
		return writeFailure
	})
	if moved != target || !errors.Is(err, writeFailure) {
		t.Fatalf("expected a partial tag result after the move, got (%q, %v)", moved, err)
	}
	want := indexFixtureInt(t, s, "SELECT id FROM tracks WHERE file_path = ? AND title = 'Original'", targetName)
	if want != id {
		t.Fatalf("partially saved move changed the track ID from %d to %d", id, want)
	}
}

func TestRetryPendingDeletionAfterIndexError(t *testing.T) {
	s, _ := newIndexFixture(t)
	ctx := context.Background()
	path := "Album/song.mp3"
	full := writeIndexFixtureTrack(t, s, path, "Song", "Rock")
	if err := s.IndexFile(full, path); err != nil {
		t.Fatal(err)
	}
	indexFixtureExec(t, s, "CREATE TRIGGER fail_delete BEFORE DELETE ON tracks BEGIN SELECT RAISE(ABORT, 'index unavailable'); END")
	if _, err := s.DeletePath(ctx, full, false, func() error { return os.Remove(full) }); err == nil || !strings.Contains(err.Error(), "queued") {
		t.Fatalf("expected queued reconciliation after deleting the audio: %v", err)
	}
	if n := indexFixtureInt(t, s, "SELECT COUNT(*) FROM tracks WHERE file_path = ?", path); n != 1 {
		t.Fatal("failed transaction changed the track")
	}
	indexFixtureExec(t, s, "DROP TRIGGER fail_delete")
	if err := s.RetryPending(ctx); err != nil {
		t.Fatal(err)
	}
	if n := indexFixtureInt(t, s, "SELECT COUNT(*) FROM tracks WHERE file_path = ?", path); n != 0 {
		t.Fatal("retry did not remove the stale track")
	}
}
