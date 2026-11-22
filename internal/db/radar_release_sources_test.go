package db

import (
	"path/filepath"
	"testing"
)

func TestRadarReleaseSourcesMigrateAndJoinConfirmedRecords(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "radar.db")
	old, err := openTestSchemaVersion(ctx, path, 9)
	if err != nil {
		t.Fatal(err)
	}
	mustReleaseSQL(t, old.Conn(), `
		INSERT INTO artists(id, name, clean_name) VALUES (1,'AC/DC','ac/dc'),(2,'Guest','guest'),(3,'AC','ac');
		INSERT INTO radar_releases(id, artist_name, clean_artist_name, title, clean_title, release_type, source_url,
			deezer_id, musicbrainz_id, identity_key, status)
		VALUES (11,'AC/DC','ac/dc','Edition','edition','album','https://www.deezer.com/album/55','55',NULL,'deezer:55','new'),
		       (12,'Guest','guest','Catalog title','catalog title','album','https://musicbrainz.org/release-group/group',
		       NULL,'group','musicbrainz:group','ignored');
	`)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	count := func(query string, want int) {
		t.Helper()
		var got int
		if err := database.Conn().QueryRowContext(ctx, query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got %d, want %d, err %v", query, got, want, err)
		}
	}
	count("SELECT count(*) FROM radar_release_sources", 2)

	release := RadarReleaseEntity{ArtistName: "AC/DC, Guest", CatalogCredits: []string{"AC/DC", "Guest"},
		Title: "Edition", DeezerID: "55", MusicBrainzID: "group", IsRadar: true,
		MusicBrainzMetadata: &RadarSourceMetadata{Title: "Catalog title", ArtistName: "Guest"}}
	if err := database.UpsertRadarRelease(ctx, &release); err != nil {
		t.Fatal(err)
	}
	if release.ID != 11 || release.Status != "ignored" || release.IsRadar {
		t.Fatalf("unexpected canonical card: %+v", release)
	}
	count("SELECT count(*) FROM radar_releases", 1)
	count("SELECT count(*) FROM radar_release_sources WHERE release_id=11", 2)
	count("SELECT count(*) FROM radar_release_sources WHERE provider='musicbrainz' AND title='Catalog title'", 1)
	for id, want := range map[int64]int{1: 1, 2: 1, 3: 0} {
		releases, err := QueryArtistDiscography(ctx, database.Conn(), "", id)
		if err != nil || len(releases) != want {
			t.Fatalf("artist %d: got %d records, want %d, err %v", id, len(releases), want, err)
		}
	}

	edition := RadarReleaseEntity{ArtistName: "AC/DC", CatalogCredits: []string{"AC/DC"}, Title: "Edition (Deluxe)",
		DeezerID: "56", MusicBrainzID: "group", IsRadar: true}
	if err := database.UpsertRadarRelease(ctx, &edition); err != nil {
		t.Fatal(err)
	}
	if edition.ID == release.ID || edition.Status != "new" {
		t.Fatalf("distinct edition was combined: %+v", edition)
	}
	count("SELECT count(*) FROM radar_releases", 2)
	count("SELECT count(*) FROM radar_release_sources WHERE provider='musicbrainz' AND external_id='group'", 2)
	count("SELECT count(*) FROM pragma_foreign_key_check", 0)
}

func TestRadarReleaseReusesMusicBrainzCardWhenDeezerIsAdded(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "radar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	group := RadarReleaseEntity{ArtistName: "Artist", Title: "Record", MusicBrainzID: "group", Status: "downloaded"}
	if err := database.UpsertRadarRelease(t.Context(), &group); err != nil {
		t.Fatal(err)
	}
	edition := RadarReleaseEntity{ArtistName: "ARTIST", Title: "RECORD", DeezerID: "55", MusicBrainzID: "group", IsRadar: true}
	if err := database.UpsertRadarRelease(t.Context(), &edition); err != nil {
		t.Fatal(err)
	}
	if edition.ID != group.ID || edition.Status != "downloaded" || edition.IsRadar {
		t.Fatalf("promotion changed the card: before %+v, after %+v", group, edition)
	}
}

func TestRadarReplacesSourceCreditsAndPreservesEdition(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := t.Context()
	mustReleaseSQL(t, database.Conn(), `INSERT INTO artists(id, name, clean_name, deezer_id) VALUES
		(71,'Twin','twin','101'), (72,'Twin','twin','102'), (73,'Guest','guest',NULL)`)
	upsert := func(r RadarReleaseEntity) RadarReleaseEntity {
		t.Helper()
		if err := database.UpsertRadarRelease(ctx, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	check := func(artistID int64, count int) {
		t.Helper()
		releases, err := QueryArtistDiscography(ctx, database.Conn(), "", artistID)
		if len(releases) != count || err != nil {
			t.Fatalf("artist %d: got %d releases, want %d, err=%v", artistID, len(releases), count, err)
		}
	}
	edition := upsert(RadarReleaseEntity{Title: "Deluxe", ArtistName: "Twin, Guest", DeezerID: "42",
		MusicBrainzID: "group", Year: 2022, ReleaseDate: "2022-04-01", TrackCount: 15,
		ProviderCredits: []RadarCredit{{Name: "Twin", ExternalID: "101"}, {Name: "Guest"}}})
	check(71, 1)
	check(72, 0)
	check(73, 1)
	group := upsert(RadarReleaseEntity{Title: "Original", ArtistName: "Guest", MusicBrainzID: "group",
		Year: 1990, ReleaseDate: "1990-01-01", TrackCount: 9, ProviderCredits: []RadarCredit{{Name: "Guest"}}})
	if group.ID != edition.ID {
		t.Fatalf("group ID %d, edition ID %d", group.ID, edition.ID)
	}
	edition.ProviderCredits = edition.ProviderCredits[:1]
	edition.ArtistName = "Twin"
	upsert(edition)
	check(73, 1)
	group.ProviderCredits = []RadarCredit{}
	upsert(group)
	check(73, 0)
	check(71, 1)
	check(72, 0)
	card, err := database.GetRadarReleaseByID(ctx, edition.ID)
	if err != nil || card.Title != "Deluxe" || card.Year != 2022 || card.ReleaseDate != "2022-04-01" || card.TrackCount != 15 {
		t.Fatalf("edition metadata changed: %+v, err=%v", card, err)
	}
}
