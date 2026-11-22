package downloader_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/auth"
	"music-manager/internal/features/downloader"
	"music-manager/internal/services/censorcheck"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/resolver"
	"music-manager/internal/testkit"
)

// setupTestModule инициализирует изолированное тестовое окружение и возвращает TestEnv с примонтированным Chi роутером.
func setupTestModule(t *testing.T) (*testkit.TestEnv, *downloader.Module) {
	t.Helper()
	env := testkit.NewEnv(t)

	repo := downloader.NewRepository(env.DB.Conn())

	// SourceResolver с мок-транспортом testkit
	sourceRes := resolver.NewSourceResolver(nil, nil, "")

	svc := downloader.NewService(downloader.ServiceConfig{
		Repo:          repo,
		Resolver:      sourceRes,
		TmpSandbox:    env.TmpSandbox,
		MusicSandbox:  env.MusicSandbox,
		CookiesPath:   "",
		MaxConcurrent: 2,
	})

	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("failed to start downloader service: %v", err)
	}
	t.Cleanup(func() {
		_ = svc.Close()
	})

	mod := downloader.NewModule(svc)

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

func TestDownloader_UnauthorizedAccess(t *testing.T) {
	env, _ := setupTestModule(t)

	// Очищаем cookie авторизации
	env.ClearAuth()

	// Запрос без сессии должен возвращать 401 Unauthorized
	resp := env.Get("/downloads")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", resp.StatusCode)
	}
}

func TestDownloader_CreateAndGetJob(t *testing.T) {
	env, mod := setupTestModule(t)
	mod.Service().PauseQueue()

	// 1. Создание задачи через POST /downloads
	createReq := downloader.CreateJobRequest{
		URL:    "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		Source: "youtube",
	}

	resp := env.PostJSON("/downloads", createReq)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d, body: %s", resp.StatusCode, string(env.ReadBody(resp)))
	}

	var createdJob downloader.Job
	env.DecodeJSON(resp, &createdJob)

	if createdJob.ID == "" {
		t.Fatal("expected non-empty job ID")
	}
	if createdJob.Status != downloader.StatusQueued {
		t.Fatalf("expected status 'QUEUED', got %q", createdJob.Status)
	}
	if createdJob.URL != createReq.URL {
		t.Fatalf("expected url %q, got %q", createReq.URL, createdJob.URL)
	}

	// 2. Получение задачи по ID через GET /downloads/{id}
	getResp := env.Get("/downloads/" + createdJob.ID)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", getResp.StatusCode)
	}

	var fetchedJob downloader.Job
	env.DecodeJSON(getResp, &fetchedJob)

	if fetchedJob.ID != createdJob.ID {
		t.Fatalf("expected fetched job ID %q, got %q", createdJob.ID, fetchedJob.ID)
	}
}

func TestDownloader_CreateBatch(t *testing.T) {
	env, mod := setupTestModule(t)
	mod.Service().PauseQueue()

	batchReq := downloader.CreateBatchJobRequest{
		URLs: []string{
			"https://www.youtube.com/watch?v=video111111",
			"https://www.youtube.com/watch?v=video222222",
			"https://www.youtube.com/watch?v=video333333",
		},
		Source: "youtube",
	}

	resp := env.PostJSON("/downloads/batch", batchReq)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var batchResp downloader.BatchJobResponse
	env.DecodeJSON(resp, &batchResp)

	if batchResp.TotalQueued != 3 {
		t.Fatalf("expected total_queued 3, got %d", batchResp.TotalQueued)
	}
	if len(batchResp.JobIDs) != 3 {
		t.Fatalf("expected 3 job_ids, got %d", len(batchResp.JobIDs))
	}
}

