package resolver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestYandexMusicClient_ParseURL(t *testing.T) {
	client := NewYandexMusicClient("")

	tests := []struct {
		url          string
		wantResource string
		wantID       int64
		wantErr      bool
	}{
		{"https://music.yandex.ru/artist/249991", "artist", 249991, false},
		{"https://music.yandex.com/artist/108920/albums", "artist", 108920, false},
		{"https://music.yandex.ru/album/3087311", "album", 3087311, false},
		{"https://music.yandex.ru/album/3087311/track/26078311", "album", 3087311, false},
		{"https://music.yandex.ru/track/26078311", "track", 26078311, false},
		{"https://example.com/other", "", 0, true},
	}

	for _, tt := range tests {
		res, id, err := client.ParseURL(tt.url)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseURL(%q) err = %v, wantErr = %v", tt.url, err, tt.wantErr)
			continue
		}
		if res != tt.wantResource || id != tt.wantID {
			t.Errorf("ParseURL(%q) = (%q, %d), want (%q, %d)", tt.url, res, id, tt.wantResource, tt.wantID)
		}
	}
}

func TestYandexMusicClient_MockEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/artists/249991/brief-info":
			w.Write([]byte(`{
				"result": {
					"artist": {
						"id": 249991,
						"name": "Oxxxymiron",
						"cover": {
							"uri": "avatars.yandex.net/get-music-content/1234/avatar-%%"
						}
					}
				}
			}`))
		case "/artists/249991/direct-albums":
			w.Write([]byte(`{
				"result": {
					"albums": [
						{
							"id": 3087311,
							"title": "Горгород",
							"year": 2015,
							"metaType": "music",
							"coverUri": "avatars.yandex.net/get-music-content/5678/cover-%%",
							"trackCount": 11,
							"artists": [{"id": 249991, "name": "Oxxxymiron"}]
						}
					]
				}
			}`))
		case "/albums/3087311/with-tracks":
			w.Write([]byte(`{
				"result": {
					"id": 3087311,
					"title": "Горгород",
					"year": 2015,
					"metaType": "music",
					"coverUri": "avatars.yandex.net/get-music-content/5678/cover-%%",
					"trackCount": 1,
					"artists": [{"id": 249991, "name": "Oxxxymiron"}],
					"volumes": [
						[
							{
								"id": 26078311,
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
		case "/search":
			w.Write([]byte(`{
				"result": {
					"tracks": {
						"results": [
							{
								"id": 111,
								"title": "Сансара",
								"artists": [
									{"name": "Баста"},
									{"name": "Диана Арбенина"},
									{"name": "Александр Ф. Скляр"}
								]
							}
						]
					}
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewYandexMusicClient("")
	client.SetBaseURL(server.URL)
	ctx := context.Background()

	// 1. Brief Info
	artist, err := client.GetArtistBriefInfo(ctx, 249991)
	if err != nil {
		t.Fatalf("GetArtistBriefInfo failed: %v", err)
	}
	if artist.Name != "Oxxxymiron" {
		t.Errorf("Artist Name = %q, want 'Oxxxymiron'", artist.Name)
	}
	if artist.AvatarURL() != "https://avatars.yandex.net/get-music-content/1234/avatar-1000x1000" {
		t.Errorf("AvatarURL = %q", artist.AvatarURL())
	}

	// 2. Direct Albums
	albums, err := client.GetArtistDirectAlbums(ctx, 249991, 0, 10)
	if err != nil {
		t.Fatalf("GetArtistDirectAlbums failed: %v", err)
	}
	if len(albums) != 1 || albums[0].Title != "Горгород" {
		t.Fatalf("Albums = %v, want 1 album 'Горгород'", albums)
	}
	if albums[0].BestCoverURL() != "https://avatars.yandex.net/get-music-content/5678/cover-1000x1000" {
		t.Errorf("BestCoverURL = %q", albums[0].BestCoverURL())
	}
	if albums[0].RecordType() != "album" {
		t.Errorf("RecordType = %q, want 'album'", albums[0].RecordType())
	}

	// 3. Album with Tracks
	albTracks, err := client.GetAlbumWithTracks(ctx, 3087311)
	if err != nil {
		t.Fatalf("GetAlbumWithTracks failed: %v", err)
	}
	if len(albTracks.Tracks) != 1 {
		t.Fatalf("Tracks len = %d, want 1", len(albTracks.Tracks))
	}
	allArtists := albTracks.Tracks[0].AllArtistNames()
	if len(allArtists) != 2 || allArtists[1] != "ЛСП" {
		t.Errorf("Track all artists = %v, want ['Oxxxymiron', 'ЛСП']", allArtists)
	}

	// 4. Search Track
	searchRes, err := client.SearchTrack(ctx, "Сансара")
	if err != nil {
		t.Fatalf("SearchTrack failed: %v", err)
	}
	if len(searchRes) != 1 || len(searchRes[0].Artists) != 3 {
		t.Errorf("Search artists = %v, want 3 artists", searchRes)
	}
}

func TestYandexMusicClient_HTTP451Geoblock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnavailableForLegalReasons) // 451
		w.Write([]byte(`{"error":{"name":"Unavailable For Legal Reasons","message":""}}`))
	}))
	defer server.Close()

	client := NewYandexMusicClient("")
	client.SetBaseURL(server.URL)
	ctx := context.Background()

	// 1. Brief info
	_, err := client.GetArtistBriefInfo(ctx, 8135481)
	if err == nil || !strings.Contains(err.Error(), "HTTP 451 Geoblock") {
		t.Errorf("GetArtistBriefInfo expected HTTP 451 Geoblock error, got: %v", err)
	}

	// 2. Direct albums
	_, err = client.GetArtistDirectAlbums(ctx, 8135481, 0, 10)
	if err == nil || !strings.Contains(err.Error(), "HTTP 451 Geoblock") {
		t.Errorf("GetArtistDirectAlbums expected HTTP 451 Geoblock error, got: %v", err)
	}

	// 3. Album with tracks
	_, err = client.GetAlbumWithTracks(ctx, 12345)
	if err == nil || !strings.Contains(err.Error(), "HTTP 451 Geoblock") {
		t.Errorf("GetAlbumWithTracks expected HTTP 451 Geoblock error, got: %v", err)
	}

	// 4. Search track
	_, err = client.SearchTrack(ctx, "Toxi$")
	if err == nil || !strings.Contains(err.Error(), "HTTP 451 Geoblock") {
		t.Errorf("SearchTrack expected HTTP 451 Geoblock error, got: %v", err)
	}
}

