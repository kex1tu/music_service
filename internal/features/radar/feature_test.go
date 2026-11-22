package radar_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/auth"
	featureRadar "music-manager/internal/features/radar"
	serviceRadar "music-manager/internal/services/radar"
	"music-manager/internal/services/resolver"
	"music-manager/internal/testkit"
)

type mockScannerEngine struct {
	customLinks     map[string]string
	isRunning       bool
	scannedArtistID atomic.Int64
}

func newMockScanner() *mockScannerEngine {
	return &mockScannerEngine{
		customLinks: make(map[string]string),
	}
}

func (m *mockScannerEngine) ScanArtist(ctx context.Context, artistName string) (*serviceRadar.ScanArtistResult, error) {
	return &serviceRadar.ScanArtistResult{
		ArtistName:      artistName,
		TotalDiscovered: 5,
		NewReleases:     2,
		Releases:        []resolver.AggregatedRelease{},
	}, nil
}

func (m *mockScannerEngine) ScanArtistByID(ctx context.Context, artistID int64) (*serviceRadar.ScanArtistResult, error) {
	m.scannedArtistID.Store(artistID)
	return m.ScanArtist(ctx, "One")
}

func (m *mockScannerEngine) ScanAll(ctx context.Context, trigger string) error {
	m.isRunning = true
	return nil
}

func (m *mockScannerEngine) GetProgress() serviceRadar.ScanProgress {
	return serviceRadar.ScanProgress{
		IsRunning:        m.isRunning,
		CurrentArtist:    "Test Artist",
		TotalArtists:     10,
		CurrentIndex:     3,
		NewReleasesFound: 1,
	}
}

func (m *mockScannerEngine) CancelScan() {
	m.isRunning = false
}

func (m *mockScannerEngine) UpdateArtistCustomLinks(ctx context.Context, artistName, alias, deezerID, mbID, yandexID string) error {
	m.customLinks[artistName] = alias + ":" + deezerID
	return nil
}

func setupRadarEnv(t *testing.T) (*testkit.TestEnv, *featureRadar.Module, *mockScannerEngine) {
	t.Helper()
	env := testkit.NewEnv(t)

	repo := featureRadar.NewRepository(env.DB.Conn())
	scannerMock := newMockScanner()

	svc := featureRadar.NewService(featureRadar.ServiceConfig{
		Repo:    repo,
		Scanner: scannerMock,
	})

	mod := featureRadar.NewModule(svc)

	r := chi.NewRouter()
	authMiddleware := auth.Middleware(env.SecretKey)

	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		mod.RegisterRoutes(r)
	})

	env.Mount(r)
	env.SetAuthUser("admin")

	return env, mod, scannerMock
}

func TestRadar_UnauthorizedAccess(t *testing.T) {
	env, _, _ := setupRadarEnv(t)
	env.ClearAuth()

	resp := env.Get("/api/radar/feed")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", resp.StatusCode)
	}
}

func TestRadar_GetFeed_And_UnreadCount(t *testing.T) {
	env, _, _ := setupRadarEnv(t)

	now := time.Now().Format("2006-01-02")
	// Вставляем тестовые релизы радара
	_, err := env.DB.Conn().Exec(`
		INSERT INTO radar_releases (id, artist_name, clean_artist_name, title, clean_title, release_type, release_date, year, status, is_radar, source_url)
		VALUES (1, 'Daft Punk', 'daft punk', 'Random Memories', 'random memories', 'album', ?, 2024, 'new', 1, 'https://deezer.com/album/1'),
		       (2, 'Daft Punk', 'daft punk', 'Get Lucky Remixed', 'get lucky remixed', 'single', ?, 2024, 'new', 1, 'https://deezer.com/album/2'),
		       (3, 'Daft Punk', 'daft punk', 'Old Track', 'old track', 'single', '2013-01-01', 2013, 'downloaded', 0, 'https://deezer.com/album/3');
	`, now, now)
	if err != nil {
		t.Fatalf("failed to insert test releases: %v", err)
	}

	// 1. Проверяем счетчик непросмотренных
	respUnread := env.Get("/api/radar/unread-count")
	if respUnread.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", respUnread.StatusCode)
	}
	var unreadResp featureRadar.UnreadCountResponse
	if err := json.Unmarshal(env.ReadBody(respUnread), &unreadResp); err != nil {
		t.Fatalf("failed to decode unread response: %v", err)
	}
	if unreadResp.Unread != 2 {
		t.Errorf("expected 2 unread releases, got %d", unreadResp.Unread)
	}

	// 2. Проверяем ленту
	respFeed := env.Get("/api/radar/feed?type=album")
	if respFeed.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", respFeed.StatusCode)
	}
	var feedResp featureRadar.FeedResponse
	if err := json.Unmarshal(env.ReadBody(respFeed), &feedResp); err != nil {
		t.Fatalf("failed to decode feed response: %v", err)
	}
	if feedResp.Total != 1 || len(feedResp.Items) != 1 || feedResp.Items[0].Title != "Random Memories" {
		t.Errorf("unexpected feed response: %+v", feedResp)
	}
}

