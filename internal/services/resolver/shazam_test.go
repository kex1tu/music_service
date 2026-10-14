package resolver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestShazamProvider_RecognizeAudioFile(t *testing.T) {
	mockOutput := []byte(`{
		"matches": [{"id": "123"}],
		"track": {
			"key": "556677",
			"title": "Starman",
			"subtitle": "David Bowie",
			"genres": {"primary": "Rock"},
			"images": {
				"coverart": "https://images.shazam.com/cover/400x400cc.jpg",
				"coverarthq": "https://images.shazam.com/cover/800x800cc.jpg"
			},
			"sections": [
				{
					"type": "SONG",
					"metadata": [
						{"title": "Album", "text": "The Rise and Fall of Ziggy Stardust"},
						{"title": "Released", "text": "1972"},
						{"title": "Label", "text": "RCA Records"}
					]
				}
			]
		}
	}`)

	provider := NewShazamProvider(nil)
	provider.SetCommandExecutor(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "songrec" {
			return nil, fmt.Errorf("unexpected command: %s", name)
		}
		if len(args) != 2 || args[0] != "audio-file-to-recognized-song" || args[1] != "/safe/music/track.mp3" {
			return nil, fmt.Errorf("unexpected arguments: %v", args)
		}
		return mockOutput, nil
	})

	ctx := context.Background()
	rel, err := provider.RecognizeAudioFile(ctx, "/safe/music/track.mp3")
	if err != nil {
		t.Fatalf("unexpected recognize error: %v", err)
	}

	if rel.ID != "556677" {
		t.Errorf("expected ID 556677, got %s", rel.ID)
	}
	if rel.Title != "The Rise and Fall of Ziggy Stardust" {
		t.Errorf("expected album title, got %s", rel.Title)
	}
	if rel.Artist != "David Bowie" {
		t.Errorf("expected artist David Bowie, got %s", rel.Artist)
	}
	if rel.Year != 1972 {
		t.Errorf("expected year 1972, got %d", rel.Year)
	}
	if rel.Genre != "Rock" {
		t.Errorf("expected genre Rock, got %s", rel.Genre)
	}
	if rel.CoverURL != "https://images.shazam.com/cover/1000x1000cc.jpg" {
		t.Errorf("expected 1000x1000cc.jpg cover, got %s", rel.CoverURL)
	}
	if len(rel.Tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(rel.Tracks))
	}
	if rel.Tracks[0].Title != "Starman" {
		t.Errorf("expected track title Starman, got %s", rel.Tracks[0].Title)
	}
}

func TestShazamProvider_Search(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"resultCount": 1,
			"results": [
				{
					"trackName": "Heroes",
					"artistName": "David Bowie",
					"collectionName": "Heroes (2017 Remaster)",
					"primaryGenreName": "Rock",
					"releaseDate": "1977-10-14T07:00:00Z",
					"trackNumber": 3,
					"trackTimeMillis": 367000,
					"artworkUrl100": "https://is1-ssl.mzstatic.com/image/thumb/Music/v4/100x100bb.jpg"
				}
			]
		}`)
	}))
	defer server.Close()

	itunesClient := NewITunesClient(nil)
	itunesClient.SetBaseURL(server.URL)

	provider := NewShazamProvider(itunesClient)
	ctx := context.Background()

	results, err := provider.Search(ctx, "David Bowie Heroes")
	if err != nil {
		t.Fatalf("unexpected search error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	res := results[0]
	if res.Source != "shazam" {
		t.Errorf("expected source shazam, got %s", res.Source)
	}
	if res.Title != "Heroes (2017 Remaster)" {
		t.Errorf("expected collection name, got %s", res.Title)
	}
	if res.Artist != "David Bowie" {
		t.Errorf("expected artist David Bowie, got %s", res.Artist)
	}
	if res.Year != 1977 {
		t.Errorf("expected year 1977, got %d", res.Year)
	}
	if res.CoverURL != "https://is1-ssl.mzstatic.com/image/thumb/Music/v4/1000x1000bb.jpg" {
		t.Errorf("expected HQ artwork 1000x1000bb.jpg, got %s", res.CoverURL)
	}
}
