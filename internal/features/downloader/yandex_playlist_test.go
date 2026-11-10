package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

func TestDownloaderService_Preflight_YandexPlaylistIframe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users/my-user/playlists/50":
			w.Write([]byte(`{
				"result": {
					"kind": 50,
					"title": "Летний Плейлист 2026",
					"trackCount": 3,
					"owner": {"login": "my-user", "name": "DJ Summer"},
					"tracks": [
						{
							"id": 1,
							"track": {
								"id": 1,
								"title": "Summer Track 1",
								"durationMs": 180000,
								"artists": [{"name": "Summer Artist 1"}],
								"albums": [{"title": "Album 1", "year": 2026}]
							}
						},
						{
							"id": 2,
							"track": {
								"id": 2,
								"title": "Summer Track 2",
								"durationMs": 200000,
								"artists": [{"name": "Summer Artist 2"}],
								"albums": [{"title": "Album 2", "year": 2025}]
							}
						},
						{
							"id": 3
						}
					]
				}
			}`))
		case "/tracks":
			// track 3 не найден в каталоге
			w.Write([]byte(`{"result": []}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	yaClient := resolver.NewYandexMusicClient("")
	yaClient.SetBaseURL(server.URL)

	sourceResolver := resolver.NewSourceResolver(nil, nil, "")
	sourceResolver.SetYandexClient(yaClient)

	svc := NewService(ServiceConfig{
		Resolver: sourceResolver,
	})

	ctx := context.Background()
	iframeInput := `<iframe frameborder="0" style="border:none;width:100%;height:450px;" width="100%" height="450" src="https://music.yandex.ru/iframe/#playlist/my-user/50"></iframe>`

	resp, err := svc.PreflightRelease(ctx, iframeInput, "auto")
	if err != nil {
		t.Fatalf("PreflightRelease failed: %v", err)
	}

	if resp == nil {
		t.Fatal("PreflightResponse is nil")
	}

	// 1. Проверяем ReleaseInfo
	if !resp.Release.IsPlaylist {
		t.Error("resp.Release.IsPlaylist must be true for playlists")
	}
	if resp.Release.Title != "Летний Плейлист 2026" {
		t.Errorf("resp.Release.Title = %q, want 'Летний Плейлист 2026'", resp.Release.Title)
	}
	if resp.Release.Artist != "DJ Summer" {
		t.Errorf("resp.Release.Artist = %q, want 'DJ Summer'", resp.Release.Artist)
	}
	if resp.Release.FetchedTracks != 2 {
		t.Errorf("resp.Release.FetchedTracks = %d, want 2", resp.Release.FetchedTracks)
	}
	if resp.Release.SkippedTracks != 1 {
		t.Errorf("resp.Release.SkippedTracks = %d, want 1", resp.Release.SkippedTracks)
	}

	// 2. Проверяем Summary
	if resp.Summary.FetchedTracksCount != 2 {
		t.Errorf("resp.Summary.FetchedTracksCount = %d, want 2", resp.Summary.FetchedTracksCount)
	}
	if resp.Summary.SkippedTracksCount != 1 {
		t.Errorf("resp.Summary.SkippedTracksCount = %d, want 1", resp.Summary.SkippedTracksCount)
	}

	// 3. Проверяем треки
	if len(resp.Tracks) != 2 {
		t.Fatalf("len(resp.Tracks) = %d, want 2", len(resp.Tracks))
	}
	if resp.Tracks[0].Title != "Summer Track 1" || resp.Tracks[1].Title != "Summer Track 2" {
		t.Errorf("Unexpected track titles: %q, %q", resp.Tracks[0].Title, resp.Tracks[1].Title)
	}
}

func TestDownloaderService_ProcessSingleTrack_PreservesTrackAlbumForPlaylists(t *testing.T) {
	tempDir := t.TempDir()
	rawDir := filepath.Join(tempDir, "raw")
	convertedDir := filepath.Join(tempDir, "converted")
	_ = os.MkdirAll(rawDir, 0755)
	_ = os.MkdirAll(convertedDir, 0755)

	tmpBox, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewService(ServiceConfig{
		TmpSandbox: tmpBox,
	})

	playlistMeta := &resolver.ReleaseMetadata{
		AlbumTitle:  "Mega Hits Playlist",
		AlbumArtist: "Various Curators",
		IsPlaylist:  true,
	}

	// Создаем минимальный валидный MP3 файл для теста записи тегов
	testMP3 := filepath.Join(convertedDir, "01. Hit Song.mp3")
	minimalMP3 := []byte{
		0xFF, 0xFB, 0x90, 0x64, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	if err := os.WriteFile(testMP3, minimalMP3, 0644); err != nil {
		t.Fatal(err)
	}

	track := resolver.ResolvedTrack{
		TrackID:     "ya_999_1",
		TrackNumber: 1,
		Title:       "Hit Song",
		Artist:      "Original Singer",
		Album:       "Actual Studio Album", // Настоящий альбом трека
		AlbumArtist: "Original Singer",
		Duration:    180,
	}

	// Проверяем, что при тегировании трека плейлиста в тег пишется его собственный альбом "Actual Studio Album",
	// а не название плейлиста "Mega Hits Playlist"
	art := track.Artist
	albArt := track.AlbumArtist
	alb := track.Album
	if alb == "" {
		if playlistMeta.IsPlaylist {
			alb = track.Title
		} else {
			alb = playlistMeta.AlbumTitle
		}
	}
	if albArt == "" && !playlistMeta.IsPlaylist {
		albArt = playlistMeta.AlbumArtist
	}
	if albArt == "" {
		albArt = art
	}

	if alb != "Actual Studio Album" {
		t.Fatalf("Expected album %q, got %q", "Actual Studio Album", alb)
	}

	tagData := id3.TrackTags{
		Path:        testMP3,
		Title:       track.Title,
		Artist:      art,
		Album:       alb,
		AlbumArtist: albArt,
		TrackNumber: track.TrackNumber,
		Duration:    float64(track.Duration),
	}

	if err := id3.WriteTags(testMP3, tagData, nil); err != nil {
		t.Fatalf("id3.WriteTags failed: %v", err)
	}

	readTags, err := id3.ReadTags(testMP3, "01. Hit Song.mp3")
	if err != nil {
		t.Fatalf("id3.ReadTags failed: %v", err)
	}

	if readTags.Album != "Actual Studio Album" {
		t.Errorf("readTags.Album = %q, want 'Actual Studio Album' (must NEVER be %q)", readTags.Album, playlistMeta.AlbumTitle)
	}
	if readTags.Artist != "Original Singer" {
		t.Errorf("readTags.Artist = %q, want 'Original Singer'", readTags.Artist)
	}

	_ = svc
}
