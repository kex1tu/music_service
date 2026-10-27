package id3

import (
	"testing"
)

func TestSyncAlbumArtistAndTrackArtists_EmptyAlbumArtistUniformTracks(t *testing.T) {
	tracks := []TrackTags{
		{Title: "Track 1", Artist: "Queen"},
		{Title: "Track 2", Artist: "Queen"},
		{Title: "Track 3", Artist: "Queen"},
	}

	aa, synced := SyncAlbumArtistAndTrackArtists("", tracks)
	if aa != "Queen" {
		t.Errorf("expected album artist 'Queen', got %q", aa)
	}
	for i, tr := range synced {
		if tr.AlbumArtist != "Queen" {
			t.Errorf("track %d expected AlbumArtist 'Queen', got %q", i, tr.AlbumArtist)
		}
		if tr.Artist != "Queen" {
			t.Errorf("track %d expected Artist 'Queen', got %q", i, tr.Artist)
		}
	}
}

func TestSyncAlbumArtistAndTrackArtists_IncorporateAlbumArtistIntoGuestTrack(t *testing.T) {
	tracks := []TrackTags{
		{Title: "Intro", Artist: "Kanye West"},
		{Title: "Monster", Artist: "Jay-Z\\\\Nicki Minaj"}, // Kanye West is missing in track Artist
	}

	aa, synced := SyncAlbumArtistAndTrackArtists("Kanye West", tracks)
	if aa != "Kanye West" {
		t.Errorf("expected album artist 'Kanye West', got %q", aa)
	}
	if synced[0].Artist != "Kanye West" {
		t.Errorf("track 0 expected Artist 'Kanye West', got %q", synced[0].Artist)
	}
	expectedTrack1 := "Kanye West\\\\Jay-Z\\\\Nicki Minaj"
	if synced[1].Artist != expectedTrack1 {
		t.Errorf("track 1 expected Artist %q, got %q", expectedTrack1, synced[1].Artist)
	}
	if synced[1].AlbumArtist != "Kanye West" {
		t.Errorf("track 1 expected AlbumArtist 'Kanye West', got %q", synced[1].AlbumArtist)
	}
}

func TestSyncAlbumArtistAndTrackArtists_CompilationSoundtrack(t *testing.T) {
	tracks := []TrackTags{
		{Title: "Track 1", Artist: "Daft Punk"},
		{Title: "Track 2", Artist: "M83"},
	}

	aa, synced := SyncAlbumArtistAndTrackArtists("Various Artists", tracks)
	if aa != "Various Artists" {
		t.Errorf("expected album artist 'Various Artists', got %q", aa)
	}
	if synced[0].Artist != "Daft Punk" {
		t.Errorf("track 0 artist should remain 'Daft Punk', got %q", synced[0].Artist)
	}
	if synced[1].Artist != "M83" {
		t.Errorf("track 1 artist should remain 'M83', got %q", synced[1].Artist)
	}
	if synced[0].AlbumArtist != "Various Artists" || synced[1].AlbumArtist != "Various Artists" {
		t.Errorf("all tracks should have AlbumArtist 'Various Artists'")
	}
}
