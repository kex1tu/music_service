package radio_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/auth"
	featureRadio "music-manager/internal/features/radio"
	serviceRadio "music-manager/internal/services/radio"
	"music-manager/internal/testkit"
)

func setupRadioEnv(t *testing.T, streamToken string) (*testkit.TestEnv, *featureRadio.Module) {
	t.Helper()
	env := testkit.NewEnv(t)

	repo := featureRadio.NewRepository(env.DB.Conn())
	selector := serviceRadio.NewWaveSelector(env.DB, nil, nil)
	streamer := serviceRadio.NewIcyStreamer(selector, env.MusicSandbox, env.DB, streamToken)
	m3u := serviceRadio.NewM3UGenerator(selector, env.MusicSandbox, 50)

	svc := featureRadio.NewService(featureRadio.ServiceConfig{
		Selector:         selector,
		Streamer:         streamer,
		M3U:              m3u,
		Repo:             repo,
		RadioStreamToken: streamToken,
		SecretKey:        env.SecretKey,
		RadioWaveSize:    50,
	})

	mod := featureRadio.NewModule(svc)

	r := chi.NewRouter()
	authMiddleware := auth.Middleware(env.SecretKey)

	// Stream endpoints are public/token-protected, general radio API requires session
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		r.Route("/api/radio", func(r chi.Router) {
			r.Get("/now-playing", mod.GetNowPlaying)
			r.Get("/wave/queue", mod.GetWaveQueue)
			r.Get("/wave/stats", mod.GetWaveStats)
			r.Get("/genres", mod.GetGenres)
			r.Post("/m3u/generate", mod.GenerateM3U)
			r.Post("/navidrome/sync-radio", mod.SyncNavidromeRadio)
		})
	})

	r.Route("/api/radio/stream", func(r chi.Router) {
		r.Options("/*", mod.HandleStreamOptions)
		r.Get("/now-playing", mod.GetNowPlaying)
		r.Get("/wave", mod.StreamWave)
		r.Head("/wave", mod.StreamWave)
		r.Get("/wave.mp3", mod.StreamWave)
		r.Head("/wave.mp3", mod.StreamWave)
		r.Get("/genre/{name}", mod.StreamGenre)
		r.Head("/genre/{name}", mod.StreamGenre)
		r.Get("/artist/{name}", mod.StreamArtist)
		r.Head("/artist/{name}", mod.StreamArtist)
	})

	env.Mount(r)
	env.SetAuthUser("admin")

	return env, mod
}

func TestRadio_WaveStats(t *testing.T) {
	env, _ := setupRadioEnv(t, "secret-stream-token")

	// Вставляем жанр
	_, _ = env.DB.Conn().Exec("INSERT INTO genres (id, name) VALUES (1, 'Rock')")
	_, _ = env.DB.Conn().Exec("INSERT INTO artists (id, name, clean_name) VALUES (1, 'Queen', 'queen')")
	_, _ = env.DB.Conn().Exec("INSERT INTO albums (id, title, clean_title, artist_id, folder_path) VALUES (1, 'Bohemian', 'bohemian', 1, 'Queen/Bohemian')")
	_, _ = env.DB.Conn().Exec("INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, mtime) VALUES (1, 1, 1, 'Track 1', 'track 1', 'Queen/Bohemian/01.mp3', 1024, 123.0)")
	_, _ = env.DB.Conn().Exec("INSERT INTO track_genres (track_id, genre_id) VALUES (1, 1)")

	resp := env.Get("/api/radio/wave/stats")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var stats featureRadio.WaveStatsResponse
	if err := json.Unmarshal(env.ReadBody(resp), &stats); err != nil {
		t.Fatalf("failed to decode stats: %v", err)
	}

	if !stats.StreamTokenSet || stats.StreamToken != "secret-stream-token" {
		t.Errorf("unexpected stream token in stats: %+v", stats)
	}
	if len(stats.TopGenres) != 1 || stats.TopGenres[0] != "Rock" {
		t.Errorf("expected Rock in top genres, got: %+v", stats.TopGenres)
	}
}

func TestRadio_Genres(t *testing.T) {
	env, _ := setupRadioEnv(t, "")

	_, _ = env.DB.Conn().Exec("INSERT INTO genres (id, name) VALUES (1, 'Jazz')")
	_, _ = env.DB.Conn().Exec("INSERT INTO artists (id, name, clean_name) VALUES (1, 'Miles', 'miles')")
	_, _ = env.DB.Conn().Exec("INSERT INTO albums (id, title, clean_title, artist_id, folder_path) VALUES (1, 'Kind of Blue', 'kind of blue', 1, 'Miles/Blue')")
	_, _ = env.DB.Conn().Exec("INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, mtime) VALUES (1, 1, 1, 'So What', 'so what', 'Miles/Blue/01.mp3', 1024, 123.0)")
	_, _ = env.DB.Conn().Exec("INSERT INTO track_genres (track_id, genre_id) VALUES (1, 1)")

	resp := env.Get("/api/radio/genres?limit=10")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var gResp featureRadio.GenresResponse
	if err := json.Unmarshal(env.ReadBody(resp), &gResp); err != nil {
		t.Fatalf("failed to decode genres: %v", err)
	}

	if len(gResp.Genres) != 1 || gResp.Genres[0] != "Jazz" {
		t.Errorf("expected Jazz, got %+v", gResp.Genres)
	}
}