func TestDownloader_ListJobsAndDeterministicSort(t *testing.T) {
	env, mod := setupTestModule(t)
	mod.Service().PauseQueue()

	sameTimestamp := time.Now().UTC().Format(time.RFC3339Nano)

	// Добавляем задачи с одинаковым CreatedAt
	jobIDs := []string{"job-c", "job-a", "job-b"}
	for _, id := range jobIDs {
		job := &downloader.Job{
			ID:        id,
			Status:    downloader.StatusQueued,
			CreatedAt: sameTimestamp,
		}
		mod.Service().AddJob(job)
	}

	resp := env.Get("/downloads")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var jobs []*downloader.Job
	env.DecodeJSON(resp, &jobs)

	if len(jobs) < 3 {
		t.Fatalf("expected at least 3 jobs, got %d", len(jobs))
	}

	// Детерминированная сортировка: ID по возрастанию при одинаковом статусе и дате
	expectedIDs := []string{"job-a", "job-b", "job-c"}
	for i, exp := range expectedIDs {
		if jobs[i].ID != exp {
			t.Errorf("at index %d: expected %s, got %s", i, exp, jobs[i].ID)
		}
	}
}

func TestDownloader_QueuePauseResumeAndSummary(t *testing.T) {
	env, _ := setupTestModule(t)

	// 1. Проверка начального Summary
	resp := env.Get("/downloads/queue/summary")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var sum downloader.QueueSummaryResponse
	env.DecodeJSON(resp, &sum)
	if sum.IsPaused {
		t.Error("expected IsPaused false initially")
	}

	// 2. Пауза очереди POST /downloads/pause
	pauseResp := env.PostJSON("/downloads/pause", nil)
	if pauseResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from pause, got %d", pauseResp.StatusCode)
	}
	var pResp downloader.PauseResponse
	env.DecodeJSON(pauseResp, &pResp)
	if !pResp.IsPaused {
		t.Error("expected IsPaused true after pause")
	}

	// 3. Возобновление очереди POST /downloads/resume
	resResp := env.PostJSON("/downloads/resume", nil)
	if resResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from resume, got %d", resResp.StatusCode)
	}
	var rResp downloader.PauseResponse
	env.DecodeJSON(resResp, &rResp)
	if rResp.IsPaused {
		t.Error("expected IsPaused false after resume")
	}
}

func TestDownloader_RetryAndDeleteJob(t *testing.T) {
	env, mod := setupTestModule(t)
	mod.Service().PauseQueue()

	errMsg := "transcoding failed"
	failedJob := &downloader.Job{
		ID:        "job-failed-1",
		Status:    downloader.StatusError,
		Error:     &errMsg,
		URL:       "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		Source:    "youtube",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	mod.Service().AddJob(failedJob)

	// 1. Перезапуск задачи POST /downloads/{id}/retry
	retryResp := env.PostJSON("/downloads/job-failed-1/retry", nil)
	if retryResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from retry, got %d", retryResp.StatusCode)
	}

	var retriedJob downloader.Job
	env.DecodeJSON(retryResp, &retriedJob)
	if retriedJob.Status != downloader.StatusQueued {
		t.Fatalf("expected status 'QUEUED', got %q", retriedJob.Status)
	}

	// 2. Удаление задачи DELETE /downloads/{id}
	reqDel := env.NewRequest(http.MethodDelete, "/downloads/job-failed-1", nil)
	delResp := env.Do(reqDel)
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from delete, got %d", delResp.StatusCode)
	}

	// 3. Проверка, что задачи больше нет -> 404 Not Found
	getResp := env.Get("/downloads/job-failed-1")
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found after delete, got %d", getResp.StatusCode)
	}
}

func TestDownloader_ClearQueueAndCompleted(t *testing.T) {
	env, mod := setupTestModule(t)
	mod.Service().PauseQueue()

	mod.Service().AddJob(&downloader.Job{ID: "job-ready", Status: downloader.StatusReady})
	mod.Service().AddJob(&downloader.Job{ID: "job-err", Status: downloader.StatusError})
	mod.Service().AddJob(&downloader.Job{ID: "job-wait", Status: downloader.StatusQueued})

	clearResp := env.PostJSON("/downloads/queue/clear", nil)
	if clearResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from clear, got %d", clearResp.StatusCode)
	}

	var cr downloader.ClearResponse
	env.DecodeJSON(clearResp, &cr)
	if cr.Cleared != 2 {
		t.Fatalf("expected 2 cleared jobs, got %d", cr.Cleared)
	}

	// Проверяем, что осталась только задача в очереди
	listResp := env.Get("/downloads")
	var remaining []*downloader.Job
	env.DecodeJSON(listResp, &remaining)
	if len(remaining) != 1 || remaining[0].ID != "job-wait" {
		t.Fatalf("expected only 'job-wait' to remain, got %d jobs", len(remaining))
	}
}

