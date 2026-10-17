package radar_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
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
	followed     map[string]bool
	autoDownload map[string]bool
	customLinks  map[string]string
	isRunning    bool
}

func newMockScanner() *mockScannerEngine {
	return &mockScannerEngine{
		followed:     make(map[string]bool),
		autoDownload: make(map[string]bool),
		customLinks:  make(map[string]string),
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

func (m *mockScannerEngine) FollowArtist(ctx context.Context, artistName string, followed bool) error {
	m.followed[artistName] = followed
	return nil
}

func (m *mockScannerEngine) SetAutoDownload(ctx context.Context, artistName string, auto bool) error {
	m.autoDownload[artistName] = auto
	return nil
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

func TestRadar_TrackedArtists_Lifecycle(t *testing.T) {
	env, _, scanner := setupRadarEnv(t)

	// 1. Добавляем артиста
	addReq := featureRadar.AddTrackedArtistRequest{Name: "Radiohead"}
	respAdd := env.PostJSON("/api/radar/artists", addReq)
	if respAdd.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", respAdd.StatusCode, string(env.ReadBody(respAdd)))
	}

	// 2. Проверяем список
	respList := env.Get("/api/radar/artists")
	var listResp featureRadar.TrackedArtistsResponse
	if err := json.Unmarshal(env.ReadBody(respList), &listResp); err != nil {
		t.Fatalf("failed to decode artists: %v", err)
	}
	if listResp.Total != 1 || listResp.Items[0].Name != "Radiohead" {
		t.Errorf("expected Radiohead in tracked artists, got %+v", listResp)
	}

	// 3. Переключаем Follow
	followReq := featureRadar.FollowArtistRequest{Followed: true}
	respFollow := env.PostJSON("/api/radar/artists/"+url.PathEscape("Radiohead")+"/follow", followReq)
	if respFollow.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from follow, got %d", respFollow.StatusCode)
	}
	if !scanner.followed["Radiohead"] {
		t.Errorf("expected scanner.followed[Radiohead] to be true")
	}

	// 4. Переключаем AutoDownload
	autoReq := featureRadar.AutoDownloadRequest{AutoDownload: true}
	respAuto := env.PostJSON("/api/radar/artists/"+url.PathEscape("Radiohead")+"/auto-download", autoReq)
	if respAuto.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from auto-download, got %d", respAuto.StatusCode)
	}
	if !scanner.autoDownload["Radiohead"] {
		t.Errorf("expected scanner.autoDownload[Radiohead] to be true")
	}

	// 5. Обновляем кастомные ссылки
	customReq := featureRadar.CustomLinksRequest{
		Alias:    "RH",
		DeezerID: "12345",
	}
	respCustom := env.PostJSON("/api/radar/artists/"+url.PathEscape("Radiohead")+"/custom-links", customReq)
	if respCustom.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from custom-links, got %d", respCustom.StatusCode)
	}
	if scanner.customLinks["Radiohead"] != "RH:12345" {
		t.Errorf("unexpected custom links: %s", scanner.customLinks["Radiohead"])
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
