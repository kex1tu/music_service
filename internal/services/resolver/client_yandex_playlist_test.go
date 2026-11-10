package resolver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestYandexMusicClient_ParseResource_Comprehensive(t *testing.T) {
	client := NewYandexMusicClient("")

	tests := []struct {
		name         string
		url          string
		wantType     string
		wantID       int64
		wantOwner    string
		wantKind     int64
		wantUUID     string
		wantErr      bool
		errSubstring string
	}{
		// 1. Стандартные owner/kind URL
		{
			name:      "owner/kind playlist standard ru",
			url:       "https://music.yandex.ru/users/yamusic-top/playlists/1076",
			wantType:  "playlist",
			wantOwner: "yamusic-top",
			wantKind:  1076,
		},
		{
			name:      "owner/kind playlist com domain",
			url:       "https://music.yandex.com/users/someone/playlists/3",
			wantType:  "playlist",
			wantOwner: "someone",
			wantKind:  3,
		},
		{
			name:      "owner/kind playlist kz domain with trailing slash",
			url:       "https://music.yandex.kz/users/kz-user/playlists/5/",
			wantType:  "playlist",
			wantOwner: "kz-user",
			wantKind:  5,
		},
		{
			name:      "owner/kind playlist by domain with query params",
			url:       "https://music.yandex.by/users/by_user/playlists/10?lang=ru",
			wantType:  "playlist",
			wantOwner: "by_user",
			wantKind:  10,
		},
		{
			name:      "owner/kind playlist uz domain",
			url:       "https://music.yandex.uz/users/uz_user/playlists/42",
			wantType:  "playlist",
			wantOwner: "uz_user",
			wantKind:  42,
		},

		// 2. UUID плейлисты
		{
			name:     "uuid playlist with plural playlists",
			url:      "https://music.yandex.ru/playlists/799426f4-b236-4d1d-9351-872f9be7aa8b",
			wantType: "playlist",
			wantUUID: "799426f4-b236-4d1d-9351-872f9be7aa8b",
		},
		{
			name:     "uuid playlist with singular playlist",
			url:      "https://music.yandex.ru/playlist/799426f4-b236-4d1d-9351-872f9be7aa8b",
			wantType: "playlist",
			wantUUID: "799426f4-b236-4d1d-9351-872f9be7aa8b",
		},
		{
			name:     "uuid playlist com domain uppercase UUID",
			url:      "https://music.yandex.com/playlists/799426F4-B236-4D1D-9351-872F9BE7AA8B",
			wantType: "playlist",
			wantUUID: "799426F4-B236-4D1D-9351-872F9BE7AA8B",
		},

		// 3. Iframe коды и iframe URL
		{
			name:      "iframe html with fragment owner/kind",
			url:       `<iframe frameborder="0" style="border:none;width:100%;height:450px;" width="100%" height="450" src="https://music.yandex.ru/iframe/#playlist/yamusic-top/1076"></iframe>`,
			wantType:  "playlist",
			wantOwner: "yamusic-top",
			wantKind:  1076,
		},
		{
			name:     "iframe html with fragment uuid",
			url:      `<iframe src="https://music.yandex.ru/iframe/#playlist/799426f4-b236-4d1d-9351-872f9be7aa8b"></iframe>`,
			wantType: "playlist",
			wantUUID: "799426f4-b236-4d1d-9351-872f9be7aa8b",
		},
		{
			name:      "iframe html without hash owner/kind",
			url:       `<iframe src="https://music.yandex.ru/iframe/playlist/yamusic-top/1076"></iframe>`,
			wantType:  "playlist",
			wantOwner: "yamusic-top",
			wantKind:  1076,
		},
		{
			name:     "iframe html without hash uuid",
			url:      `<iframe src="https://music.yandex.ru/iframe/playlist/799426f4-b236-4d1d-9351-872f9be7aa8b"></iframe>`,
			wantType: "playlist",
			wantUUID: "799426f4-b236-4d1d-9351-872f9be7aa8b",
		},
		{
			name:      "iframe html with protocol relative url",
			url:       `<iframe src="//music.yandex.ru/iframe/#playlist/yamusic-top/1076"></iframe>`,
			wantType:  "playlist",
			wantOwner: "yamusic-top",
			wantKind:  1076,
		},
		{
			name:      "direct iframe URL without tag",
			url:       "https://music.yandex.ru/iframe/#playlist/yamusic-top/1076",
			wantType:  "playlist",
			wantOwner: "yamusic-top",
			wantKind:  1076,
		},

		// 4. Существующие типы ресурсов (обратная совместимость)
		{
			name:     "album standard",
			url:      "https://music.yandex.ru/album/3087311",
			wantType: "album",
			wantID:   3087311,
		},
		{
			name:     "album with track context",
			url:      "https://music.yandex.ru/album/3087311/track/26078311",
			wantType: "album",
			wantID:   3087311,
		},
		{
			name:     "track direct",
			url:      "https://music.yandex.ru/track/26078311",
			wantType: "track",
			wantID:   26078311,
		},
		{
			name:     "artist direct",
			url:      "https://music.yandex.ru/artist/249991",
			wantType: "artist",
			wantID:   249991,
		},
		{
			name:     "iframe album",
			url:      `<iframe src="https://music.yandex.ru/iframe/#album/3087311"></iframe>`,
			wantType: "album",
			wantID:   3087311,
		},
		{
			name:     "iframe track",
			url:      `<iframe src="https://music.yandex.ru/iframe/#track/26078311"></iframe>`,
			wantType: "track",
			wantID:   26078311,
		},

		// 5. Чужие и недопустимые домены (безопасность)
		{
			name:         "evil yandex domain",
			url:          "https://evil-yandex.com/users/foo/playlists/1",
			wantErr:      true,
			errSubstring: "не принадлежит белому списку",
		},
		{
			name:         "notyandex domain",
			url:          "https://notyandex.ru/playlists/799426f4-b236-4d1d-9351-872f9be7aa8b",
			wantErr:      true,
			errSubstring: "не принадлежит белому списку",
		},
		{
			name:         "javascript in iframe",
			url:          `<iframe src="javascript:alert(1)"></iframe>`,
			wantErr:      true,
			errSubstring: "empty URL",
		},
		{
			name:         "empty input",
			url:          "   ",
			wantErr:      true,
			errSubstring: "empty URL",
		},
		{
			name:         "non-resource yandex path",
			url:          "https://music.yandex.ru/settings",
			wantErr:      true,
			errSubstring: "не удалось извлечь ресурс",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := client.ParseResource(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseResource(%q) error = %v, wantErr = %v", tt.url, err, tt.wantErr)
			}
			if tt.wantErr {
				if tt.errSubstring != "" && !strings.Contains(err.Error(), tt.errSubstring) {
					t.Errorf("ParseResource(%q) error = %q, want substring %q", tt.url, err.Error(), tt.errSubstring)
				}
				return
			}
			if ref.Type != tt.wantType {
				t.Errorf("ref.Type = %q, want %q", ref.Type, tt.wantType)
			}
			if tt.wantID > 0 && ref.ID != tt.wantID {
				t.Errorf("ref.ID = %d, want %d", ref.ID, tt.wantID)
			}
			if tt.wantOwner != "" && ref.Owner != tt.wantOwner {
				t.Errorf("ref.Owner = %q, want %q", ref.Owner, tt.wantOwner)
			}
			if tt.wantKind > 0 && ref.Kind != tt.wantKind {
				t.Errorf("ref.Kind = %d, want %d", ref.Kind, tt.wantKind)
			}
			if tt.wantUUID != "" && !strings.EqualFold(ref.PlaylistUUID, tt.wantUUID) {
				t.Errorf("ref.PlaylistUUID = %q, want %q", ref.PlaylistUUID, tt.wantUUID)
			}
		})
	}
}