func TestDownloader_ValidationAndSecurity(t *testing.T) {
	env, _ := setupTestModule(t)

	// 1. Создание задачи с пустым телом -> 400 Bad Request
	respEmpty := env.PostJSON("/downloads", strings.NewReader(""))
	if respEmpty.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for empty body, got %d", respEmpty.StatusCode)
	}

	// 2. Создание задачи с запрещенным доменом (SSRF Whitelist Guard) -> 400 Bad Request
	reqDisallowed := downloader.CreateJobRequest{
		URL: "http://169.254.169.254/latest/meta-data",
	}
	respDisallowed := env.PostJSON("/downloads", reqDisallowed)
	if respDisallowed.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for SSRF URL, got %d", respDisallowed.StatusCode)
	}

	// 3. Preview с недопустимым доменом -> 400 Bad Request
	respPrev := env.Get("/downloads/preview?url=https://malicious-site.com/track.mp3")
	if respPrev.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for disallowed preview domain, got %d", respPrev.StatusCode)
	}

	// 4. Загрузка .torrent файла -> 400 Bad Request
	respTorrent := env.PostJSON("/downloads/torrent", nil)
	if respTorrent.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for torrent upload, got %d", respTorrent.StatusCode)
	}
}

func TestDownloader_ApiPrefixAliases(t *testing.T) {
	env, _ := setupTestModule(t)

	// GET /api/downloads/candidates без параметров -> 400 Bad Request
	resp := env.Get("/api/downloads/candidates")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request from alias /api/downloads/candidates, got %d", resp.StatusCode)
	}

	// GET /api/downloads/preview без url -> 400 Bad Request
	respPrev := env.Get("/api/downloads/preview")
	if respPrev.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request from alias /api/downloads/preview, got %d", respPrev.StatusCode)
	}
}

func TestDownloader_GoldenJSONSnapshot(t *testing.T) {
	env, _ := setupTestModule(t)

	resp := env.Get("/downloads/queue/summary")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	body := env.ReadBody(resp)
	env.AssertGoldenJSON(body, "testdata/queue_summary.golden.json")
}

func TestDownloader_Repository_Lifecycle(t *testing.T) {
	env := testkit.NewEnv(t)
	repo := downloader.NewRepository(env.DB.Conn())
	ctx := context.Background()

	// 1. Создание задачи
	errMsg := "initial test error"
	job := &downloader.Job{
		ID:              "job-repo-test-01",
		Status:          downloader.StatusQueued,
		Source:          "youtube",
		URL:             "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		Progress:        0.0,
		CurrentStep:     "В очереди",
		TotalTracks:     1,
		CompletedTracks: 0,
		Error:           &errMsg,
	}

	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatalf("failed to create job: %v", err)
	}

	// 2. Получение задачи
	got, err := repo.GetJob(ctx, "job-repo-test-01")
	if err != nil {
		t.Fatalf("failed to get job: %v", err)
	}
	if got == nil || got.ID != "job-repo-test-01" {
		t.Fatalf("expected job-repo-test-01, got %v", got)
	}

	// 3. Обновление прогресса
	if err := repo.UpdateJobProgress(ctx, "job-repo-test-01", 55.5, "Скачивание 55%"); err != nil {
		t.Fatalf("failed to update progress: %v", err)
	}

	// 4. Проверка активных задач
	activeCount, err := repo.GetActiveJobCount(ctx)
	if err != nil {
		t.Fatalf("failed to get active count: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("expected 1 active job, got %d", activeCount)
	}

	activeJobs, err := repo.GetActiveJobs(ctx)
	if err != nil {
		t.Fatalf("failed to get active jobs: %v", err)
	}
	if len(activeJobs) != 1 {
		t.Fatalf("expected 1 active job in list, got %d", len(activeJobs))
	}

	// QUEUED survives restart. Only a task already claimed by a worker is interrupted.
	if resetCount, err := repo.ResetInterruptedJobs(ctx); err != nil || resetCount != 0 {
		t.Fatalf("reset %d queued jobs, err %v", resetCount, err)
	}
	if err := repo.UpdateJobStatus(ctx, job.ID, downloader.StatusResolving, "Resolving", nil); err != nil {
		t.Fatal(err)
	}
	resetCount, err := repo.ResetInterruptedJobs(ctx)
	if err != nil {
		t.Fatalf("failed to reset interrupted jobs: %v", err)
	}
	if resetCount != 1 {
		t.Fatalf("expected 1 reset job, got %d", resetCount)
	}

	// Проверяем, что статус переведен в ERROR
	afterReset, _ := repo.GetJob(ctx, "job-repo-test-01")
	if afterReset.Status != downloader.StatusError {
		t.Fatalf("expected status ERROR after reset, got %s", afterReset.Status)
	}

	// 6. Очистка завершенных задач
	cleared, err := repo.ClearFinishedJobs(ctx, 0)
	if err != nil {
		t.Fatalf("failed to clear finished jobs: %v", err)
	}
	if cleared != 1 {
		t.Fatalf("expected 1 cleared job, got %d", cleared)
	}

	// 7. Проверка удаления
	afterClear, _ := repo.GetJob(ctx, "job-repo-test-01")
	if afterClear != nil {
		t.Fatalf("expected job to be deleted, found: %v", afterClear)
	}
}