func TestRadarListsLibraryArtistsWithoutSubscriptions(t *testing.T) {
	env, _, _ := setupRadarEnv(t)
	if _, err := env.DB.Conn().Exec(`
        INSERT INTO artists(id, name, clean_name) VALUES
          (1, 'Radiohead', 'radiohead'), (2, 'Outside', 'outside'), (3, 'Guest', 'guest');
        INSERT INTO albums(id, artist_id, title, clean_title, folder_path)
          VALUES (1, 1, 'Album', 'album', 'Album');
        INSERT INTO album_artists(album_id, artist_id, position) VALUES (1, 3, 1);
        INSERT INTO tracked_artists(id, name, clean_name, artist_id, is_followed, auto_download) VALUES
          (11, 'Radiohead', 'radiohead', 1, 0, 1), (12, 'Outside', 'outside', 2, 1, 1),
          (13, 'Guest', 'guest', 3, 0, 0);
    `); err != nil {
		t.Fatal(err)
	}

	resp := env.Get("/api/radar/artists?followed=true")
	var list featureRadar.TrackedArtistsResponse
	if err := json.Unmarshal(env.ReadBody(resp), &list); err != nil || list.Total != 2 ||
		list.Items[0].Name != "Guest" || list.Items[1].Name != "Radiohead" || list.Items[0].AlbumCount != 1 {
		t.Fatalf("unexpected library artists: %+v, %v", list, err)
	}

	for _, path := range []string{"/api/radar/artists", "/api/radar/artists/Radiohead/follow", "/api/radar/artists/Radiohead/auto-download"} {
		resp := env.PostJSON(path, map[string]any{"name": "Outside", "auto_download": true, "followed": true})
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("obsolete POST %s returned %d", path, resp.StatusCode)
		}
		env.ReadBody(resp)
	}
}

func TestRadar_ArtistDiscography(t *testing.T) {
	env, _, _ := setupRadarEnv(t)

	_, _ = env.DB.Conn().Exec("INSERT INTO tracked_artists (id, name, clean_name, is_followed) VALUES (1, 'Nirvana', 'nirvana', 1)")
	_, _ = env.DB.Conn().Exec(`
		INSERT INTO radar_releases (id, artist_name, clean_artist_name, title, clean_title, release_type, year, status, is_radar, source_url)
		VALUES (10, 'Nirvana', 'nirvana', 'Nevermind', 'nevermind', 'album', 1991, 'in_library', 0, 'https://deezer.com/album/10')
	`)

	resp := env.Get("/api/radar/artists/" + url.PathEscape("Nirvana") + "/discography")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(env.ReadBody(resp)))
	}

	var disco featureRadar.ArtistDiscographyResponse
	if err := json.Unmarshal(env.ReadBody(resp), &disco); err != nil {
		t.Fatalf("failed to decode discography: %v", err)
	}

	if disco.Artist != "Nirvana" || len(disco.Releases) != 1 || disco.Releases[0].Title != "Nevermind" {
		t.Errorf("unexpected discography response: %+v", disco)
	}
	if disco.Tracked == nil || disco.Tracked.Name != "Nirvana" {
		t.Errorf("expected tracked artist Nirvana, got: %+v", disco.Tracked)
	}
}

func TestRadar_SetReleaseStatus(t *testing.T) {
	env, _, _ := setupRadarEnv(t)

	_, _ = env.DB.Conn().Exec(`
		INSERT INTO radar_releases (id, artist_name, clean_artist_name, title, clean_title, release_type, year, status, is_radar, source_url)
		VALUES (50, 'Artist', 'artist', 'Single 1', 'single 1', 'single', 2024, 'new', 1, 'https://deezer.com/album/50')
	`)

	// Меняем статус на ignored
	statusReq := featureRadar.SetReleaseStatusRequest{Status: "ignored"}
	resp := env.PostJSON("/api/radar/releases/50/status", statusReq)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Проверяем, что в unread больше нет
	respUnread := env.Get("/api/radar/unread-count")
	var unreadResp featureRadar.UnreadCountResponse
	_ = json.Unmarshal(env.ReadBody(respUnread), &unreadResp)
	if unreadResp.Unread != 0 {
		t.Errorf("expected 0 unread after status change, got %d", unreadResp.Unread)
	}
}

