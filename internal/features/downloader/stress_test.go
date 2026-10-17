package downloader_test

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"music-manager/internal/features/downloader"
	"music-manager/internal/testkit"
)

// TestStress_ConcurrencyEnqueueAndPrioritySorting тестирует высококонкурентную постановку задач,
// одновременную модификацию статусов и проверку детерминированной сортировки под гонками (-race).
func TestStress_ConcurrencyEnqueueAndPrioritySorting(t *testing.T) {
	env, mod := setupTestModule(t)
	mod.Service().PauseQueue() // держим на паузе, чтобы вручную симулировать и контролировать статусы

	validURLs := []string{
		"https://www.youtube.com/watch?v=stress001",
		"https://music.youtube.com/watch?v=stress002",
		"https://soundcloud.com/artist/stress003",
		"https://www.deezer.com/track/1004",
		"https://www.youtube.com/watch?v=stress005",
	}

	const (
		numEnqueuers   = 15
		jobsPerEnqueue = 10
		numReaders     = 10
		numModifiers   = 5
	)

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var createdJobIDs sync.Map
	var totalEnqueued int64

	// 1. Конкурентные создатели задач через POST /downloads
	for i := 0; i < numEnqueuers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < jobsPerEnqueue; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				url := fmt.Sprintf("%s_%d_%d", validURLs[(workerID+j)%len(validURLs)], workerID, j)
				req := downloader.CreateJobRequest{
					URL:    url,
					Source: "auto",
				}
				resp := env.PostJSON("/downloads", req)
				if resp.StatusCode == http.StatusOK {
					var created downloader.Job
					env.DecodeJSON(resp, &created)
					if created.ID != "" {
						createdJobIDs.Store(created.ID, created.ID)
						atomic.AddInt64(&totalEnqueued, 1)
					}
				}
				time.Sleep(2 * time.Millisecond)
			}
		}(i)
	}

	// 2. Конкурентные модификаторы статусов задач
	statuses := []downloader.JobStatus{
		downloader.StatusResolving,
		downloader.StatusDownloading,
		downloader.StatusConverting,
		downloader.StatusTagging,
		downloader.StatusReady,
		downloader.StatusError,
		downloader.StatusCancelled,
	}

	for i := 0; i < numModifiers; i++ {
		wg.Add(1)
		go func(modID int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				allJobs := mod.Service().GetAllJobs()
				if len(allJobs) > 0 {
					targetJob := allJobs[rand.Intn(len(allJobs))]
					st := statuses[rand.Intn(len(statuses))]
					switch st {
					case downloader.StatusDownloading:
						targetJob.UpdateProgress(float64(rand.Intn(90)+1), "Downloading chunk")
					case downloader.StatusReady:
						targetJob.SetReady(1, 1)
					case downloader.StatusError:
						targetJob.SetError("Simulated stress error")
					default:
						targetJob.SetStatus(st)
					}
				}
				time.Sleep(3 * time.Millisecond)
			}
		}(i)
	}

	// 3. Конкурентные читатели списка задач с проверкой инварианта сортировки
	var sortViolations int64
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				resp := env.Get("/downloads")
				if resp.StatusCode == http.StatusOK {
					var list []*downloader.Job
					env.DecodeJSON(resp, &list)

					// Проверка строгости порядка:
					// 1. Приоритет статуса: pI <= pJ
					// 2. При равном приоритете: CreatedAt DESC (более новые сначала)
					// 3. При равном CreatedAt: ID ASC
					for k := 0; k < len(list)-1; k++ {
						p1 := list[k].Status.Priority()
						p2 := list[k+1].Status.Priority()
						if p1 > p2 {
							atomic.AddInt64(&sortViolations, 1)
							t.Errorf("Priority violation: index %d (%s: %d) > index %d (%s: %d)",
								k, list[k].Status, p1, k+1, list[k+1].Status, p2)
						} else if p1 == p2 {
							if list[k].CreatedAt < list[k+1].CreatedAt {
								atomic.AddInt64(&sortViolations, 1)
								t.Errorf("CreatedAt violation: index %d (%s) < index %d (%s)",
									k, list[k].CreatedAt, k+1, list[k+1].CreatedAt)
							} else if list[k].CreatedAt == list[k+1].CreatedAt {
								if list[k].ID > list[k+1].ID {
									atomic.AddInt64(&sortViolations, 1)
									t.Errorf("ID tie-breaker violation: index %d (%s) > index %d (%s)",
										k, list[k].ID, k+1, list[k+1].ID)
								}
							}
						}
					}
				}
				time.Sleep(5 * time.Millisecond)
			}
		}(i)
	}

	// Ждем завершения генерации задач
	time.Sleep(500 * time.Millisecond)
	cancel()
	wg.Wait()

	if violations := atomic.LoadInt64(&sortViolations); violations > 0 {
		t.Fatalf("Found %d priority sorting violations under load", violations)
	}
	if enqueued := atomic.LoadInt64(&totalEnqueued); enqueued == 0 {
		t.Fatal("Expected at least some jobs to be successfully enqueued")
	}
}