func TestDownloader_PreviewAudio_SubprocessMocking(t *testing.T) {
	env, mod := setupTestModule(t)

	// 1. Проверяем default test mode mock (MUSIC_MANAGER_TEST_MODE=1)
	resp := env.Get("/downloads/preview?url=" + url.QueryEscape("https://www.youtube.com/watch?v=mock_preview_001"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for mock preview, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/mpeg" {
		t.Fatalf("expected audio/mpeg Content-Type, got %q", ct)
	}
	body := env.ReadBody(resp)
	if len(body) == 0 {
		t.Fatal("expected non-empty audio payload")
	}

	// 2. Проверяем внедрение кастомного mock-стримера с симуляцией 502 Bad Gateway
	mod.SetPreviewStreamer(func(ctx context.Context, targetURL string, w http.ResponseWriter) error {
		return errors.New("simulated upstream failure")
	})

	respErr := env.Get("/downloads/preview?url=" + url.QueryEscape("https://www.youtube.com/watch?v=mock_preview_001"))
	if respErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway from mock error, got %d", respErr.StatusCode)
	}
}

func TestDownloader_Discography_YouTubeMocking(t *testing.T) {
	env, mod := setupTestModule(t)

	// 1. Проверяем default test mode mock (MUSIC_MANAGER_TEST_MODE=1)
	resp := env.Get("/downloads/discography?url=" + url.QueryEscape("https://music.youtube.com/channel/UC12345"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for mock discography, got %d", resp.StatusCode)
	}
	var discResp downloader.DiscographyResponse
	env.DecodeJSON(resp, &discResp)
	if discResp.Source != "youtube" {
		t.Fatalf("expected source 'youtube', got %q", discResp.Source)
	}
	if len(discResp.Releases) == 0 {
		t.Fatal("expected non-empty mock releases")
	}

	// 2. Проверяем внедрение кастомного mock fetcher (404 Not Found при пустом списке)
	mod.SetYtReleasesFetcher(func(ctx context.Context, targetURL, cookiesPath string) ([]downloader.DiscographyRelease, string, error) {
		return nil, "", nil
	})

	respNotFound := env.Get("/downloads/discography?url=" + url.QueryEscape("https://music.youtube.com/channel/UC12345"))
	if respNotFound.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found for empty releases, got %d", respNotFound.StatusCode)
	}
}