func TestRadar_Scan_Progress_And_Cancel(t *testing.T) {
	env, _, scanner := setupRadarEnv(t)

	// 1. Проверяем статус до запуска
	respProg := env.Get("/api/radar/progress")
	if respProg.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", respProg.StatusCode)
	}

	// 2. Запуск сканирования всей медиатеки
	respScan := env.PostJSON("/api/radar/scan", nil)
	if respScan.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", respScan.StatusCode)
	}
	if !scanner.isRunning {
		t.Errorf("expected scanner.isRunning to be true")
	}

	// 3. Сканирование одного артиста
	respScanArtist := env.PostJSON("/api/radar/scan", featureRadar.ScanRequest{Artist: "Gorillaz"})
	if respScanArtist.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", respScanArtist.StatusCode)
	}

	// 4. Отмена сканирования
	respCancel := env.PostJSON("/api/radar/cancel", nil)
	if respCancel.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", respCancel.StatusCode)
	}
	if scanner.isRunning {
		t.Errorf("expected scanner.isRunning to be false after cancel")
	}
}

func TestRadarUpdatesSelectedUnicodeArtistSettings(t *testing.T) {
	env, _, _ := setupRadarEnv(t)
	if _, err := env.DB.Conn().Exec(`
		INSERT INTO artists (id, name, clean_name) VALUES (1, 'Ёлка', 'ёлка'), (2, 'ЁЛКА', 'ёлка');
        INSERT INTO albums (artist_id, title, clean_title, folder_path)
            VALUES (1, 'First', 'first', 'First'), (2, 'Second', 'second', 'Second');
        INSERT INTO tracked_artists (id, name, clean_name, artist_id, deezer_id)
        VALUES (11, 'Ёлка', 'ёлка', 1, '100'), (22, 'ЁЛКА', 'ёлка', 2, '200')
	`); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"ЁЛКА", "ёлка"} {
		resp := env.Get("/api/radar/artists?search=" + url.QueryEscape(query))
		var artists featureRadar.TrackedArtistsResponse
		if err := json.Unmarshal(env.ReadBody(resp), &artists); err != nil || artists.Total != 2 {
			t.Fatalf("Unicode search %q: %+v, %v", query, artists, err)
		}
	}
	resp := env.PostJSON("/api/radar/artists/"+url.PathEscape("ЁЛКА")+"/custom-links",
		featureRadar.CustomLinksRequest{ArtistID: 22, DeezerID: "222"})
	body := env.ReadBody(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("custom links failed: %s", body)
	}
	var first, second string
	if err := env.DB.Conn().QueryRow(`SELECT
		(SELECT deezer_id FROM tracked_artists WHERE id=11),
		(SELECT deezer_id FROM tracked_artists WHERE id=22)`).Scan(&first, &second); err != nil ||
		first != "100" || second != "222" {
		t.Fatalf("custom links changed the wrong artist: %v %v %v", first, second, err)
	}
}

func TestRadarCustomLinksUsesLocalArtistIDWithOverlappingSettingsIDs(t *testing.T) {
	env, _, scanner := setupRadarEnv(t)
	if _, err := env.DB.Conn().Exec(`
		INSERT INTO artists(id, name, clean_name) VALUES(501,'One','one'),(502,'Two','two');
		INSERT INTO tracked_artists(id, artist_id, name, clean_name)
			VALUES(502,501,'One','one'),(501,502,'Two','two');
	`); err != nil {
		t.Fatal(err)
	}
	resp := env.PostJSON("/api/radar/artists/One/custom-links",
		featureRadar.CustomLinksRequest{ArtistID: 501, Alias: "custom"})
	body := env.ReadBody(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("custom links failed: %s", body)
	}
	var name string
	if err := env.DB.Conn().QueryRow("SELECT name FROM tracked_artists WHERE custom_search_alias='custom'").Scan(&name); err != nil || name != "One" {
		t.Fatalf("updated artist=%q err=%v", name, err)
	}
	resp = env.PostJSON("/api/radar/scan", featureRadar.ScanRequest{Artist: "One", ArtistID: 501})
	env.ReadBody(resp)
	if resp.StatusCode != http.StatusOK || scanner.scannedArtistID.Load() != 501 {
		t.Fatalf("scan used ID %d, status %d", scanner.scannedArtistID.Load(), resp.StatusCode)
	}

}
