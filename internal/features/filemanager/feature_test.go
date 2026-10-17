package filemanager_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/auth"
	"music-manager/internal/features/filemanager"
	"music-manager/internal/testkit"
)

type mockOrganizer struct {
	renamed []string
}

func (m *mockOrganizer) RenameFile(oldRel, newRel string) ([]string, error) {
	m.renamed = append(m.renamed, newRel)
	return []string{newRel}, nil
}

func (m *mockOrganizer) SyncCompanions(oldBaseRel, newBaseRel string) error {
	return nil
}

func setupFileManagerEnv(t *testing.T) (*testkit.TestEnv, *filemanager.Module) {
	t.Helper()
	env := testkit.NewEnv(t)

	repo := filemanager.NewRepository(env.DB.Conn())
	svc := filemanager.NewService(filemanager.ServiceConfig{
		MusicSandbox: env.MusicSandbox,
		TmpSandbox:   env.TmpSandbox,
		Repo:         repo,
	})

	mod := filemanager.NewModule(svc)
	r := chi.NewRouter()
	authMiddleware := auth.Middleware(env.SecretKey)
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		mod.RegisterRoutes(r)
	})

	env.Mount(r)
	env.SetAuthUser("admin")

	return env, mod
}

func TestFileManager_ListFiles(t *testing.T) {
	env, _ := setupFileManagerEnv(t)

	// Создаем тестовую структуру директорий и MP3
	env.CreateDummyMP3("Album1/01. Track.mp3", "Track 1", "Artist 1", "Album 1")
	env.CreateDummyMP3("Album1/02. Track.mp3", "Track 2", "Artist 1", "Album 1")

	resp := env.Get("/files?path=Album1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var items []filemanager.FileItem
	body := env.ReadBody(resp)
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("failed to decode json: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Title != "Track 1" || items[1].Title != "Track 2" {
		t.Errorf("unexpected track titles: %+v", items)
	}
}

func TestFileManager_StreamAudio_RFC7233Range(t *testing.T) {
	env, _ := setupFileManagerEnv(t)

	dummyPath := env.CreateDummyMP3("TestStream/song.mp3", "StreamSong", "Artist", "Album")
	fi, _ := os.Stat(dummyPath)

	req := env.NewRequest("GET", "/files/stream?path=TestStream/song.mp3", nil)
	req.Header.Set("Range", "bytes=0-99")

	resp := env.Do(req)

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("expected 206 Partial Content, got %d", resp.StatusCode)
	}

	cr := resp.Header.Get("Content-Range")
	expectedPrefix := "bytes 0-99/"
	if !strings.HasPrefix(cr, expectedPrefix) {
		t.Errorf("expected Content-Range starting with %q, got %q", expectedPrefix, cr)
	}

	body := env.ReadBody(resp)
	if len(body) != 100 {
		t.Errorf("expected 100 bytes, got %d (file size: %d)", len(body), fi.Size())
	}
}

func TestFileManager_StreamTrack_AndMetadata(t *testing.T) {
	env, _ := setupFileManagerEnv(t)

	env.CreateDummyMP3("SingleAlbum/01. Song.mp3", "Awesome Title", "Cool Artist", "SingleAlbum")

	// Индексируем в базу SQLite
	_, _ = env.DB.Conn().Exec("INSERT INTO artists (id, name, clean_name) VALUES (1, 'Cool Artist', 'cool artist')")
	_, _ = env.DB.Conn().Exec("INSERT INTO albums (id, title, clean_title, artist_id, folder_path) VALUES (1, 'SingleAlbum', 'singlealbum', 1, 'SingleAlbum')")
	_, err := env.DB.Conn().Exec(`
		INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, bitrate, duration, file_hash, mtime)
		VALUES (42, 1, 1, 'Awesome Title', 'awesome title', 'SingleAlbum/01. Song.mp3', 2048, 320, 180.0, 'aabbcc1122334455', 123456.0)
	`)
	if err != nil {
		t.Fatalf("failed to insert track: %v", err)
	}

	// 1. Проверяем метаданные по числовому ID
	respMeta := env.Get("/api/tracks/42/metadata")
	if respMeta.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", respMeta.StatusCode)
	}
	var meta filemanager.TrackMetadataResponse
	if err := json.Unmarshal(env.ReadBody(respMeta), &meta); err != nil {
		t.Fatalf("failed to unmarshal metadata: %v", err)
	}
	if meta.Title != "Awesome Title" || meta.Artist != "Cool Artist" {
		t.Errorf("unexpected metadata: %+v", meta)
	}

	// 2. Проверяем стриминг по хэшу
	reqStream := env.NewRequest("GET", "/api/tracks/aabbcc1122334455/stream", nil)
	reqStream.Header.Set("Range", "bytes=0-50")
	respStream := env.Do(reqStream)

	if respStream.StatusCode != http.StatusPartialContent {
		t.Fatalf("expected 206 Partial Content, got %d", respStream.StatusCode)
	}
	if len(env.ReadBody(respStream)) != 51 {
		t.Errorf("expected 51 bytes for bytes=0-50 range")
	}

	// 3. Проверяем xHTTP чанки алиас
	respChunk := env.Get("/api/library/stream/42/chunks/part1")
	if respChunk.StatusCode != http.StatusOK && respChunk.StatusCode != http.StatusPartialContent {
		t.Fatalf("expected 200 or 206 from chunk alias, got %d", respChunk.StatusCode)
	}
}