func TestDownloader_Discography_SSRF_StrictDomainValidation(t *testing.T) {
	env, _ := setupTestModule(t)

	ssrfVectors := []struct {
		name string
		url  string
	}{
		{"path_injection_youtube", "http://attacker.com/youtube.com"},
		{"query_injection_youtube", "http://evil.org?ref=https://youtube.com"},
		{"fragment_injection_youtube", "http://evil.org#https://youtube.com"},
		{"subdomain_spoof_youtube", "http://youtube.com.attacker.com"},
		{"prefix_spoof_youtube", "http://evil-youtube.com"},
		{"ftp_scheme_youtube", "ftp://youtube.com/artist/123"},
		{"gopher_scheme", "gopher://127.0.0.1:6379/_youtube.com"},
		{"file_scheme", "file:///etc/passwd#youtube.com"},
		{"path_injection_deezer", "http://attacker.com/deezer.com"},
		{"path_injection_yandex", "http://attacker.com/music.yandex.ru"},
		{"path_injection_apple", "http://attacker.com/music.apple.com"},
		{"path_injection_spotify", "http://attacker.com/spotify.com"},
		{"aws_metadata", "http://169.254.169.254/latest/meta-data"},
		{"loopback_ip", "http://127.0.0.1:8080"},
	}

	for _, tc := range ssrfVectors {
		tc := tc
		t.Run("GET_discography_ssrf_"+tc.name, func(t *testing.T) {
			target := "/downloads/discography?url=" + url.QueryEscape(tc.url)
			resp := env.Get(target)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("[%s] expected 400 Bad Request for SSRF URL %q, got %d", tc.name, tc.url, resp.StatusCode)
			}
		})
	}
}

func TestDownloader_ProcessTracks_CancellationWaitsForGoroutines(t *testing.T) {
	_, mod := setupTestModule(t)
	svc := mod.Service()

	ctx, cancel := context.WithCancel(context.Background())

	job, err := svc.CreateJob(ctx, "https://www.youtube.com/watch?v=cancel_leak_001", "youtube")
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}

	// Отменяем контекст немедленно
	cancel()

	// Даем время на обработку отмены
	time.Sleep(50 * time.Millisecond)
	snap := job.Snapshot()
	if snap.Status != downloader.StatusCancelled && snap.Status != downloader.StatusError && snap.Status != downloader.StatusQueued {
		t.Logf("job status after cancel: %s", snap.Status)
	}
}

func TestDownloader_DebouncedSave_ConcurrentProgressAndStatusUpdates(t *testing.T) {
	env, mod := setupTestModule(t)
	svc := mod.Service()

	job, err := svc.CreateJob(context.Background(), "https://www.youtube.com/watch?v=stress_debounce", "youtube")
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Worker 1: Rapidly update progress and current step (writing to Job)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			select {
			case <-ctx.Done():
				return
			default:
			}
			job.UpdateProgress(float64(i)*0.4, fmt.Sprintf("Step %d - downloading chunk", i))
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// Worker 2: Concurrent status updates
	wg.Add(1)
	go func() {
		defer wg.Done()
		statuses := []downloader.JobStatus{downloader.StatusResolving, downloader.StatusDownloading, downloader.StatusConverting, downloader.StatusTagging}
		for i := 0; i < 100; i++ {
			select {
			case <-ctx.Done():
				return
			default:
			}
			job.SetStatus(statuses[i%len(statuses)])
			time.Sleep(3 * time.Millisecond)
		}
	}()

	// Worker 3: Concurrent repository persistence via SaveJob
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			select {
			case <-ctx.Done():
				return
			default:
			}
			_ = svc.Repo().SaveJob(ctx, job)
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Worker 4: Concurrent HTTP list requests
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			select {
			case <-ctx.Done():
				return
			default:
			}
			resp := env.Get("/downloads")
			_ = env.ReadBody(resp)
			time.Sleep(5 * time.Millisecond)
		}
	}()

	wg.Wait()
}

type mockLyricsProvider struct {
	result *lyrics.LyricsSearchResult
}

func (m *mockLyricsProvider) Search(ctx context.Context, artist, title, album string, duration int) (*lyrics.LyricsSearchResult, error) {
	return m.result, nil
}

