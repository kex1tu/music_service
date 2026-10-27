package resolver

import (
	"strings"
	"testing"
)

func TestMatchTracklists_DirectURL(t *testing.T) {
	matcher := NewFullReleaseMatcher(nil)

	targetTracks := []ResolvedTrack{
		{TrackID: "t1", TrackNumber: 1, Title: "Банкомат", Artist: "DEAD BLONDE", Duration: 200},
		{TrackID: "t2", TrackNumber: 2, Title: "Красный свет", Artist: "DEAD BLONDE", Duration: 198},
	}

	candTracks := []AlbumTrackCandidate{
		{
			Title:     "Банкомат",
			Artist:    "DEAD BLONDE",
			Duration:  200,
			DirectURL: "https://soundcloud.com/rusyarapscene/dead-blonde-bankomat",
		},
		{
			Title:     "Красный свет",
			Artist:    "DEAD BLONDE",
			Duration:  198,
			DirectURL: "https://soundcloud.com/rusyarapscene/dead-blonde-krasny-svet",
		},
	}

	urls, matched := matcher.MatchTracklists(targetTracks, candTracks)
	if matched != 2 {
		t.Fatalf("expected 2 matches, got %d", matched)
	}
	if urls[0] != "https://soundcloud.com/rusyarapscene/dead-blonde-bankomat" {
		t.Errorf("track 0 url = %q, want direct soundcloud url", urls[0])
	}
	if urls[1] != "https://soundcloud.com/rusyarapscene/dead-blonde-krasny-svet" {
		t.Errorf("track 1 url = %q, want direct soundcloud url", urls[1])
	}
}

func TestSoundCloudSetSearcher_ParseSetEntries(t *testing.T) {
	searcher := NewSoundCloudSetSearcher("")

	sampleOutput := `
{"title": "DEAD BLONDE - Банкомат [FREE DL]", "duration": 200.0, "webpage_url": "https://soundcloud.com/user/track1", "uploader": "Русская Рэп-Сцена", "playlist_title": "DEAD BLONDE - СПЛЕТНИЦА"}
{"title": "DEAD BLONDE - Красный свет (Buy = Free DL)", "duration": 198.0, "webpage_url": "https://soundcloud.com/user/track2", "uploader": "Русская Рэп-Сцена", "playlist_title": "DEAD BLONDE - СПЛЕТНИЦА"}
`
	cand, err := searcher.ParseOutputBytes([]byte(sampleOutput), "https://soundcloud.com/user/sets/spletnitsa")
	if err != nil {
		t.Fatalf("ParseOutputBytes failed: %v", err)
	}
	if cand == nil {
		t.Fatal("expected cand, got nil")
	}
	if len(cand.Tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(cand.Tracks))
	}
	if cand.Tracks[0].Title != "Банкомат" {
		t.Errorf("track 0 title = %q, want 'Банкомат'", cand.Tracks[0].Title)
	}
	if cand.Tracks[0].DirectURL != "https://soundcloud.com/user/track1" {
		t.Errorf("track 0 direct URL = %q", cand.Tracks[0].DirectURL)
	}
	if !strings.Contains(cand.Title, "СПЛЕТНИЦА") {
		t.Errorf("cand title = %q, want contain 'СПЛЕТНИЦА'", cand.Title)
	}
}