func TestYandexMusicClient_ProxyConfiguration(t *testing.T) {
	client := NewYandexMusicClient("dummy_token", "http://127.0.0.1:8080")
	if client.token != "dummy_token" {
		t.Errorf("client token = %q, want 'dummy_token'", client.token)
	}
	tr, ok := client.httpClient.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatalf("expected *http.Transport in client.httpClient.Transport")
	}
	if tr.Proxy == nil {
		t.Errorf("expected tr.Proxy to be set when proxyURL is provided")
	}
}

func TestYandexCoverURLs_OriginalAndCandidates(t *testing.T) {
	// 1. Проверяем OriginalCoverURL на альбоме
	album := &YandexAlbum{
		CoverURI: "avatars.yandex.net/get-music-content/5678/cover-%%",
	}
	if album.BestCoverURL() != "https://avatars.yandex.net/get-music-content/5678/cover-1000x1000" {
		t.Errorf("BestCoverURL = %q, want 1000x1000", album.BestCoverURL())
	}
	if album.OriginalCoverURL() != "https://avatars.yandex.net/get-music-content/5678/cover-orig" {
		t.Errorf("OriginalCoverURL = %q, want orig", album.OriginalCoverURL())
	}

	// 2. Проверяем OriginalCoverURL на плейлисте
	playlist := &YandexPlaylist{
		Cover: YandexCover{
			URI: "avatars.yandex.net/get-music-user-playlist/123/%%",
		},
	}
	if playlist.BestCoverURL() != "https://avatars.yandex.net/get-music-user-playlist/123/1000x1000" {
		t.Errorf("playlist BestCoverURL = %q, want 1000x1000", playlist.BestCoverURL())
	}
	if playlist.OriginalCoverURL() != "https://avatars.yandex.net/get-music-user-playlist/123/orig" {
		t.Errorf("playlist OriginalCoverURL = %q, want orig", playlist.OriginalCoverURL())
	}

	// 3. Проверяем OriginalAvatarURL на артисте
	artist := &YandexArtistMeta{
		Cover: struct {
			URI    string `json:"uri"`
			Prefix string `json:"prefix"`
		}{
			URI: "avatars.yandex.net/get-music-content/999/avatar-%%",
		},
	}
	if artist.AvatarURL() != "https://avatars.yandex.net/get-music-content/999/avatar-1000x1000" {
		t.Errorf("artist AvatarURL = %q, want 1000x1000", artist.AvatarURL())
	}
	if artist.OriginalAvatarURL() != "https://avatars.yandex.net/get-music-content/999/avatar-orig" {
		t.Errorf("artist OriginalAvatarURL = %q, want orig", artist.OriginalAvatarURL())
	}

	// 4. Проверяем YandexCoverCandidateURLs
	testCases := []struct {
		input        string
		wantOrig     string
		wantFallback string
	}{
		{
			input:        "https://avatars.yandex.net/get-music-content/456/1000x1000",
			wantOrig:     "https://avatars.yandex.net/get-music-content/456/orig",
			wantFallback: "https://avatars.yandex.net/get-music-content/456/1000x1000",
		},
		{
			input:        "https://avatars.yandex.net/get-music-content/456/orig",
			wantOrig:     "https://avatars.yandex.net/get-music-content/456/orig",
			wantFallback: "https://avatars.yandex.net/get-music-content/456/1000x1000",
		},
		{
			input:        "https://avatars.yandex.net/get-music-content/456/400x400",
			wantOrig:     "https://avatars.yandex.net/get-music-content/456/orig",
			wantFallback: "https://avatars.yandex.net/get-music-content/456/1000x1000",
		},
		{
			input:        "https://avatars.yandex.net/get-music-content/456/%%",
			wantOrig:     "https://avatars.yandex.net/get-music-content/456/orig",
			wantFallback: "https://avatars.yandex.net/get-music-content/456/1000x1000",
		},
		{
			input:        "http://127.0.0.1:8080/get-music-content/456/1000x1000",
			wantOrig:     "http://127.0.0.1:8080/get-music-content/456/orig",
			wantFallback: "http://127.0.0.1:8080/get-music-content/456/1000x1000",
		},
		{
			input:        "https://e-cdns-images.dzcdn.net/images/cover/123/1000x1000.jpg",
			wantOrig:     "https://e-cdns-images.dzcdn.net/images/cover/123/1000x1000.jpg",
			wantFallback: "",
		},
		{
			input:        "",
			wantOrig:     "",
			wantFallback: "",
		},
	}

	for _, tc := range testCases {
		orig, fallback := YandexCoverCandidateURLs(tc.input)
		if orig != tc.wantOrig || fallback != tc.wantFallback {
			t.Errorf("YandexCoverCandidateURLs(%q) = (%q, %q), want (%q, %q)", tc.input, orig, fallback, tc.wantOrig, tc.wantFallback)
		}
	}
}
