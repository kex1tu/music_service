package resolver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveYandex_Playlist_FullPipeline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/users/test-curator/playlists/77":
			// Плейлист содержит 5 элементов:
			// 1: Track 101 ("Track A", Album Alpha)
			// 2: Track 102 ("Track B", Album Beta)
			// 3: Track 101 (Повтор Track A!)
			// 4: Track 103 (Без вложенного Track, требует догрузки через /tracks)
			// 5: Track 999 (Удаленный/несуществующий трек, не возвращается /tracks)
			w.Write([]byte(`{
				"result": {
					"uid": 123,
					"kind": 77,
					"title": "Мой Супер Плейлист",
					"trackCount": 5,
					"owner": {
						"login": "test-curator",
						"name": "Куратор Плейлистов"
					},
					"cover": {
						"uri": "avatars.yandex.net/get-music-content/0000/playlist-cov-%%"
					},
					"tracks": [
						{
							"id": 101,
							"albumId": 201,
							"track": {
								"id": 101,
								"title": "Track A",
								"durationMs": 180000,
								"artists": [{"name": "Artist One"}],
								"albums": [{"title": "Album Alpha", "year": 2021, "genre": "electronic", "coverUri": "cov-alpha-%%"}]
							}
						},
						{
							"id": 102,
							"albumId": 202,
							"track": {
								"id": 102,
								"title": "Track B",
								"durationMs": 210000,
								"artists": [{"name": "Artist Two"}],
								"albums": [{"title": "Album Beta", "year": 2022, "genre": "rock", "coverUri": "cov-beta-%%"}]
							}
						},
						{
							"id": 101,
							"albumId": 201,
							"track": {
								"id": 101,
								"title": "Track A",
								"durationMs": 180000,
								"artists": [{"name": "Artist One"}],
								"albums": [{"title": "Album Alpha", "year": 2021, "genre": "electronic", "coverUri": "cov-alpha-%%"}]
							}
						},
						{
							"id": 103,
							"albumId": 203
						},
						{
							"id": 999,
							"albumId": 999
						}
					]
				}
			}`))

		case "/tracks":
			_ = r.ParseForm()
			tids := r.Form.Get("track-ids")
			// Возвращаем метаданные только для 103 (999 недоступен/удален)
			if strings.Contains(tids, "103") {
				w.Write([]byte(`{
					"result": [
						{
							"id": 103,
							"title": "Track C (Hydrated)",
							"durationMs": 240000,
							"artists": [{"name": "Artist Three"}],
							"albums": [{"title": "Album Gamma", "year": 2023, "genre": "ambient", "coverUri": "cov-gamma-%%"}]
						}
					]
				}`))
			} else {
				w.Write([]byte(`{"result": []}`))
			}

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	yaClient := NewYandexMusicClient("")
	yaClient.SetBaseURL(server.URL)

	res := NewSourceResolver(nil, nil, "")
	res.SetYandexClient(yaClient)

	ctx := context.Background()
	meta, tracks, err := res.Resolve(ctx, "https://music.yandex.ru/users/test-curator/playlists/77", "auto")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// 1. Проверка метаданных релиза
	if meta == nil {
		t.Fatal("ReleaseMetadata is nil")
	}
	if meta.AlbumTitle != "Мой Супер Плейлист" {
		t.Errorf("meta.AlbumTitle = %q, want 'Мой Супер Плейлист'", meta.AlbumTitle)
	}
	if meta.AlbumArtist != "Куратор Плейлистов" {
		t.Errorf("meta.AlbumArtist = %q, want 'Куратор Плейлистов'", meta.AlbumArtist)
	}
	if !meta.IsPlaylist {
		t.Error("meta.IsPlaylist should be true")
	}
	if meta.TotalTracks != 5 {
		t.Errorf("meta.TotalTracks = %d, want 5", meta.TotalTracks)
	}
	if meta.FetchedTracks != 4 {
		t.Errorf("meta.FetchedTracks = %d, want 4", meta.FetchedTracks)
	}
	if meta.SkippedTracks != 1 {
		t.Errorf("meta.SkippedTracks = %d, want 1 (track 999)", meta.SkippedTracks)
	}

	// 2. Проверка полученных треков
	if len(tracks) != 4 {
		t.Fatalf("len(tracks) = %d, want 4", len(tracks))
	}

	// Трек 1 (Track A, pos 1)
	tr1 := tracks[0]
	if tr1.TrackNumber != 1 || tr1.TrackID != "ya_101_1" || tr1.CatalogTrackID != "ya_101" {
		t.Errorf("tr1: number=%d, trackID=%q, catalogID=%q", tr1.TrackNumber, tr1.TrackID, tr1.CatalogTrackID)
	}
	if tr1.Title != "Track A" || tr1.Artist != "Artist One" {
		t.Errorf("tr1 title/artist mismatch: %q / %q", tr1.Title, tr1.Artist)
	}
	if tr1.Album != "Album Alpha" {
		t.Errorf("tr1.Album = %q, want 'Album Alpha' (must NOT be playlist title!)", tr1.Album)
	}

	// Трек 2 (Track B, pos 2)
	tr2 := tracks[1]
	if tr2.TrackNumber != 2 || tr2.TrackID != "ya_102_2" || tr2.CatalogTrackID != "ya_102" {
		t.Errorf("tr2: number=%d, trackID=%q, catalogID=%q", tr2.TrackNumber, tr2.TrackID, tr2.CatalogTrackID)
	}
	if tr2.Album != "Album Beta" {
		t.Errorf("tr2.Album = %q, want 'Album Beta'", tr2.Album)
	}

	// Трек 3 (Повтор Track A на позиции 3)
	tr3 := tracks[2]
	if tr3.TrackNumber != 3 || tr3.TrackID != "ya_101_3" || tr3.CatalogTrackID != "ya_101" {
		t.Errorf("tr3: number=%d, trackID=%q, catalogID=%q", tr3.TrackNumber, tr3.TrackID, tr3.CatalogTrackID)
	}
	if tr3.TrackID == tr1.TrackID {
		t.Errorf("Duplicate track has identical TrackID (%q == %q), queue keys must be distinct!", tr3.TrackID, tr1.TrackID)
	}
	if tr3.CatalogTrackID != tr1.CatalogTrackID {
		t.Errorf("Duplicate track must share identical CatalogTrackID: %q != %q", tr3.CatalogTrackID, tr1.CatalogTrackID)
	}
	if tr3.Album != "Album Alpha" {
		t.Errorf("tr3.Album = %q, want 'Album Alpha'", tr3.Album)
	}

	// Трек 4 (Track C, успешно догруженный через /tracks)
	tr4 := tracks[3]
	if tr4.TrackNumber != 4 || tr4.TrackID != "ya_103_4" || tr4.CatalogTrackID != "ya_103" {
		t.Errorf("tr4: number=%d, trackID=%q, catalogID=%q", tr4.TrackNumber, tr4.TrackID, tr4.CatalogTrackID)
	}
	if tr4.Title != "Track C (Hydrated)" || tr4.Album != "Album Gamma" {
		t.Errorf("tr4 hydrated mismatch: title=%q, album=%q", tr4.Title, tr4.Album)
	}

	// Все 4 трека должны сохранять свои собственные альбомы, а не заголовок плейлиста!
	for i, tr := range tracks {
		if tr.Album == "Мой Супер Плейлист" {
			t.Errorf("track %d has album name equal to playlist name: %q", i+1, tr.Album)
		}
	}
}

