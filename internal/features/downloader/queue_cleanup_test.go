package downloader

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
)

func TestQueueCleanupFilesystemFailure(t *testing.T) {
	for _, action := range []string{"delete", "retry"} {
		t.Run(action, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "queue.db")
			database, err := db.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			sb, err := sandbox.NewPathSandbox(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			dir, err := sb.Resolve("job/raw", false)
			if err != nil {
				t.Fatal(err)
			}
			if err := safefs.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			file, err := sb.Resolve("job/raw/partial.dat", false)
			if err != nil {
				t.Fatal(err)
			}
			if err := safefs.WriteFile(file, []byte("incomplete"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := safefs.Chmod(dir, 0500); err != nil {
				t.Fatal(err)
			}
			defer safefs.Chmod(dir, 0700)
			repo := NewRepository(database.Conn())
			job := &Job{ID: "job", Status: StatusError, Source: "deezer", URL: "https://www.deezer.com/album/1"}
			if err := repo.CreateJob(t.Context(), job); err != nil {
				t.Fatal(err)
			}
			svc := NewService(ServiceConfig{Repo: repo, TmpSandbox: sb})
			svc.AddJob(job)
			router := chi.NewRouter()
			NewModule(svc).RegisterRoutes(router)
			method, url := http.MethodDelete, "/downloads/job"
			if action == "retry" {
				method, url = http.MethodPost, "/downloads/job/retry"
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(method, url, nil))
			if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), `"deleted"`) {
				t.Errorf("cleanup failure was reported as success: HTTP %d: %s", response.Code, response.Body)
			}
			if _, err := safefs.Stat(file); err != nil {
				t.Fatalf("failure fixture did not retain file: %v", err)
			}
			saved, err := repo.GetJob(t.Context(), "job")
			if err != nil || saved == nil || saved.Status != StatusError || saved.Error == nil {
				t.Errorf("cleanup failure was lost from SQLite: %+v, %v", saved, err)
			}
			if got, ok := svc.GetJob("job"); !ok || got.Snapshot().Status != StatusError || got.Snapshot().Error == nil {
				t.Errorf("cleanup failure was lost from memory: %+v", got)
			}
			if err := svc.Close(); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			database, err = db.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			repo = NewRepository(database.Conn())
			// A second failure on a fresh service must retain the original action.
			second := NewService(ServiceConfig{Repo: repo, TmpSandbox: sb})
			second.PauseQueue()
			if err := second.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, ok := second.GetJob("job")
			wantAction := cleanupDelete
			if action == "retry" {
				wantAction = cleanupRetry
			}
			if !ok || got.Snapshot().CleanupPending != wantAction {
				t.Fatalf("restart lost cleanup action: %+v", got)
			}
			if claimed, err := repo.ClaimNextJob(t.Context()); err != nil || claimed != nil {
				t.Fatalf("claimed pending cleanup: %+v, %v", claimed, err)
			}
			if n := second.ClearCompletedJobs(t.Context()); n != 0 {
				t.Fatalf("bulk clear discarded cleanup intent: %d", n)
			}
			if err := second.Close(); err != nil {
				t.Fatal(err)
			}
			if err := safefs.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			third := NewService(ServiceConfig{Repo: repo, TmpSandbox: sb})
			third.PauseQueue()
			if err := third.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer third.Close()
			jobDir, err := sb.Resolve("job", false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := safefs.Stat(jobDir); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("restart did not finish cleanup: %v", err)
			}
			saved, err = repo.GetJob(t.Context(), "job")
			if err != nil {
				t.Fatal(err)
			}
			if action == "delete" {
				if _, ok := third.GetJob("job"); ok || saved != nil {
					t.Fatalf("deleted job survived restart: %+v", saved)
				}
			} else if saved == nil || saved.Status != StatusQueued || saved.CleanupPending != "" {
				t.Fatalf("retry did not queue after cleanup: %+v", saved)
			}
		})
	}
}

func cleanupFixture(t *testing.T) (*Service, *Repository, *sandbox.PathSandbox) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	sb, err := sandbox.NewPathSandbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(database.Conn())
	svc := NewService(ServiceConfig{Repo: repo, TmpSandbox: sb})
	t.Cleanup(func() { svc.Close() })
	return svc, repo, sb
}