func TestFileManager_RenameFile_AndCompanion(t *testing.T) {
	env, _ := setupFileManagerEnv(t)

	env.CreateDummyMP3("RenameAlbum/01. OldTrack.mp3", "OldTrack", "Artist", "RenameAlbum")

	// Создаем сопутствующий текст песни .lrc
	lrcAbs, _ := env.MusicSandbox.Resolve("RenameAlbum/01. OldTrack.lrc", false)
	_ = os.WriteFile(lrcAbs, []byte("[00:01.00] Test Lyrics"), 0644)

	// Индексируем в базу
	_, _ = env.DB.Conn().Exec("INSERT INTO artists (id, name, clean_name) VALUES (1, 'Artist', 'artist')")
	_, _ = env.DB.Conn().Exec("INSERT INTO albums (id, title, clean_title, artist_id, folder_path) VALUES (1, 'RenameAlbum', 'renamealbum', 1, 'RenameAlbum')")
	_, err := env.DB.Conn().Exec("INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, mtime) VALUES (10, 1, 1, 'OldTrack', 'oldtrack', 'RenameAlbum/01. OldTrack.mp3', 2048, 123456.0)")
	if err != nil {
		t.Fatalf("failed to insert track: %v", err)
	}

	reqBody := filemanager.RenameRequest{
		OldPath: "RenameAlbum/01. OldTrack.mp3",
		NewPath: "RenameAlbum/01. NewTrack.mp3",
	}

	resp := env.PostJSON("/files/rename", reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", resp.StatusCode, string(env.ReadBody(resp)))
	}

	// Проверяем, что исходного файла нет, а новый есть
	oldAbs, _ := env.MusicSandbox.Resolve("RenameAlbum/01. OldTrack.mp3", false)
	newAbs, _ := env.MusicSandbox.Resolve("RenameAlbum/01. NewTrack.mp3", false)
	if _, err := os.Stat(oldAbs); !os.IsNotExist(err) {
		t.Errorf("old file still exists")
	}
	if _, err := os.Stat(newAbs); err != nil {
		t.Errorf("new file does not exist: %v", err)
	}

	// Проверяем, что .lrc переместился
	newLrcAbs, _ := env.MusicSandbox.Resolve("RenameAlbum/01. NewTrack.lrc", false)
	if _, err := os.Stat(newLrcAbs); err != nil {
		t.Errorf("companion .lrc was not moved: %v", err)
	}

	// Проверяем обновление записи в SQLite
	var dbPath string
	_ = env.DB.Conn().QueryRow("SELECT file_path FROM tracks WHERE id = 10").Scan(&dbPath)
	if dbPath != "RenameAlbum/01. NewTrack.mp3" {
		t.Errorf("expected db file_path 'RenameAlbum/01. NewTrack.mp3', got %q", dbPath)
	}
}

func TestFileManager_DeleteFile_AndCleanup(t *testing.T) {
	env, _ := setupFileManagerEnv(t)

	env.CreateDummyMP3("DeleteAlbum/01. Track.mp3", "Track", "Artist", "DeleteAlbum")
	lrcAbs, _ := env.MusicSandbox.Resolve("DeleteAlbum/01. Track.lrc", false)
	_ = os.WriteFile(lrcAbs, []byte("[00:01.00] Delete me"), 0644)

	// Запись в SQLite
	_, _ = env.DB.Conn().Exec("INSERT INTO artists (id, name, clean_name) VALUES (2, 'Artist', 'artist')")
	_, _ = env.DB.Conn().Exec("INSERT INTO albums (id, title, clean_title, artist_id, folder_path) VALUES (2, 'DeleteAlbum', 'deletealbum', 2, 'DeleteAlbum')")
	_, err := env.DB.Conn().Exec("INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, mtime) VALUES (20, 2, 2, 'Track', 'track', 'DeleteAlbum/01. Track.mp3', 2048, 123456.0)")
	if err != nil {
		t.Fatalf("failed to insert track: %v", err)
	}

	req := env.NewRequest("DELETE", "/files?path="+url.QueryEscape("DeleteAlbum/01. Track.mp3"), nil)
	resp := env.Do(req)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Проверяем удаление из базы
	var count int
	_ = env.DB.Conn().QueryRow("SELECT COUNT(*) FROM tracks WHERE id = 20").Scan(&count)
	if count != 0 {
		t.Errorf("track still exists in db")
	}

	// Проверяем удаление .lrc
	if _, err := os.Stat(lrcAbs); !os.IsNotExist(err) {
		t.Errorf("companion .lrc was not deleted")
	}
}

