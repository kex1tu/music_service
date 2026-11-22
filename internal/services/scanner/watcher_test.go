package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/testkit"
)

func watcherSandbox(t *testing.T) *sandbox.PathSandbox {
	t.Helper()
	sb, err := sandbox.NewPathSandbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return sb
}

func watcherPath(t *testing.T, sb *sandbox.PathSandbox, path string) string {
	t.Helper()
	full, err := sb.Resolve(path, false, sandbox.LiteralPath)
	if err != nil {
		t.Fatal(err)
	}
	return full
}

func watcherTrack(t *testing.T, sb *sandbox.PathSandbox, path, title string, number int) string {
	t.Helper()
	full, err := testkit.CreateDummyMP3WithTags(sb, path, id3.TrackTags{
		Title: title, Artist: "Band", Album: "Release", TrackNumber: number,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return full
}

func startWatcherFixture(t *testing.T, s *Scanner) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	if err := s.StartWatcher(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitForWatchedValue[T comparable](t *testing.T, read func() T, want T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		got := read()
		if got == want {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatalf("watcher left value %v, want %v", got, want)
		}
	}
}

func waitForWatchedTrackCount(t *testing.T, s *Scanner, path string, want int64) {
	t.Helper()
	waitForWatchedValue(t, func() int64 {
		return indexFixtureInt(t, s, "SELECT COUNT(*) FROM tracks WHERE file_path = ?", path)
	}, want)
}

func assertWatchedSearch(t *testing.T, s *Scanner, query, path string) {
	t.Helper()
	results, err := s.database.SearchLibrary(t.Context(), query, 10)
	if err != nil {
		t.Fatal(err)
	}
	if path == "" && len(results.Tracks) == 0 {
		return
	}
	if path == "" || len(results.Tracks) != 1 || results.Tracks[0].Path != path {
		t.Fatalf("search for %q returned %+v, want one track at %q or no tracks if empty", query, results.Tracks, path)
	}
}

func TestWatcherIndexesMovedAlbumAndLaterNestedTracks(t *testing.T) {
	s, _ := newIndexFixture(t)
	incoming := watcherSandbox(t)
	const first = "Album/CD1/01. Opener.mp3"
	const second = "Album/CD2/01. Interlude.mp3"
	const later = "Album/CD2/02. Finale.mp3"
	watcherTrack(t, incoming, first, "Opener", 1)
	watcherTrack(t, incoming, second, "Interlude", 1)
	startWatcherFixture(t, s)

	if err := os.Rename(watcherPath(t, incoming, "Album"), watcherPath(t, s.musicSandbox, "Album")); err != nil {
		t.Fatal(err)
	}
	waitForWatchedTrackCount(t, s, first, 1)
	waitForWatchedTrackCount(t, s, second, 1)
	assertWatchedSearch(t, s, "Opener", first)
	assertWatchedSearch(t, s, "Interlude", second)

	watcherTrack(t, s.musicSandbox, later, "Finale", 2)
	waitForWatchedTrackCount(t, s, later, 1)
	assertWatchedSearch(t, s, "Finale", later)
	if got := indexFixtureInt(t, s, "SELECT COUNT(*) FROM tracks"); got != 3 {
		t.Fatalf("watcher indexed %d tracks, want 3", got)
	}
}

func TestWatcherRemovesMovedAlbumAndKeepsNeighbor(t *testing.T) {
	s, _ := newIndexFixture(t)
	outside := watcherSandbox(t)
	const removed = "Album/CD2/01. Studio song.mp3"
	const neighbor = "Album Live/01. Live song.mp3"
	watcherTrack(t, s.musicSandbox, removed, "Studio song", 1)
	watcherTrack(t, s.musicSandbox, neighbor, "Live song", 1)
	if n, err := s.ScanAll(t.Context()); err != nil || n != 2 {
		t.Fatalf("initial scan indexed %d tracks: %v", n, err)
	}
	assertWatchedSearch(t, s, "Studio song", removed)
	startWatcherFixture(t, s)

	if err := os.Rename(watcherPath(t, s.musicSandbox, "Album"), watcherPath(t, outside, "Album")); err != nil {
		t.Fatal(err)
	}
	waitForWatchedTrackCount(t, s, removed, 0)
	if got := indexFixtureInt(t, s, "SELECT COUNT(*) FROM tracks WHERE file_path = ?", neighbor); got != 1 {
		t.Fatalf("neighbor has %d rows, want 1", got)
	}
	assertWatchedSearch(t, s, "Studio song", "")
	assertWatchedSearch(t, s, "Live song", neighbor)
}

func TestWatcherReindexesAtomicReplacement(t *testing.T) {
	s, _ := newIndexFixture(t)
	incoming := watcherSandbox(t)
	const track = "Album/01. Song.mp3"
	path := watcherTrack(t, s.musicSandbox, track, "Original title", 1)
	replacement := watcherTrack(t, incoming, "replacement.mp3", "Updated title", 1)
	if n, err := s.ScanAll(t.Context()); err != nil || n != 1 {
		t.Fatalf("initial scan indexed %d tracks: %v", n, err)
	}
	id := indexFixtureInt(t, s, "SELECT id FROM tracks WHERE file_path = ?", track)
	startWatcherFixture(t, s)

	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if err := safefs.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	waitForWatchedValue(t, func() string {
		var title string
		if err := s.database.Conn().QueryRow("SELECT COALESCE(MAX(title), '') FROM tracks WHERE file_path = ?", track).Scan(&title); err != nil {
			t.Fatal(err)
		}
		return title
	}, "Updated title")
	if n := indexFixtureInt(t, s, "SELECT COUNT(*) FROM tracks WHERE file_path = ? AND id = ?", track, id); n != 1 {
		t.Fatalf("replacement did not preserve track ID %d", id)
	}
	assertWatchedSearch(t, s, "Original title", "")
	assertWatchedSearch(t, s, "Updated title", track)
}

func TestWatcherRefreshesLyricsStatusWithoutMP3Write(t *testing.T) {
	s, _ := newIndexFixture(t)
	const track = "Album/01. Song.MP3"
	watcherTrack(t, s.musicSandbox, track, "Song", 1)
	if n, err := s.ScanAll(t.Context()); err != nil || n != 1 {
		t.Fatalf("initial scan indexed %d tracks: %v", n, err)
	}
	lrc := watcherPath(t, s.musicSandbox, "Album/01. Song.lrc")
	txt := watcherPath(t, s.musicSandbox, "Album/01. Song.txt")
	status := func() string {
		var status string
		if err := s.database.Conn().QueryRow("SELECT lyrics_status FROM tracks WHERE file_path = ?", track).Scan(&status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	if got := status(); got != "none" {
		t.Fatalf("initial lyrics status = %q", got)
	}
	startWatcherFixture(t, s)

	if err := safefs.WriteFile(lrc, []byte("[00:01.00]words"), 0600); err != nil {
		t.Fatal(err)
	}
	waitForWatchedValue(t, status, "lrc")
	if err := safefs.Rename(lrc, txt); err != nil {
		t.Fatal(err)
	}
	waitForWatchedValue(t, status, "txt")
	if err := safefs.Remove(txt); err != nil {
		t.Fatal(err)
	}
	waitForWatchedValue(t, status, "none")
}

func TestWatcherAndFullScanSkipExcludedSubtrees(t *testing.T) {
	s, _ := newIndexFixture(t)
	const visible = "tmp_downloads-live/CD1/01. Song.mp3"
	var excluded []string
	for _, dir := range []string{".hidden", "Album/tmp", "Album/tmp_downloads"} {
		path := filepath.Join(dir, "Nested", "CD1", "01. Song.mp3")
		excluded = append(excluded, path)
		watcherTrack(t, s.musicSandbox, path, "Ignored song", 1)
	}
	watcherTrack(t, s.musicSandbox, visible, "Initial control", 1)
	if n, err := s.ScanAll(t.Context()); err != nil || n != 1 {
		t.Fatalf("initial scan indexed %d tracks: %v", n, err)
	}
	startWatcherFixture(t, s)

	staging := watcherSandbox(t)
	overwrite := func(path, source string) {
		t.Helper()
		data, err := safefs.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := safefs.WriteFile(watcherPath(t, s.musicSandbox, path), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range excluded {
		overwrite(path, watcherPath(t, s.musicSandbox, path))
	}
	updateControl := func(title string) {
		t.Helper()
		overwrite(visible, watcherTrack(t, staging, title+".mp3", title, 1))
		waitForWatchedValue(t, func() string {
			var got string
			if err := s.database.Conn().QueryRow("SELECT title FROM tracks WHERE file_path = ?", visible).Scan(&got); err != nil {
				t.Fatal(err)
			}
			return got
		}, title)
	}
	updateControl("First control")
	// A second control update waits for a fresh event batch after the excluded writes.
	updateControl("Final control")
	if n := indexFixtureInt(t, s, "SELECT COUNT(*) FROM tracks"); n != 1 {
		t.Fatalf("watcher indexed excluded paths: %d total rows", n)
	}
	assertWatchedSearch(t, s, "Ignored song", "")
	assertWatchedSearch(t, s, "Final control", visible)
}