func TestYandexMusicClient_GetPlaylist_PaginationAndErrors(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasPrefix(r.URL.Path, "/users/yamusic-top/playlists/1076"):
			page := r.URL.Query().Get("page")
			if page == "" || page == "0" {
				// Страница 0: возвращает 2 трека и pager с total=4
				w.Write([]byte(`{
					"result": {
						"uid": 12345,
						"kind": 1076,
						"title": "Чарт Яндекс Музыки",
						"trackCount": 4,
						"cover": {
							"uri": "avatars.yandex.net/get-music-content/1111/chart-%%"
						},
						"owner": {
							"login": "yamusic-top",
							"name": "Яндекс Музыка"
						},
						"pager": {
							"page": 0,
							"perPage": 2,
							"total": 4
						},
						"tracks": [
							{
								"id": 101,
								"albumId": 201,
								"track": {
									"id": 101,
									"title": "Трек 1",
									"durationMs": 180000,
									"artists": [{"name": "Артист 1"}],
									"albums": [{"title": "Альбом 1", "year": 2023, "genre": "pop"}]
								}
							},
							{
								"id": 102,
								"albumId": 202,
								"track": {
									"id": 102,
									"title": "Трек 2",
									"durationMs": 200000,
									"artists": [{"name": "Артист 2"}],
									"albums": [{"title": "Альбом 2", "year": 2022, "genre": "rock"}]
								}
							}
						]
					}
				}`))
			} else if page == "1" {
				// Страница 1: следующие 2 трека
				w.Write([]byte(`{
					"result": {
						"tracks": [
							{
								"id": 103,
								"albumId": 203,
								"track": {
									"id": 103,
									"title": "Трек 3",
									"durationMs": 210000,
									"artists": [{"name": "Артист 3"}],
									"albums": [{"title": "Альбом 3", "year": 2024, "genre": "electronic"}]
								}
							},
							{
								"id": 104,
								"albumId": 204,
								"track": {
									"id": 104,
									"title": "Трек 4",
									"durationMs": 190000,
									"artists": [{"name": "Артист 4"}],
									"albums": [{"title": "Альбом 4", "year": 2021, "genre": "hip-hop"}]
								}
							}
						]
					}
				}`))
			} else {
				w.Write([]byte(`{"result": {"tracks": []}}`))
			}

		case r.URL.Path == "/users/private-user/playlists/1":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error": "forbidden"}`))

		case r.URL.Path == "/users/notfound-user/playlists/999":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error": "not found"}`))

		case r.URL.Path == "/users/rate-limited/playlists/1":
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error": "rate limit"}`))

		case r.URL.Path == "/users/malformed/playlists/1":
			w.Write([]byte(`{malformed json}`))

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewYandexMusicClient("")
	client.SetBaseURL(server.URL)
	ctx := context.Background()

	// 1. Успешный запрос с пагинацией (страница 0 + страница 1 = 4 трека)
	playlist, err := client.GetPlaylist(ctx, "yamusic-top", 1076)
	if err != nil {
		t.Fatalf("GetPlaylist failed: %v", err)
	}
	if playlist.Title != "Чарт Яндекс Музыки" {
		t.Errorf("Title = %q, want 'Чарт Яндекс Музыки'", playlist.Title)
	}
	if len(playlist.Tracks) != 4 {
		t.Fatalf("len(playlist.Tracks) = %d, want 4 after pagination", len(playlist.Tracks))
	}
	if playlist.Tracks[0].Track.Title != "Трек 1" || playlist.Tracks[3].Track.Title != "Трек 4" {
		t.Errorf("Unexpected track titles: %q, %q", playlist.Tracks[0].Track.Title, playlist.Tracks[3].Track.Title)
	}

	// 2. Ошибка 403 Forbidden (приватный плейлист)
	_, err = client.GetPlaylist(ctx, "private-user", 1)
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("Expected HTTP 403 error, got %v", err)
	}

	// 3. Ошибка 404 Not Found
	_, err = client.GetPlaylist(ctx, "notfound-user", 999)
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("Expected HTTP 404 error, got %v", err)
	}

	// 4. Ошибка 429 Too Many Requests
	_, err = client.GetPlaylist(ctx, "rate-limited", 1)
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Errorf("Expected HTTP 429 error, got %v", err)
	}

	// 5. Ошибка парсинга malformed JSON
	_, err = client.GetPlaylist(ctx, "malformed", 1)
	if err == nil || !strings.Contains(err.Error(), "ошибка парсинга JSON") {
		t.Errorf("Expected malformed JSON error, got %v", err)
	}
}