func TestFileManager_PathTraversal_Security(t *testing.T) {
	env, _ := setupFileManagerEnv(t)

	badPaths := []string{
		"../../etc/passwd",
		"../../../root/.bashrc",
		"/etc/shadow",
		"folder/../../outside.mp3",
		"folder/safe.mp3%00.exe",
	}

	for _, bad := range badPaths {
		t.Run("List_"+bad, func(t *testing.T) {
			resp := env.Get("/files?path=" + bad)
			if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
				t.Errorf("expected 403, 400 or 404 for path %q, got %d", bad, resp.StatusCode)
			}
		})

		t.Run("Stream_"+bad, func(t *testing.T) {
			resp := env.Get("/files/stream?path=" + bad)
			if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
				t.Errorf("expected 403, 400 or 404 for stream %q, got %d", bad, resp.StatusCode)
			}
		})
	}
}

func TestFileManager_SearchLibrary(t *testing.T) {
	env, _ := setupFileManagerEnv(t)

	_, _ = env.DB.Conn().Exec("INSERT INTO artists (id, name, clean_name) VALUES (10, 'SearchArtist', 'searchartist')")
	_, _ = env.DB.Conn().Exec("INSERT INTO albums (id, title, clean_title, artist_id, folder_path) VALUES (10, 'SearchAlbum', 'searchalbum', 10, 'SearchAlbum')")
	_, err := env.DB.Conn().Exec("INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, mtime) VALUES (100, 10, 10, 'SearchTrack', 'searchtrack', 'SearchAlbum/01. SearchTrack.mp3', 2048, 123456.0)")
	if err != nil {
		t.Fatalf("failed to insert search track: %v", err)
	}

	resp := env.Get("/files/search?q=Search")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var res filemanager.SearchLibraryResult
	if err := json.Unmarshal(env.ReadBody(resp), &res); err != nil {
		t.Fatalf("failed to decode search result: %v", err)
	}

	if len(res.Tracks) != 1 || res.Tracks[0].Title != "SearchTrack" {
		t.Errorf("expected 1 track, got %+v", res.Tracks)
	}
	if len(res.Albums) != 1 || res.Albums[0].Title != "SearchAlbum" {
		t.Errorf("expected 1 album, got %+v", res.Albums)
	}
	if len(res.Artists) != 1 || res.Artists[0].Name != "SearchArtist" {
		t.Errorf("expected 1 artist, got %+v", res.Artists)
	}
}

func TestFileManager_ListLibraryTracks(t *testing.T) {
	env, _ := setupFileManagerEnv(t)

	_, _ = env.DB.Conn().Exec("INSERT INTO artists (id, name, clean_name) VALUES (1, 'Art', 'art')")
	_, _ = env.DB.Conn().Exec("INSERT INTO albums (id, title, clean_title, artist_id, folder_path) VALUES (1, 'Alb', 'alb', 1, 'Alb')")
	for i := 1; i <= 3; i++ {
		_, err := env.DB.Conn().Exec(`
			INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_hash, file_size, mtime)
			VALUES (?, 1, 1, ?, 'clean', ?, ?, 2048, 123456.0)
		`, i, "Title "+strconv.Itoa(i), "Alb/0"+strconv.Itoa(i)+". Track.mp3", "hash"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("failed to insert track %d: %v", i, err)
		}
	}

	resp := env.Get("/api/library/tracks?limit=2&offset=0")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var page filemanager.LibraryTracksResponse
	if err := json.Unmarshal(env.ReadBody(resp), &page); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if page.Total != 3 {
		t.Errorf("expected total 3, got %d", page.Total)
	}
	if len(page.Tracks) != 2 {
		t.Errorf("expected 2 tracks, got %d", len(page.Tracks))
	}
}