func stageCleanupFile(t *testing.T, sb *sandbox.PathSandbox, rel string) string {
	t.Helper()
	path, err := sb.Resolve(rel, false)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := sb.Resolve(filepath.Dir(rel), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := safefs.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := safefs.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestQueueCleanupDatabaseFailures(t *testing.T) {
	for _, action := range []JobStatus{cleanupDelete, cleanupRetry} {
		for _, phase := range []string{"intent", "finish"} {
			t.Run(string(action)+"/"+phase, func(t *testing.T) {
				svc, repo, sb := cleanupFixture(t)
				file := stageCleanupFile(t, sb, "job/raw/partial.dat")
				job := &Job{ID: "job", Status: StatusError}
				if err := repo.CreateJob(t.Context(), job); err != nil {
					t.Fatal(err)
				}
				svc.AddJob(job)
				query := `CREATE TRIGGER fail_cleanup BEFORE UPDATE ON download_jobs BEGIN SELECT RAISE(FAIL, 'intent rejected'); END`
				if phase == "finish" {
					query = `CREATE TRIGGER fail_cleanup BEFORE DELETE ON download_jobs BEGIN SELECT RAISE(FAIL, 'delete rejected'); END`
					if action == cleanupRetry {
						query = `CREATE TRIGGER fail_cleanup BEFORE UPDATE ON download_jobs WHEN NEW.status='QUEUED' BEGIN SELECT RAISE(FAIL, 'retry rejected'); END`
					}
				}
				if _, err := repo.db.ExecContext(t.Context(), query); err != nil {
					t.Fatal(err)
				}
				var err error
				if action == cleanupDelete {
					err = svc.DeleteJob(t.Context(), "job")
				} else {
					_, err = svc.RetryJob(t.Context(), "job")
				}
				if !errors.Is(err, errQueueStorage) {
					t.Fatalf("expected storage failure: %v", err)
				}
				saved, err := repo.GetJob(t.Context(), "job")
				if err != nil || saved == nil {
					t.Fatalf("lost job: %+v, %v", saved, err)
				}
				if phase == "intent" {
					if saved.CleanupPending != "" || job.Snapshot().CleanupPending != "" {
						t.Fatal("published rejected intent")
					}
					if got, err := safefs.ReadFile(file); err != nil || string(got) != "keep" {
						t.Fatalf("touched files before durable intent: %q, %v", got, err)
					}
				} else {
					if saved.CleanupPending != action || job.Snapshot().CleanupPending != action {
						t.Fatal("lost intent after filesystem success")
					}
					if _, err := safefs.Stat(file); !errors.Is(err, fs.ErrNotExist) {
						t.Fatalf("did not reach filesystem cleanup: %v", err)
					}
				}
				if _, err := repo.db.ExecContext(t.Context(), `DROP TRIGGER fail_cleanup`); err != nil {
					t.Fatal(err)
				}
				restarted := NewService(ServiceConfig{Repo: repo, TmpSandbox: sb})
				restarted.PauseQueue()
				if err := restarted.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				defer restarted.Close()
				saved, err = repo.GetJob(t.Context(), "job")
				if err != nil {
					t.Fatal(err)
				}
				if phase == "finish" {
					if action == cleanupDelete && saved != nil {
						t.Fatalf("delete not recovered: %+v", saved)
					}
					if action == cleanupRetry && (saved == nil || saved.Status != StatusQueued || saved.CleanupPending != "") {
						t.Fatalf("retry not recovered: %+v", saved)
					}
				} else if saved == nil || saved.Status != StatusError || saved.CleanupPending != "" {
					t.Fatalf("restart executed rejected intent: %+v", saved)
				}
			})
		}
	}
}

func TestQueueCleanupWaitsForWorkerAndIgnoresLateProgress(t *testing.T) {
	svc, repo, sb := cleanupFixture(t)
	file := stageCleanupFile(t, sb, "job/raw/partial.dat")
	job := &Job{ID: "job", Status: StatusQueued}
	if err := repo.CreateJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	svc.AddJob(job)
	claimed, workerCtx, cancel, err := svc.claimNextJob(t.Context())
	if err != nil || claimed == nil {
		t.Fatalf("claim: %+v, %v", claimed, err)
	}
	defer cancel()
	if err := svc.DeleteJob(t.Context(), "job"); !errors.Is(err, errCleanupPending) {
		t.Fatalf("deleting active writer: %v", err)
	}
	if workerCtx.Err() != context.Canceled {
		t.Fatal("worker not cancelled")
	}
	if _, err := safefs.Stat(file); err != nil {
		t.Fatalf("cleaned before writer exited: %v", err)
	}
	job.SetError("late worker error")
	if err := repo.updateProgressSnapshot(t.Context(), &Job{ID: "job", Status: StatusError}); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("late write overwrote intent: %v", err)
	}
	saved, err := repo.GetJob(t.Context(), "job")
	if err != nil || saved.CleanupPending != cleanupDelete {
		t.Fatalf("intent lost: %+v, %v", saved, err)
	}
	if snap := job.Snapshot(); snap.Status != StatusError || snap.Error == nil || *snap.Error != errCleanupPending.Error() {
		t.Fatalf("worker obscured cleanup error: %+v", snap)
	}
	svc.finishJobExecution(t.Context(), job)
	if _, ok := svc.GetJob("job"); ok {
		t.Fatal("worker completion did not finish deletion")
	}
	if _, err := safefs.Stat(file); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file survived writer completion: %v", err)
	}
}

