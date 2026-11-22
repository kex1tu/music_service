package scanner

import (
	"errors"
	"testing"

	"music-manager/internal/db"
	"music-manager/internal/id3"
	"music-manager/internal/testkit"
)

func TestReplacingIndexedMediaInvalidatesStaleMetadata(t *testing.T) {
	env := testkit.NewEnv(t)
	ctx := t.Context()
	rel := "Artist/Edition/01.mp3"
	tags := id3.TrackTags{Title: "Intro", Artist: "Ann", Album: "Old", AlbumArtist: "Ann",
		ISRC: "USAAA0000001", TrackNumber: 1, TrackTotal: 8}
	scan := NewScanner(env.MusicSandbox, env.DB, nil)
	path := env.CreateDummyMP3WithTags(rel, tags, nil)
	if err := scan.IndexFile(path, rel); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := env.DB.Conn().QueryRow("SELECT id FROM albums").Scan(&id); err != nil {
		t.Fatal(err)
	}
	verify := func() map[db.TrackPosition]bool {
		matched, err := db.VerifyCatalog(ctx, env.DB.Conn(), id, "100", []db.CatalogTrack{{
			BackfillSelection: db.BackfillSelection{TrackPosition: db.ReleasePosition(1, 1)},
			Title:             "Intro", Artist: "Ann", ISRC: "USAAA0000001",
		}})
		if err != nil {
			t.Fatal(err)
		}
		return matched
	}
	if len(verify()) != 1 {
		t.Fatal("the original file should match the catalog")
	}
	tags.Title, tags.ISRC, tags.TrackTotal = "New recording", "", 1
	path = env.CreateDummyMP3WithTags(rel, tags, nil)
	if err := scan.IndexFile(path, rel); err != nil {
		t.Fatal(err)
	}
	if len(verify()) != 0 {
		t.Fatal("the replacement inherited the original recording ID")
	}
	var oldRevision int64
	if err := env.DB.Conn().QueryRow("SELECT catalog_revision FROM albums WHERE id = ?", id).Scan(&oldRevision); err != nil {
		t.Fatal(err)
	}
	tags.Album = "New edition"
	path = env.CreateDummyMP3WithTags(rel, tags, nil)
	if err := scan.IndexFile(path, rel); err != nil {
		t.Fatal(err)
	}
	_, err := db.VerifyCatalog(ctx, env.DB.Conn(), id, "100", []db.CatalogTrack{{
		BackfillSelection: db.BackfillSelection{TrackPosition: db.ReleasePosition(1, 1)},
		Title:             "Intro", Artist: "Ann",
	}}, oldRevision)
	if !errors.Is(err, db.ErrCatalogAlbumChanged) {
		t.Fatalf("expected rejection of stale catalog response, got %v", err)
	}
	var stale, total int
	if err := env.DB.Conn().QueryRow(`SELECT catalog_checked_at IS NOT NULL OR deezer_id IS NOT NULL
		OR EXISTS(SELECT 1 FROM release_tracklist WHERE album_id = albums.id), total_tracks FROM albums WHERE id = ?`, id).
		Scan(&stale, &total); err != nil || stale != 0 || total != 1 {
		t.Fatalf("stale binding=%d tracks=%d error=%v", stale, total, err)
	}
}

func TestFullScanRemovesConflictsForMissingUnindexedFiles(t *testing.T) {
	env := testkit.NewEnv(t)
	if _, err := env.DB.Conn().Exec(`INSERT INTO library_artist_conflicts(file_path, artist_name, album_artist)
		VALUES ('gone.mp3', 'Twin', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := NewScanner(env.MusicSandbox, env.DB, nil).ScanAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := env.DB.Conn().QueryRow("SELECT COUNT(*) FROM library_artist_conflicts").Scan(&count); err != nil || count != 0 {
		t.Fatalf("conflicts=%d err=%v", count, err)
	}
}

func TestFullScanPreservesCatalogWithoutAlbumArtistTags(t *testing.T) {
	env := testkit.NewEnv(t)
	ctx := t.Context()
	scan := NewScanner(env.MusicSandbox, env.DB, nil)
	var catalog []db.CatalogTrack
	for i, track := range []struct{ path, title, artist string }{
		{"Album/01.mp3", "First", "Alice"},
		{"Album/02.mp3", "Second", "Bob"},
	} {
		env.CreateDummyMP3WithTags(track.path, id3.TrackTags{
			Title: track.title, Artist: track.artist, Album: "Album", TrackNumber: i + 1, Year: 2000 + i,
		}, nil)
		catalog = append(catalog, db.CatalogTrack{
			BackfillSelection: db.BackfillSelection{TrackPosition: db.ReleasePosition(1, i+1)},
			Title:             track.title, Artist: track.artist,
		})
	}
	if n, err := scan.ScanAll(ctx); err != nil || n != 2 {
		t.Fatalf("initial scan: indexed %d tracks: %v", n, err)
	}
	var id int64
	if err := env.DB.Conn().QueryRow(`SELECT id FROM albums WHERE folder_path = 'Album'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if matched, err := db.VerifyCatalog(ctx, env.DB.Conn(), id, "42", catalog); err != nil || len(matched) != 2 {
		t.Fatalf("verify catalog: %v, %v", matched, err)
	}
	var catalogRevision int
	if err := env.DB.Conn().QueryRow("SELECT catalog_revision FROM albums WHERE id = ?", id).Scan(&catalogRevision); err != nil {
		t.Fatal(err)
	}
	if n, err := scan.ScanAll(ctx); err != nil || n != 2 {
		t.Fatalf("rescan: indexed %d tracks: %v", n, err)
	}
	var deezerID string
	var revision, count, year int
	if err := env.DB.Conn().QueryRow(`SELECT COALESCE(deezer_id, ''), catalog_revision, year,
		(SELECT COUNT(*) FROM release_tracklist WHERE album_id = albums.id) FROM albums WHERE id = ?`, id).
		Scan(&deezerID, &revision, &year, &count); err != nil || deezerID != "42" || revision != catalogRevision || count != 2 || year != 2001 {
		t.Fatalf("unchanged files changed catalog: Deezer %q, revision %d, year %d, positions %d: %v", deezerID, revision, year, count, err)
	}
}
