package resolver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSourceResolver_ResolveYandex(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/albums/3087311/with-tracks":
			w.Write([]byte(`{
				"result": {
					"id": 3087311,
					"title": "Горгород",
					"year": 2015,
					"genre": "hiphop",
					"metaType": "music",
					"coverUri": "avatars.yandex.net/get-music-content/5678/cover-%%",
					"trackCount": 2,
					"artists": [{"id": 249991, "name": "Oxxxymiron"}],
					"volumes": [
						[
							{
								"id": 26078311,
								"title": "Не с начала",
								"durationMs": 130000,
								"artists": [{"id": 249991, "name": "Oxxxymiron"}]
							},
							{
								"id": 26078312,
								"title": "Где нас нет",
								"durationMs": 265000,
								"artists": [
									{"id": 249991, "name": "Oxxxymiron"},
									{"id": 99999, "name": "ЛСП"}
								]
							}
						]
					]
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	res := NewSourceResolver(nil, nil, "")
	res.Yandex().SetBaseURL(server.URL)

	meta, tracks, err := res.Resolve(context.Background(), "https://music.yandex.ru/album/3087311", "yandex")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if meta.AlbumTitle != "Горгород" {
		t.Errorf("AlbumTitle = %q, want 'Горгород'", meta.AlbumTitle)
	}
	if meta.AlbumArtist != "Oxxxymiron" {
		t.Errorf("AlbumArtist = %q, want 'Oxxxymiron'", meta.AlbumArtist)
	}
	if len(tracks) != 2 {
		t.Fatalf("Tracks len = %d, want 2", len(tracks))
	}

	// First track: solo
	if tracks[0].Title != "Не с начала" || tracks[0].Artist != "Oxxxymiron" {
		t.Errorf("Track[0] = (%q, %q)", tracks[0].Title, tracks[0].Artist)
	}

	// Second track: collaboration with ЛСП
	if tracks[1].Title != "Где нас нет" || tracks[1].Artist != "Oxxxymiron\\\\ЛСП" {
		t.Errorf("Track[1] = (%q, %q), want 'Oxxxymiron\\\\ЛСП'", tracks[1].Title, tracks[1].Artist)
	}
}