func TestRadio_NowPlaying(t *testing.T) {
	env, _ := setupRadioEnv(t, "")

	resp := env.Get("/api/radio/now-playing")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var np featureRadio.NowPlayingResponse
	if err := json.Unmarshal(env.ReadBody(resp), &np); err != nil {
		t.Fatalf("failed to decode now-playing: %v", err)
	}

	// Повторяем запрос к стрим-алиасу
	respStream := env.Get("/api/radio/stream/now-playing")
	if respStream.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from stream alias, got %d", respStream.StatusCode)
	}
}

func TestRadio_StreamAuth(t *testing.T) {
	streamToken := "my-auth-radio-token"
	env, _ := setupRadioEnv(t, streamToken)

	// Очищаем cookie администратора для проверки доступа к открытому стриму
	env.ClearAuth()

	// 1. Запрос без токена -> 401 Unauthorized
	respNoToken := env.Get("/api/radio/stream/wave")
	if respNoToken.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", respNoToken.StatusCode)
	}

	// 2. Запрос с токеном в Query ?token=...
	reqQuery := env.NewRequest("GET", "/api/radio/stream/wave?token="+streamToken, nil)
	// Добавляем dummy mp3 файл для стрима
	env.CreateDummyMP3("Queen/Bohemian/01.mp3", "Song", "Queen", "Bohemian")
	respQuery := env.Do(reqQuery)
	// Сервер либо начнет стриминг (200), либо 503 если библиотека пуста, но НЕ 401
	if respQuery.StatusCode == http.StatusUnauthorized {
		t.Fatalf("expected auth success with token query, got 401")
	}

	// 3. Запрос с токеном в Header X-Stream-Token
	reqHeader := env.NewRequest("GET", "/api/radio/stream/wave", nil)
	reqHeader.Header.Set("X-Stream-Token", streamToken)
	respHeader := env.Do(reqHeader)
	if respHeader.StatusCode == http.StatusUnauthorized {
		t.Fatalf("expected auth success with X-Stream-Token header, got 401")
	}

	// 4. Запрос с сессионной cookie администратора
	env.SetAuthUser("admin")
	reqCookie := env.NewRequest("GET", "/api/radio/stream/wave", nil)
	respCookie := env.Do(reqCookie)
	if respCookie.StatusCode == http.StatusUnauthorized {
		t.Fatalf("expected auth success with session cookie, got 401")
	}
}

func TestRadio_CORS_Options(t *testing.T) {
	env, _ := setupRadioEnv(t, "")

	req := env.NewRequest("OPTIONS", "/api/radio/stream/wave", nil)
	resp := env.Do(req)

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 No Content for OPTIONS, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected Access-Control-Allow-Origin: *")
	}
}

func TestRadio_GenerateM3U(t *testing.T) {
	env, _ := setupRadioEnv(t, "")

	// Создаем треки для генерации плейлиста
	env.CreateDummyMP3("Band/Album/01. Track.mp3", "Track 1", "Band", "Album")
	_, _ = env.DB.Conn().Exec("INSERT INTO artists (id, name, clean_name) VALUES (1, 'Band', 'band')")
	_, _ = env.DB.Conn().Exec("INSERT INTO albums (id, title, clean_title, artist_id, folder_path) VALUES (1, 'Album', 'album', 1, 'Band/Album')")
	_, _ = env.DB.Conn().Exec("INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, mtime, bitrate) VALUES (1, 1, 1, 'Track 1', 'track 1', 'Band/Album/01. Track.mp3', 1024, 123.0, 320)")

	resp := env.PostJSON("/api/radio/m3u/generate", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from m3u/generate, got %d: %s", resp.StatusCode, string(env.ReadBody(resp)))
	}

	var m3uResp featureRadio.GenerateM3UResponse
	if err := json.Unmarshal(env.ReadBody(resp), &m3uResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if m3uResp.Status != "ok" || m3uResp.PlaylistPath == "" {
		t.Errorf("unexpected m3u response: %+v", m3uResp)
	}
}

func TestRadio_SyncNavidrome_Unconfigured(t *testing.T) {
	env, _ := setupRadioEnv(t, "")

	reqBody := featureRadio.SyncRadioRequest{
		Name: "Моя Волна",
	}

	resp := env.PostJSON("/api/radio/navidrome/sync-radio", reqBody)
	// Должен вернуть 400 Bad Request, так как реквизиты Subsonic не настроены
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for unconfigured navidrome, got %d", resp.StatusCode)
	}
}