// TestStress_PauseResumeOscillationsUnderRace тестирует многопоточную смену состояний паузы/возобновления
// через sync.Cond с параллельными воркерами и проверкой отсутствия дедлоков и гонок данных.
func TestStress_PauseResumeOscillationsUnderRace(t *testing.T) {
	env, mod := setupTestModule(t)
	svc := mod.Service()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// 1. Агрессивные осцилляции Pause/Resume
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if j%2 == 0 {
					svc.PauseQueue()
				} else {
					svc.ResumeQueue()
				}
				time.Sleep(time.Duration(rand.Intn(3)) * time.Millisecond)
			}
		}(i)
	}

	// 2. Параллельные HTTP-запросы паузы и возобновления
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if j%2 == 0 {
					resp := env.Do(env.NewRequest(http.MethodPost, "/downloads/pause", nil))
					_ = env.ReadBody(resp)
				} else {
					resp := env.Do(env.NewRequest(http.MethodPost, "/downloads/resume", nil))
					_ = env.ReadBody(resp)
				}
				time.Sleep(time.Duration(rand.Intn(4)) * time.Millisecond)
			}
		}(i)
	}

	// 3. Параллельное создание задач
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 15; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				req := downloader.CreateJobRequest{
					URL:    fmt.Sprintf("https://www.youtube.com/watch?v=osc_%d_%d", id, j),
					Source: "youtube",
				}
				resp := env.PostJSON("/downloads", req)
				_ = env.ReadBody(resp)
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
			}
		}(i)
	}

	// 4. Параллельный опрос состояния и сводки очереди
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				_ = svc.IsQueuePaused()
				summary := svc.GetSummary()
				_ = summary.OverallProgress
				resp := env.Get("/downloads/summary")
				_ = env.ReadBody(resp)
				time.Sleep(3 * time.Millisecond)
			}
		}(i)
	}

	// Ждем завершения всех горутин
	wg.Wait()

	// Обязательно возобновляем очередь в конце для корректной остановки
	svc.ResumeQueue()
}

// TestStress_RapidJobCancellationAndDeletion тестирует быстрые удаления, отмены и перезапуски задач,
// когда воркеры или таймеры дебаунса пытаются получить доступ к удаляемой задаче.
func TestStress_RapidJobCancellationAndDeletion(t *testing.T) {
	env, mod := setupTestModule(t)
	svc := mod.Service()

	// Создаем начальный пул задач
	var jobIDs []string
	for i := 0; i < 30; i++ {
		req := downloader.CreateJobRequest{
			URL:    fmt.Sprintf("https://www.youtube.com/watch?v=cancel_%d", i),
			Source: "youtube",
		}
		resp := env.PostJSON("/downloads", req)
		var job downloader.Job
		env.DecodeJSON(resp, &job)
		if job.ID != "" {
			jobIDs = append(jobIDs, job.ID)
		}
	}

	if len(jobIDs) == 0 {
		t.Fatal("Failed to create initial jobs for cancellation test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// 1. Горутины, параллельно удаляющие задачи
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for _, jID := range jobIDs {
				select {
				case <-ctx.Done():
					return
				default:
				}
				resp := env.Do(env.NewRequest(http.MethodDelete, fmt.Sprintf("/downloads/%s", jID), nil))
				_ = env.ReadBody(resp)
				time.Sleep(time.Duration(rand.Intn(4)) * time.Millisecond)
			}
		}(i)
	}

	// 2. Горутины, пытающиеся перезапустить задачи (POST /downloads/{id}/retry)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for _, jID := range jobIDs {
				select {
				case <-ctx.Done():
					return
				default:
				}
				resp := env.Do(env.NewRequest(http.MethodPost, fmt.Sprintf("/downloads/%s/retry", jID), nil))
				_ = env.ReadBody(resp)
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
			}
		}(i)
	}

	// 3. Горутины, очищающие очередь (POST /downloads/clear)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				resp := env.Do(env.NewRequest(http.MethodPost, "/downloads/clear", nil))
				_ = env.ReadBody(resp)
				time.Sleep(time.Duration(rand.Intn(10)) * time.Millisecond)
			}
		}(i)
	}

	// 4. Горутины, запрашивающие GetJob и ListJobs
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for _, jID := range jobIDs {
				select {
				case <-ctx.Done():
					return
				default:
				}
				resp := env.Get(fmt.Sprintf("/downloads/%s", jID))
				_ = env.ReadBody(resp)
				_ = svc.GetAllJobs()
				time.Sleep(2 * time.Millisecond)
			}
		}(i)
	}

	wg.Wait()
}