func TestYandexMusicClient_GetPlaylistByUUID_AndFallback(t *testing.T) {
	calledPaths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calledPaths = append(calledPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/playlist/799426f4-b236-4d1d-9351-872f9be7aa8b":
			// Успешный первичный эндпоинт /playlist/{uuid}
			w.Write([]byte(`{
				"result": {
					"playlistUuid": "799426f4-b236-4d1d-9351-872f9be7aa8b",
					"title": "UUID Playlist",
					"trackCount": 1,
					"owner": {"name": "Curator"},
					"tracks": [
						{
							"id": 555,
							"track": {
								"id": 555,
								"title": "Single UUID Track",
								"durationMs": 150000,
								"artists": [{"name": "UUID Artist"}],
								"albums": [{"title": "UUID Album", "year": 2025}]
							}
						}
					]
				}
			}`))

		case "/playlist/fallback-uuid-12345678":
			// Первичный вернул 404, fallback на /playlists/...
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error": "not found on /playlist"}`))

		case "/playlists/fallback-uuid-12345678":
			// Успешный резервный эндпоинт
			w.Write([]byte(`{
				"result": {
					"playlistUuid": "fallback-uuid-12345678",
					"title": "Fallback Playlists Plural",
					"trackCount": 1,
					"tracks": [
						{"id": 777, "track": {"id": 777, "title": "Fallback Track", "durationMs": 160000}}
					]
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

	// 1. Первичный вызов
	pl, err := client.GetPlaylistByUUID(ctx, "799426f4-b236-4d1d-9351-872f9be7aa8b")
	if err != nil {
		t.Fatalf("GetPlaylistByUUID failed: %v", err)
	}
	if pl.Title != "UUID Playlist" {
		t.Errorf("Title = %q, want 'UUID Playlist'", pl.Title)
	}

	// 2. Резервный вызов через /playlists/... при 404 на /playlist/...
	pl2, err := client.GetPlaylistByUUID(ctx, "fallback-uuid-12345678")
	if err != nil {
		t.Fatalf("GetPlaylistByUUID fallback failed: %v", err)
	}
	if pl2.Title != "Fallback Playlists Plural" {
		t.Errorf("Title = %q, want 'Fallback Playlists Plural'", pl2.Title)
	}

	// 3. Отмена контекста (Context cancellation)
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel() // Отменяем сразу
	_, err = client.GetPlaylistByUUID(cancelCtx, "799426f4-b236-4d1d-9351-872f9be7aa8b")
	if err == nil {
		t.Error("Expected error on cancelled context, got nil")
	}
}

func TestYandexMusicClient_GetTracks_BatchHydration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/tracks" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		trackIDs := r.Form.Get("track-ids")
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(trackIDs, "101") && strings.Contains(trackIDs, "102") {
			w.Write([]byte(`{
				"result": [
					{
						"id": 101,
						"title": "Hydrated Track 1",
						"durationMs": 180000,
						"artists": [{"name": "Artist 1"}],
						"albums": [{"title": "Album 1", "year": 2023, "coverUri": "cov1-%%"}]
					},
					{
						"id": 102,
						"title": "Hydrated Track 2",
						"durationMs": 210000,
						"artists": [{"name": "Artist 2"}],
						"albums": [{"title": "Album 2", "year": 2024, "coverUri": "cov2-%%"}]
					}
				]
			}`))
			return
		}

		w.Write([]byte(`{"result": []}`))
	}))
	defer server.Close()

	client := NewYandexMusicClient("")
	client.SetBaseURL(server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tracks, err := client.GetTracks(ctx, []string{"101:201", "102:202"})
	if err != nil {
		t.Fatalf("GetTracks failed: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("len(tracks) = %d, want 2", len(tracks))
	}
	if tracks[0].Title != "Hydrated Track 1" || tracks[1].Title != "Hydrated Track 2" {
		t.Errorf("Unexpected track titles: %q, %q", tracks[0].Title, tracks[1].Title)
	}
}
