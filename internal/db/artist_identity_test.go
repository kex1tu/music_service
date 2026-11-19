package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestArtistIdentityMigration(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "v4.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	conn, err := old.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, migrate := range migrations[:4] {
		if err := migrate(ctx, conn); err != nil {
			t.Fatal(err)
		}
	}
	_, err = conn.ExecContext(ctx, `
		PRAGMA user_version = 4;
		INSERT INTO artists(id, name, clean_name) VALUES
			(11,'Main','main'), (12,'Guest','guest'),
			(13,'Björk','björk'), (14,'björk','björk');
		INSERT INTO tracked_artists(id,name,clean_name,auto_download) VALUES (70,'MAIN','main',1);
		INSERT INTO pinned_track_sources(cache_key,artist_name,track_title,pinned_url,source_type) VALUES
			('match','main','Song','https://youtu.be/example','manual'),
			('ambiguous','BJÖRK','Song','https://youtu.be/example','manual');
		INSERT INTO radar_releases(id,artist_name,clean_artist_name,title,clean_title,release_type,source_url,status)
			VALUES (50,'Main, Guest','main, guest','Song','song','single','catalog','ignored'),
			       (51,'Future','future','Later','later','album','catalog','new');
		INSERT INTO artist_relations(artist_name,related_name,score,source) VALUES ('main','björk',0.6,'legacy');
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	assert := func(query string, want int) {
		t.Helper()
		if got := libraryRowCount(t, database.conn, query); got != want {
			t.Fatalf("%s = %d, want %d", query, got, want)
		}
	}
	assert("PRAGMA user_version", 5)
	assert("SELECT COUNT(*) FROM tracked_artists WHERE id=70 AND artist_id=11 AND auto_download=1", 1)
	assert("SELECT COUNT(*) FROM pinned_track_sources WHERE cache_key='match' AND artist_id=11", 1)
	assert("SELECT COUNT(*) FROM pinned_track_sources WHERE cache_key='ambiguous' AND artist_id IS NULL", 1)
	assert("SELECT COUNT(*) FROM radar_release_artists WHERE release_id=50 AND artist_id IN (11,12)", 2)
	assert("SELECT COUNT(*) FROM artist_relations WHERE artist_id=11 AND related_artist_id IS NULL", 1)

	mustReleaseSQL(t, database.conn, "UPDATE artists SET name='New Main', clean_name='new main' WHERE id=11")
	releases, err := database.GetArtistDiscography(ctx, "New Main")
	if err != nil || len(releases) != 1 || releases[0].ID != 50 || releases[0].Status != "ignored" {
		t.Fatalf("renamed artist lost radar history: %+v, %v", releases, err)
	}
	mustReleaseSQL(t, database.conn, "INSERT INTO artists(id,name,clean_name) VALUES (15,'Future','future')")
	assert("SELECT COUNT(*) FROM radar_release_artists WHERE release_id=51 AND artist_id=15", 1)

	if err := database.SaveArtistRelations(ctx, "New Main", []ArtistRelationItem{{RelatedName: "Guest", Score: 1, Source: "manual"}}); err != nil {
		t.Fatal(err)
	}
	mustReleaseSQL(t, database.conn, "UPDATE artists SET name='New Guest', clean_name='new guest' WHERE id=12")
	related, err := database.GetRelatedArtists(ctx, "New Main", 5)
	if err != nil || len(related) == 0 || related[0] != "New Guest" {
		t.Fatalf("renamed guest lost similarity: %v, %v", related, err)
	}
	mustReleaseSQL(t, database.conn, `INSERT INTO pinned_track_sources(cache_key,artist_name,track_title,pinned_url,source_type)
		VALUES ('legacy-empty','','','https://youtu.be/old','manual')`)
	if err := database.SavePinnedSource(ctx, PinnedTrackSource{
		CacheKey: "legacy-empty", ArtistName: "New Main", TrackTitle: "Song", PinnedURL: "https://youtu.be/new", SourceType: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	assert("SELECT COUNT(*) FROM pinned_track_sources WHERE cache_key='legacy-empty' AND artist_id=11", 1)

	if err := database.ResetLibraryTables(ctx); err != nil {
		t.Fatal(err)
	}
	assert("SELECT COUNT(*) FROM artists WHERE id IN (11,12,15)", 3)
	assert("SELECT COUNT(*) FROM artists WHERE id IN (13,14)", 0)
	assert("SELECT COUNT(*) FROM pragma_foreign_key_check", 0)
}