// TestStress_RepositoryConcurrency тестирует персистентный слой Repository на конкурентные
// операции записи, чтения, поиска и обновления под SQLite WAL.
func TestStress_RepositoryConcurrency(t *testing.T) {
	env := testkit.NewEnv(t)
	repo := downloader.NewRepository(env.DB.Conn())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const (
		numWorkers   = 16
		opsPerWorker = 60
	)

	var wg sync.WaitGroup
	var errCount int64

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for op := 0; op < opsPerWorker; op++ {
				select {
				case <-ctx.Done():
					return
				default:
				}

				jobID := fmt.Sprintf("stress_repo_%d_%d", workerID, op)
				action := op % 8

				switch action {
				case 0:
					// CreateJob
					j := &downloader.Job{
						ID:          jobID,
						Status:      downloader.StatusQueued,
						Source:      "youtube",
						URL:         "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
						Progress:    0.0,
						CurrentStep: "Queued",
					}
					if err := repo.CreateJob(ctx, j); err != nil {
						atomic.AddInt64(&errCount, 1)
						t.Errorf("repo.CreateJob failed: %v", err)
					}

				case 1:
					// UpdateJobProgress
					_ = repo.UpdateJobProgress(ctx, jobID, float64(op*10), "In progress")

				case 2:
					// UpdateJobStatus
					st := downloader.StatusDownloading
					if op%2 == 0 {
						st = downloader.StatusReady
					}
					_ = repo.UpdateJobStatus(ctx, jobID, st, "Status update", nil)

				case 3:
					// GetJob
					_, err := repo.GetJob(ctx, jobID)
					if err != nil {
						atomic.AddInt64(&errCount, 1)
						t.Errorf("repo.GetJob failed: %v", err)
					}

				case 4:
					// ListJobs & GetAllJobs
					_, err := repo.ListJobs(ctx, 10, 0)
					if err != nil {
						atomic.AddInt64(&errCount, 1)
						t.Errorf("repo.ListJobs failed: %v", err)
					}
					_, _ = repo.GetAllJobs(ctx)

				case 5:
					// GetActiveJobs & GetActiveJobCount
					_, _ = repo.GetActiveJobs(ctx)
					_, _ = repo.GetActiveJobCount(ctx)

				case 6:
					// Pinned source and soundcloud cache
					pinKey := fmt.Sprintf("artist:title:%d", workerID)
					_ = repo.SavePinnedSource(ctx, downloader.PinnedTrackSourceDTO{
						CacheKey:   pinKey,
						ArtistName: "Test Artist",
						TrackTitle: "Test Title",
						PinnedURL:  "https://soundcloud.com/test",
						SourceType: "soundcloud",
					})
					_, _ = repo.GetPinnedSource(ctx, pinKey)

					scKey := fmt.Sprintf("sc:%d:%d", workerID, op)
					_ = repo.PinSoundCloudVersion(ctx, scKey, "Artist", "Title", "https://soundcloud.com/pin", 180)
					_, _ = repo.GetSoundCloudCache(ctx, scKey, 24)

				case 7:
					// DeleteJob
					_ = repo.DeleteJob(ctx, jobID)
				}
			}
		}(w)
	}

	wg.Wait()

	if totalErrors := atomic.LoadInt64(&errCount); totalErrors > 0 {
		t.Fatalf("Encountered %d errors during concurrent repository access", totalErrors)
	}
}