func TestResolveYandex_Playlist_IframeAndUUID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/users/shared-user/playlists/99":
			w.Write([]byte(`{
				"result": {
					"kind": 99,
					"title": "Iframe Shared Playlist",
					"trackCount": 1,
					"owner": {"login": "shared-user"},
					"tracks": [
						{"id": 1, "track": {"id": 1, "title": "Track 1", "durationMs": 180000, "albums": [{"title": "Album 1"}]}}
					]
				}
			}`))
		case "/playlist/11112222-3333-4444-5555-666677778888":
			w.Write([]byte(`{
				"result": {
					"playlistUuid": "11112222-3333-4444-5555-666677778888",
					"title": "UUID Direct Playlist",
					"trackCount": 1,
					"owner": {"name": "UUID Author"},
					"tracks": [
						{"id": 2, "track": {"id": 2, "title": "Track 2", "durationMs": 200000, "albums": [{"title": "Album 2"}]}}
					]
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	yaClient := NewYandexMusicClient("")
	yaClient.SetBaseURL(server.URL)

	res := NewSourceResolver(nil, nil, "")
	res.SetYandexClient(yaClient)

	ctx := context.Background()

	// 1. Iframe код в Resolve() с auto-детектированием
	iframeHTML := `<iframe frameborder="0" style="border:none;width:100%;height:450px;" width="100%" height="450" src="https://music.yandex.ru/iframe/#playlist/shared-user/99"></iframe>`
	metaIframe, tracksIframe, err := res.Resolve(ctx, iframeHTML, "auto")
	if err != nil {
		t.Fatalf("Resolve with iframe failed: %v", err)
	}
	if metaIframe.AlbumTitle != "Iframe Shared Playlist" || len(tracksIframe) != 1 {
		t.Errorf("Unexpected iframe resolve result: title=%q, tracks=%d", metaIframe.AlbumTitle, len(tracksIframe))
	}

	// 2. UUID ссылка в Resolve()
	uuidURL := "https://music.yandex.ru/playlists/11112222-3333-4444-5555-666677778888"
	metaUUID, tracksUUID, err := res.Resolve(ctx, uuidURL, "yandex")
	if err != nil {
		t.Fatalf("Resolve with UUID failed: %v", err)
	}
	if metaUUID.AlbumTitle != "UUID Direct Playlist" || len(tracksUUID) != 1 {
		t.Errorf("Unexpected UUID resolve result: title=%q, tracks=%d", metaUUID.AlbumTitle, len(tracksUUID))
	}
}
