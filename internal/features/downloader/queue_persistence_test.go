package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
)

func TestJobSelectionRollbackAndCorruptionIsolation(t *testing.T) {
	ctx := t.Context()
	database, err := db.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	conn := database.Conn()
	repo := NewRepository(conn)
	old := &Job{
		ID: "selected", Status: StatusWaitingSource,
		Tracks:               []resolver.ResolvedTrack{{Title: "old track"}},
		SelectedTrackIndices: []int{2}, SkipExisting: true,
	}
	for _, job := range []*Job{{ID: "before", Status: StatusQueued}, old, {ID: "other", Status: StatusQueued}} {
		if err := repo.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.ExecContext(ctx, `
		CREATE TRIGGER reject_job_selection BEFORE UPDATE ON download_source_selections
		WHEN NEW.job_id='selected'
		BEGIN SELECT RAISE(FAIL, 'test selection write rejected'); END`); err != nil {
		t.Fatal(err)
	}
	newSnapshot := old.Snapshot()
	newSnapshot.Status = StatusReady
	newSnapshot.Tracks[0].Title = "new track"
	newSnapshot.SelectedTrackIndices = []int{1}
	if err := repo.SaveJob(ctx, newSnapshot); err == nil {
		t.Fatal("expected JSON write to fail")
	}
	got, err := repo.GetJob(ctx, old.ID)
	if err != nil || got == nil || got.Status != old.Status || len(got.Tracks) != 1 ||
		got.Tracks[0].Title != "old track" || !slices.Equal(got.SelectedTrackIndices, []int{2}) || !got.SkipExisting {
		t.Fatalf("previous snapshot was not preserved: %+v, %v", got, err)
	}

	if _, err := conn.ExecContext(ctx, `
		DROP TRIGGER reject_job_selection;
		UPDATE download_source_selections SET payload_json='{' WHERE job_id='selected';
		UPDATE download_jobs SET status='QUEUED' WHERE id='selected';`); err != nil {
		t.Fatal(err)
	}
	list, err := repo.GetAllJobs(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("corruption hid jobs: %+v, %v", list, err)
	}
	for _, job := range list {
		if job.ID == "selected" {
			if job.Status != StatusError || job.Error == nil || *job.Error != ErrCorruptJobSelection.Error() {
				t.Fatalf("corrupt job was not marked: %+v", job)
			}
		} else if job.Status != StatusQueued {
			t.Fatalf("unrelated job was changed: %+v", job)
		}
	}
	for range list {
		if job, err := repo.ClaimNextJob(ctx); err != nil || job == nil {
			t.Fatalf("claim failed with corrupt sibling: %+v, %v", job, err)
		}
	}
	var status, payload string
	if err := conn.QueryRowContext(ctx, `
		SELECT j.status,s.payload_json FROM download_jobs j JOIN download_source_selections s ON s.job_id=j.id
		WHERE j.id='selected'`).Scan(&status, &payload); err != nil || status != string(StatusError) || payload != "{" {
		t.Fatalf("corrupt job DB state = %q, %q, %v", status, payload, err)
	}
}

func TestQueueDrainsAfterRestartWithFullWakeChannel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	const source = "https://www.deezer.com/album/123"
	old := NewService(ServiceConfig{Repo: NewRepository(database.Conn()), MaxConcurrent: 2})
	const jobCount = 8
	for range jobCount {
		_, err := old.CreateJobWithOptions(t.Context(), source, "deezer", JobOptions{SelectedTrackIndices: []int{1}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(old.wake) != cap(old.wake) {
		t.Fatal("fixture wake channel did not fill")
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	conn := reopened.Conn()
	if _, err := conn.ExecContext(t.Context(), `
		CREATE TABLE test_download_claims(job_id TEXT PRIMARY KEY);
		CREATE TRIGGER count_download_claim AFTER UPDATE OF status ON download_jobs
		WHEN OLD.status='QUEUED' AND NEW.status='RESOLVING'
		BEGIN INSERT INTO test_download_claims(job_id) VALUES(NEW.id); END;`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(conn)
	svc := NewService(ServiceConfig{Repo: repo, Resolver: resolver.NewSourceResolver(nil, nil, ""), MaxConcurrent: 2})
	// Resolving from the cache keeps the complete pipeline local. Each chosen
	// track must reach WAITING_SOURCE without invoking any media tool.
	svc.resolveCache["deezer:"+source] = &cachedResolveResult{
		meta: &resolver.ReleaseMetadata{}, expiresAt: time.Now().Add(time.Minute),
		tracks: []resolver.ResolvedTrack{{Title: "first"}, {Title: "selected"}, {Title: "last"}},
	}
	if err := svc.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		jobs, err := repo.GetAllJobs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ready := 0
		for _, job := range jobs {
			if job.Status != StatusWaitingSource {
				continue
			}
			ready++
			if len(job.Tracks) != 1 || job.Tracks[0].Title != "selected" {
				t.Fatalf("selective download became %+v", job.Tracks)
			}
		}
		if ready == jobCount {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("drained %d/%d jobs", ready, jobCount)
		case <-ticker.C:
		}
	}
	var claims int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM test_download_claims").Scan(&claims); err != nil || claims != jobCount {
		t.Fatalf("claims = %d, want %d, err=%v", claims, jobCount, err)
	}
}

func TestQueueReturnsInsertFailureToHTTP(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	conn := database.Conn()
	if _, err := conn.ExecContext(t.Context(), `
		CREATE TRIGGER reject_job BEFORE INSERT ON download_jobs
		WHEN NEW.url='https://www.deezer.com/album/123'
		BEGIN SELECT RAISE(FAIL, 'test insert rejected'); END`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(conn)
	service := NewService(ServiceConfig{Repo: repo})
	module := NewModule(service)
	rec := httptest.NewRecorder()
	module.CreateJob(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/downloads",
		strings.NewReader(`{"url":"https://www.deezer.com/album/123","source":"deezer"}`)))
	if rec.Code != http.StatusInternalServerError || len(service.GetAllJobs()) != 0 || len(service.wake) != 0 {
		t.Fatalf("HTTP confirmed a failed insert: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	module.CreateBatch(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/downloads/batch",
		strings.NewReader(`{"urls":["https://www.deezer.com/album/456","https://www.deezer.com/album/123"],"source":"deezer"}`)))
	saved, err := repo.GetAllJobs(t.Context())
	if err != nil || rec.Code != http.StatusInternalServerError || len(saved) != 0 || len(service.GetAllJobs()) != 0 {
		t.Fatalf("failed batch partially queued: HTTP %d, saved %d, err %v", rec.Code, len(saved), err)
	}
}

func TestConcurrentRepositoriesClaimOnlyOnce(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := t.Context()
	first := NewRepository(database.Conn())
	second := NewRepository(database.Conn())
	if err := first.CreateJob(ctx, &Job{ID: "only-job", Status: StatusQueued}); err != nil {
		t.Fatal(err)
	}
	type claim struct {
		job *Job
		err error
	}
	claims := make(chan claim, 2)
	start := make(chan struct{})
	for _, repo := range []*Repository{first, second} {
		go func() {
			<-start
			job, err := repo.ClaimNextJob(ctx)
			claims <- claim{job, err}
		}()
	}
	close(start)
	var claimed int
	for range 2 {
		result := <-claims
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.job != nil {
			claimed++
			if result.job.ID != "only-job" || result.job.Status != StatusResolving {
				t.Fatalf("unexpected claimed job: %+v", result.job)
			}
		}
	}
	if claimed != 1 {
		t.Fatalf("%d workers claimed one job", claimed)
	}
}