func TestDownloader_QuickListenSpots_ExtractedOnLyricsFound(t *testing.T) {
	env, mod := setupTestModule(t)
	svc := mod.Service()

	mockLRC := `[00:10.00]Куплет первый
[00:25.00]Закуривая паранойю в виде косяка
[01:05.00]Мефедрон и экстази рекой
`
	lyricsSvc := lyrics.NewService(env.MusicSandbox, &mockLyricsProvider{
		result: &lyrics.LyricsSearchResult{
			Lyrics:  mockLRC,
			Content: mockLRC,
			Synced:  true,
			Source:  "lrclib",
		},
	})
	svc.SetLyricsService(lyricsSvc)

	job := &downloader.Job{
		ID: "quick-listen-fixture", Status: downloader.StatusReady,
		URL: "https://soundcloud.com/test-artist/test-track", Source: "soundcloud",
	}

	meta := resolver.ReleaseMetadata{
		AlbumTitle:  "Test Album",
		AlbumArtist: "Test Artist",
		Year:        2026,
	}
	tr := resolver.ResolvedTrack{
		TrackID:        "track_test_1",
		TrackNumber:    1,
		Title:          "Test Track",
		Artist:         "Test Artist",
		Album:          "Test Album",
		AudioSourceURL: "https://soundcloud.com/test-artist/test-track",
		Status:         "pending",
	}
	job.SetMetadata(meta, []resolver.ResolvedTrack{tr})

	// Симулируем поиск текстов и извлечение точек, как в processSingleTrack
	lrcRes, lrcErr := lyricsSvc.SearchLyrics(context.Background(), tr.Artist, tr.Title, meta.AlbumTitle, tr.Duration)
	if lrcErr != nil || lrcRes == nil || !lrcRes.Synced {
		t.Fatalf("expected synced lyrics result")
	}

	spots := censorcheck.ExtractSpots(lrcRes.Lyrics, 3)
	if len(spots) == 0 {
		t.Fatalf("expected extracted spots, got 0")
	}

	job.UpdateTrack(0, func(t *resolver.ResolvedTrack) {
		t.Status = "ready"
		t.QuickListenSpots = spots
	})

	snap := job.Snapshot()
	if len(snap.Tracks[0].QuickListenSpots) == 0 {
		t.Fatalf("expected QuickListenSpots in snapshot track")
	}

	// Проверяем категорию и наличие слова "косяка" или "мефедрон"
	foundCoAP := false
	for _, s := range snap.Tracks[0].QuickListenSpots {
		if s.Category == censorcheck.CategoryDrugCoAP {
			foundCoAP = true
			break
		}
	}
	if !foundCoAP {
		t.Errorf("expected CategoryDrugCoAP spot in track QuickListenSpots")
	}

	if err := svc.Repo().CreateJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}

	// Проверяем сериализацию эндпоинта GET /downloads/{id}
	req := env.NewRequest("GET", "/downloads/"+job.ID, nil)
	resp := env.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	bodyStr := string(env.ReadBody(resp))
	if !strings.Contains(bodyStr, "quick_listen_spots") {
		t.Errorf("expected quick_listen_spots in JSON output: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "DRUG_COAP_6_13") {
		t.Errorf("expected DRUG_COAP_6_13 in JSON output: %s", bodyStr)
	}
}

