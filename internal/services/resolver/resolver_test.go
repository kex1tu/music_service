package resolver

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"music-manager/internal/id3"
)

func TestDetectSource_TorrentAndMagnet(t *testing.T) {
	r := NewSourceResolver(nil, nil, "")

	tests := []struct {
		input string
		want  string
	}{
		{"magnet:?xt=urn:btih:12345&dn=Radiohead+-+OK+Computer", "torrent"},
		{"https://example.com/releases/album.torrent", "torrent"},
		{"https://example.com/releases/album.torrent?passkey=xyz", "torrent"},
		{"https://www.deezer.com/album/123", "deezer"},
		{"https://www.youtube.com/watch?v=xyz", "youtube"},
	}

	for _, tt := range tests {
		got, err := r.DetectSource(tt.input)
		if err != nil {
			t.Errorf("DetectSource(%q) error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("DetectSource(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCleanReleaseTitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Radiohead - OK Computer [FLAC]", "Radiohead - OK Computer"},
		{"Daft Punk - Discovery (2001) [MP3 320]", "Daft Punk - Discovery (2001)"},
		{"Pink_Floyd_-_The_Wall_(Vinyl_Rip)", "Pink Floyd - The Wall"},
		{"Linkin+Park+-+Meteora+[24bit]", "Linkin Park - Meteora"},
	}

	for _, tt := range tests {
		got := cleanReleaseTitle(tt.input)
		if got != tt.want {
			t.Errorf("cleanReleaseTitle(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestResolve_TorrentNotFound(t *testing.T) {
	r := NewSourceResolver(nil, nil, "")
	_, _, err := r.Resolve(context.Background(), "magnet:?xt=urn:btih:abc&dn=NonExistentReleaseXYZ", "torrent")
	if err == nil {
		t.Errorf("expected error for non-existent torrent release, got nil")
	}
}

func TestFindBestMatch_MaloletkaRemix(t *testing.T) {
	m := NewAudioMatcher("")
	matchURL, err := m.FindBestMatch(context.Background(), "Малолетка (Розочка Remix)", "Макс Корж", 196)
	if err != nil {
		t.Fatalf("FindBestMatch failed: %v", err)
	}
	if matchURL == "" {
		t.Fatalf("expected non-empty matchURL, got empty")
	}
	t.Logf("Found match URL: %s", matchURL)
}

func TestDetectSource_YouTubeMusicArtist(t *testing.T) {
	r := NewSourceResolver(nil, nil, "")

	urls := []string{
		"https://music.youtube.com/channel/UC2XdaAVUannpujzv32jcouQ",
		"https://music.youtube.com/channel/UC2XdaAVUannpujzv32jcouQ/releases",
	}

	for _, u := range urls {
		src, err := r.DetectSource(u)
		if err != nil {
			t.Fatalf("DetectSource(%q) failed: %v", u, err)
		}
		if src != "youtube" {
			t.Errorf("expected source 'youtube', got %q", src)
		}
	}
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestResolve_DeezerAlbumContributors(t *testing.T) {
	mockTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		path := req.URL.Path
		var body string
		switch path {
		case "/album/1001":
			body = `{
				"id": 1001,
				"title": "AA LANGUAGE 2",
				"nb_tracks": 2,
				"artist": {"id": 1582524, "name": "Aarne"},
				"tracks": {
					"data": [
						{"id": 2001, "title": "Haunted House", "duration": 154, "track_position": 1, "disk_number": 1, "artist": {"id": 1582524, "name": "Aarne"}},
						{"id": 2002, "title": "4 ur girl", "duration": 176, "track_position": 2, "disk_number": 1, "artist": {"id": 1582524, "name": "Aarne"}}
					]
				}
			}`
		case "/track/2001":
			body = `{
				"id": 2001,
				"title": "Haunted House",
				"isrc": "RUB422303132",
				"contributors": [
					{"id": 1582524, "name": "Aarne", "role": "Main"},
					{"id": 14277587, "name": "Big Baby Tape", "role": "Main"},
					{"id": 8687202, "name": "Kizaru", "role": "Main"}
				]
			}`
		case "/track/2002":
			body = `{
				"id": 2002,
				"title": "4 ur girl",
				"isrc": "RUB422303134",
				"contributors": [
					{"id": 1582524, "name": "Aarne", "role": "Main"},
					{"id": 14277587, "name": "Big Baby Tape", "role": "Main"},
					{"id": 99999, "name": "Toxi$", "role": "Featured"},
					{"id": 88888, "name": "Chief Keef", "role": "Featured"}
				]
			}`
		default:
			body = `{"error": {"type": "DataException", "message": "no data", "code": 800}}`
		}

		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	})

	deezerClient := NewDeezerClient(mockTransport)
	r := NewSourceResolver(deezerClient, nil, "")
	// Disable external feat enricher lookups during mock test
	r.featEnricher = nil

	meta, tracks, err := r.Resolve(context.Background(), "https://www.deezer.com/album/1001", "deezer")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if meta.AlbumTitle != "AA LANGUAGE 2" {
		t.Errorf("expected album title 'AA LANGUAGE 2', got %q", meta.AlbumTitle)
	}
	if meta.AlbumArtist != "Aarne" {
		t.Errorf("expected album artist 'Aarne', got %q", meta.AlbumArtist)
	}
	if len(tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(tracks))
	}

	// Проверяем первый трек: Haunted House (Aarne\\Big Baby Tape\\Kizaru)
	t1 := tracks[0]
	expectedArtist1 := id3.FormatMultiValue([]string{"Aarne", "Big Baby Tape", "Kizaru"})
	if t1.Artist != expectedArtist1 {
		t.Errorf("expected t1.Artist %q, got %q", expectedArtist1, t1.Artist)
	}
	if t1.ISRC == nil || *t1.ISRC != "RUB422303132" {
		t.Errorf("expected ISRC 'RUB422303132', got %v", t1.ISRC)
	}

	// Проверяем второй трек: 4 ur girl (Aarne\\Big Baby Tape\\Toxi$\\Chief Keef)
	t2 := tracks[1]
	expectedArtist2 := id3.FormatMultiValue([]string{"Aarne", "Big Baby Tape", "Toxi$", "Chief Keef"})
	if t2.Artist != expectedArtist2 {
		t.Errorf("expected t2.Artist %q, got %q", expectedArtist2, t2.Artist)
	}
	if t2.ISRC == nil || *t2.ISRC != "RUB422303134" {
		t.Errorf("expected ISRC 'RUB422303134', got %v", t2.ISRC)
	}
}