func TestQueueCleanupRetainsPendingImportsAfterRestart(t *testing.T) {
	for _, ext := range []string{"mp3", "lrc", "txt"} {
		for _, status := range []JobStatus{StatusReady, StatusPartialReady, StatusError, StatusCancelled} {
			t.Run(ext+"/"+string(status), func(t *testing.T) {
				svc, repo, sb := cleanupFixture(t)
				file := stageCleanupFile(t, sb, "job/converted/pending."+ext)
				if err := repo.CreateJob(t.Context(), &Job{ID: "job", Status: status}); err != nil {
					t.Fatal(err)
				}
				svc.PauseQueue()
				if err := svc.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				if _, err := svc.RetryJob(t.Context(), "job"); err == nil {
					t.Fatal("retry accepted pending import")
				}
				if finished, err := svc.FinishImport(t.Context(), "job"); err != nil || finished {
					t.Fatalf("finish discarded import: %v, %v", finished, err)
				}
				if count := svc.ClearCompletedJobs(t.Context()); count != 0 {
					t.Fatalf("bulk cleanup discarded import: %d", count)
				}
				if saved, err := repo.GetJob(t.Context(), "job"); err != nil || saved == nil || saved.CleanupPending != "" {
					t.Fatalf("job lost: %+v, %v", saved, err)
				}
				if got, err := safefs.ReadFile(file); err != nil || string(got) != "keep" {
					t.Fatalf("pending import changed: %q, %v", got, err)
				}
			})
		}
	}
}

func TestQueueCleanupManualRetryKeepsStaging(t *testing.T) {
	svc, repo, sb := cleanupFixture(t)
	file := stageCleanupFile(t, sb, "job/converted/pending.mp3")
	index := 0
	job := &Job{ID: "job", Status: StatusError, manualTrackIndex: &index, Tracks: []resolver.ResolvedTrack{{Title: "pending"}}}
	if err := repo.CreateJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	svc.AddJob(job)
	if _, err := svc.RetryJob(t.Context(), "job"); err != nil {
		t.Fatal(err)
	}
	if got, err := safefs.ReadFile(file); err != nil || string(got) != "keep" {
		t.Fatalf("manual retry removed staged track: %q, %v", got, err)
	}
}

func TestQueueCleanupBlocksSourceChanges(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		svc, repo, sb := cleanupFixture(t)
		if !persistent {
			svc.repo = nil
		}
		file := stageCleanupFile(t, sb, "job/raw/partial.dat")
		job := &Job{ID: "job", Status: StatusError, CleanupPending: cleanupDelete,
			Tracks: []resolver.ResolvedTrack{{TrackID: "track", Title: "pending"}}}
		if persistent {
			if err := repo.CreateJob(t.Context(), job); err != nil {
				t.Fatal(err)
			}
		}
		svc.AddJob(job)
		const source = "https://soundcloud.com/artist/track"
		if _, err := svc.SwitchTrackVersion(t.Context(), "track", source); !errors.Is(err, errCleanupPending) {
			t.Fatalf("track source bypassed cleanup: %v", err)
		}
		if _, err := svc.SwitchReleaseSource(t.Context(), SwitchReleaseSourceRequest{JobID: "job", TrackMapping: map[int]string{0: source}}); !errors.Is(err, errCleanupPending) {
			t.Fatalf("release source bypassed cleanup: %v", err)
		}
		if got, err := safefs.ReadFile(file); err != nil || string(got) != "keep" {
			t.Fatalf("source change removed files during cleanup: %q, %v", got, err)
		}
	}
}