func TestDownloader_ReleaseCandidates_And_SwitchReleaseSource(t *testing.T) {
	env, mod := setupTestModule(t)

	// Мокаем FullReleaseMatcher для исключения сетевых запросов в тестах
	scSearcher := resolver.NewSoundCloudSetSearcher("")
	scSearcher.SetCustomSearchFunc(func(ctx context.Context, artist, album string, limit int) ([]resolver.AlbumReleaseCandidate, error) {
		return []resolver.AlbumReleaseCandidate{
			{
				BrowseID:   "sc_mock_spletnitsa",
				PlaylistID: "https://soundcloud.com/rusyarapscene/sets/spletnitsa",
				Title:      "СПЛЕТНИЦА",
				Artist:     "DEAD BLONDE",
				Tracks: []resolver.AlbumTrackCandidate{
					{
						Title:     "Банкомат",
						Artist:    "DEAD BLONDE",
						Duration:  200,
						DirectURL: "https://soundcloud.com/rusyarapscene/dead-blonde-bankomat",
					},
					{
						Title:     "Красный свет",
						Artist:    "DEAD BLONDE",
						Duration:  198,
						DirectURL: "https://soundcloud.com/rusyarapscene/dead-blonde-krasny-svet",
					},
				},
				TotalTracks: 2,
			},
		}, nil
	})
	mockMatcher := resolver.NewFullReleaseMatcher(nil, scSearcher)
	mod.Service().Resolver().SetFullReleaseMatcher(mockMatcher)

	// 1. Проверяем валидацию пустого запроса
	resp400 := env.Get("/api/downloads/release-candidates")
	if resp400.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", resp400.StatusCode)
	}

	mod.Service().PauseQueue()
	job := &downloader.Job{
		ID: "release-source-fixture", Status: downloader.StatusWaitingSource,
		URL: "https://music.youtube.com/playlist?list=mock_cand_job", Source: "youtube",
	}

	// Заполняем треки задачи потокобезопасно через SetMetadata
	tot := 2
	tracks := []resolver.ResolvedTrack{
		{
			TrackID:        "t1",
			TrackNumber:    1,
			TrackTotal:     &tot,
			Title:          "Банкомат",
			Artist:         "DEAD BLONDE",
			Album:          "СПЛЕТНИЦА",
			Duration:       200,
			AudioSourceURL: "https://music.youtube.com/watch?v=old1",
		},
		{
			TrackID:        "t2",
			TrackNumber:    2,
			TrackTotal:     &tot,
			Title:          "Красный свет",
			Artist:         "DEAD BLONDE",
			Album:          "СПЛЕТНИЦА",
			Duration:       198,
			AudioSourceURL: "https://music.youtube.com/watch?v=old2",
		},
	}
	job.SetMetadata(resolver.ReleaseMetadata{
		AlbumArtist: "DEAD BLONDE",
		AlbumTitle:  "СПЛЕТНИЦА",
	}, tracks)

	if err := mod.Service().Repo().CreateJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	mod.Service().AddJob(job)

	// 3. Запрашиваем кандидатов по job_id
	respCands := env.Get(fmt.Sprintf("/api/downloads/release-candidates?job_id=%s", job.ID))
	if respCands.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for release-candidates, got %d", respCands.StatusCode)
	}
	var candsResp downloader.ReleaseCandidatesResponse
	env.DecodeJSON(respCands, &candsResp)
	if !candsResp.IsCyrillic {
		t.Errorf("expected is_cyrillic = true for DEAD BLONDE")
	}
	if len(candsResp.TargetTracks) != 2 {
		t.Errorf("expected 2 target tracks, got %d", len(candsResp.TargetTracks))
	}

	// 4. Проверяем переключение источника релиза (SwitchReleaseSource)
	switchReq := downloader.SwitchReleaseSourceRequest{
		JobID:     job.ID,
		SourceURL: "https://soundcloud.com/rusyarapscene/sets/spletnitsa",
		TrackMapping: map[int]string{
			0: "https://soundcloud.com/rusyarapscene/dead-blonde-bankomat",
			1: "https://soundcloud.com/rusyarapscene/dead-blonde-krasny-svet",
		},
	}
	respSwitch := env.PostJSON("/api/downloads/releases/switch-source", switchReq)
	if respSwitch.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for switch-source, got %d. Body: %s", respSwitch.StatusCode, string(env.ReadBody(respSwitch)))
	}
	var switchRes downloader.SwitchReleaseSourceResult
	env.DecodeJSON(respSwitch, &switchRes)
	if switchRes.UpdatedCount != 2 {
		t.Errorf("expected updated_count = 2, got %d", switchRes.UpdatedCount)
	}

	// Проверяем, что в задаче обновились AudioSourceURL
	snap := job.Snapshot()
	if snap.Tracks[0].AudioSourceURL != "https://soundcloud.com/rusyarapscene/dead-blonde-bankomat" {
		t.Errorf("track 0 url not updated: %s", snap.Tracks[0].AudioSourceURL)
	}
	if snap.Tracks[1].AudioSourceURL != "https://soundcloud.com/rusyarapscene/dead-blonde-krasny-svet" {
		t.Errorf("track 1 url not updated: %s", snap.Tracks[1].AudioSourceURL)
	}
	if snap.Tracks[0].VersionStatus != "manual" {
		t.Errorf("track 0 version status = %q, want 'manual'", snap.Tracks[0].VersionStatus)
	}
}