func TestQueueCleanupRejectsRequeueWhileWorkerIsExiting(t *testing.T) {
	for _, operation := range []string{"retry", "track source", "release source"} {
		t.Run(operation, func(t *testing.T) {
			svc, repo, _ := cleanupFixture(t)
			manualIndex := 0
			job := &Job{ID: "job", Status: StatusQueued, manualTrackIndex: &manualIndex,
				Tracks: []resolver.ResolvedTrack{{TrackID: "track", Title: "pending"}}}
			if err := repo.CreateJob(t.Context(), job); err != nil {
				t.Fatal(err)
			}
			svc.AddJob(job)
			claimed, _, cancel, err := svc.claimNextJob(t.Context())
			if err != nil || claimed != job {
				t.Fatalf("claim: %p, %v", claimed, err)
			}
			defer cancel()
			job.SetError("worker reached a terminal state before exiting")

			const source = "https://soundcloud.com/artist/track"
			switch operation {
			case "retry":
				_, err = svc.RetryJob(t.Context(), job.ID)
			case "track source":
				_, err = svc.SwitchTrackVersion(t.Context(), "track", source)
			case "release source":
				_, err = svc.SwitchReleaseSource(t.Context(), SwitchReleaseSourceRequest{JobID: job.ID, TrackMapping: map[int]string{0: source}})
			}
			if err == nil {
				t.Fatal("requeued job before its previous worker exited")
			}
			if ids, err := svc.RetryAllFailedJobs(t.Context()); err != nil || len(ids) != 0 {
				t.Fatalf("bulk retry picked up a job whose worker was exiting: %v, %v", ids, err)
			}
			if next, _, _, err := svc.claimNextJob(t.Context()); err != nil || next != nil {
				t.Fatalf("second worker claimed the same job: %p, %v", next, err)
			}
			svc.finishJobExecution(t.Context(), job)
			if _, err := svc.RetryJob(t.Context(), job.ID); err != nil {
				t.Fatalf("retry after worker exit: %v", err)
			}
		})
	}
}

func TestQueueCleanupDoesNotBlockBulkRetry(t *testing.T) {
	for _, action := range []JobStatus{cleanupDelete, cleanupRetry} {
		t.Run(string(action), func(t *testing.T) {
			svc, repo, sb := cleanupFixture(t)
			file := stageCleanupFile(t, sb, "pending/raw/partial.dat")
			for _, job := range []*Job{
				{ID: "pending", Status: StatusError, CleanupPending: action},
				{ID: "failed", Status: StatusError},
			} {
				if err := repo.CreateJob(t.Context(), job); err != nil {
					t.Fatal(err)
				}
				svc.AddJob(job)
			}

			ids, err := svc.RetryAllFailedJobs(t.Context())
			if err != nil || len(ids) != 1 || ids[0] != "failed" {
				t.Fatalf("bulk retry returned %v, %v; want only the failed download", ids, err)
			}
			got, err := repo.GetJob(t.Context(), "failed")
			if err != nil || got == nil || got.Status != StatusQueued {
				t.Fatalf("failed download was not queued: %+v, %v", got, err)
			}
			pending, err := repo.GetJob(t.Context(), "pending")
			if err != nil || pending == nil || pending.CleanupPending != action {
				t.Fatalf("bulk retry changed pending cleanup: %+v, %v", pending, err)
			}
			if _, err := safefs.Stat(file); err != nil {
				t.Fatalf("bulk retry touched pending staging: %v", err)
			}
		})
	}
}

func TestQueueCleanupRetriesInBackground(t *testing.T) {
	svc, repo, sb := cleanupFixture(t)
	file := stageCleanupFile(t, sb, "job/raw/partial.dat")
	dir, err := sb.Resolve("job/raw", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := safefs.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer safefs.Chmod(dir, 0700)
	job := &Job{ID: "job", Status: StatusError}
	if err := repo.CreateJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	svc.AddJob(job)
	if err := svc.DeleteJob(t.Context(), "job"); !errors.Is(err, errCleanupPending) {
		t.Fatalf("expected failed deletion: %v", err)
	}
	svc.cleanerInterval = 10 * time.Millisecond
	svc.PauseQueue()
	if err := svc.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := safefs.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("background cleaner did not finish pending deletion")
		case <-poll.C:
			if saved, err := repo.GetJob(t.Context(), "job"); err != nil {
				t.Fatal(err)
			} else if saved != nil {
				continue
			}
			if _, err := safefs.Stat(file); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("file survived background cleanup: %v", err)
			}
			return
		}
	}
}