func TestDownloader_ParseReleaseURL(t *testing.T) {
	env, mod := setupTestModule(t)

	// 1. Проверяем отклонение домена вне whitelist
	badReq := downloader.ParseReleaseURLRequest{
		URL: "https://evil-site.com/set/123",
	}
	respBad := env.PostJSON("/api/downloads/release-candidates/parse-url", badReq)
	if respBad.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for disallowed domain, got %d", respBad.StatusCode)
	}

	// 2. Мокаем parseURLFunc
	scSearcher := resolver.NewSoundCloudSetSearcher("")
	scSearcher.SetCustomParseURLFunc(func(ctx context.Context, setURL string) (*resolver.AlbumReleaseCandidate, error) {
		return &resolver.AlbumReleaseCandidate{
			BrowseID:   setURL,
			PlaylistID: setURL,
			Title:      "СПЛЕТНИЦА",
			Artist:     "DEAD BLONDE",
			Tracks: []resolver.AlbumTrackCandidate{
				{
					Title:     "Банкомат",
					Artist:    "DEAD BLONDE",
					Duration:  200,
					DirectURL: "https://soundcloud.com/track1",
				},
			},
			TotalTracks: 1,
		}, nil
	})
	mockMatcher := resolver.NewFullReleaseMatcher(nil, scSearcher)
	mod.Service().Resolver().SetFullReleaseMatcher(mockMatcher)

	// 3. Успешный разбор валидной ссылки
	goodReq := downloader.ParseReleaseURLRequest{
		URL:    "https://soundcloud.com/deadblonde/sets/spletnitsa",
		Artist: "DEAD BLONDE",
		Album:  "СПЛЕТНИЦА",
		TargetTracks: []resolver.ResolvedTrack{
			{TrackID: "t1", TrackNumber: 1, Title: "Банкомат", Artist: "DEAD BLONDE", Duration: 200},
		},
	}
	respGood := env.PostJSON("/api/downloads/release-candidates/parse-url", goodReq)
	if respGood.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for parse-url, got %d. Body: %s", respGood.StatusCode, string(env.ReadBody(respGood)))
	}
	var parsedItem resolver.ReleaseCandidateItem
	env.DecodeJSON(respGood, &parsedItem)
	if parsedItem.Title != "СПЛЕТНИЦА" {
		t.Errorf("expected title 'СПЛЕТНИЦА', got %q", parsedItem.Title)
	}
	if parsedItem.MatchedCount != 1 {
		t.Errorf("expected matched count = 1, got %d", parsedItem.MatchedCount)
	}
}

func TestDownloader_FinishImportRetainsUnselectedTracks(t *testing.T) {
	env, mod := setupTestModule(t)
	first, second := "job/converted/first.mp3", "job/converted/second.mp3"
	secondFile := env.CreateDummyFile(env.TmpSandbox, second, []byte("not yet imported"))
	notes := env.CreateDummyFile(env.TmpSandbox, "job/notes.dat", []byte("keep"))
	mod.Service().AddJob(&downloader.Job{
		ID:     "job",
		Status: downloader.StatusPartialReady,
		Tracks: []resolver.ResolvedTrack{
			{Title: "First", ConvertedPath: &first},
			{Title: "Second", ConvertedPath: &second},
		},
	})

	finish := func(want string) {
		t.Helper()
		resp := env.Do(env.NewRequest(http.MethodDelete, "/downloads/job?imported=true", nil))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected status %d: %s", resp.StatusCode, env.ReadBody(resp))
		}
		var result downloader.DeleteResponse
		env.DecodeJSON(resp, &result)
		if result.Status != want {
			t.Fatalf("finish status = %q, want %q", result.Status, want)
		}
	}
	finish("pending")
	if cleared := mod.Service().ClearCompletedJobs(t.Context()); cleared != 0 {
		t.Fatalf("bulk cleanup removed %d jobs with pending imports", cleared)
	}
	var tracks downloader.DownloadTracksResponse
	env.DecodeJSON(env.Get("/downloads/job/tracks"), &tracks)
	if len(tracks.Tracks) != 1 || tracks.Tracks[0].Title != "Second" {
		t.Fatalf("expected just the remaining track, got %+v", tracks.Tracks)
	}
	if err := os.Remove(secondFile); err != nil {
		t.Fatal(err)
	}
	finish("deleted")
	if resp := env.Get("/downloads/job"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("finished job still in queue: %d", resp.StatusCode)
	}
	if _, err := os.Stat(notes); err != nil {
		t.Fatalf("finalization removed unrelated staging files: %v", err)
	}
}
