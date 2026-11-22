package downloader

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
	"music-manager/internal/testkit"
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
		CREATE TRIGGER reject_job_selection BEFORE UPDATE OF selection_json ON download_jobs
		WHEN NEW.id='selected'
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
		UPDATE download_jobs SET selection_json='{' WHERE id='selected';
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
		SELECT j.status,j.selection_json FROM download_jobs j
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
	id := old.rememberPreflight(source, "deezer", &resolver.ReleaseMetadata{},
		[]resolver.ResolvedTrack{{Title: "first"}, {Title: "selected"}, {Title: "last"}})
	const jobCount = 8
	for range jobCount {
		_, err := old.CreateJobWithOptions(t.Context(), source, "deezer", JobOptions{PreflightID: id, SelectedTrackIndices: []int{1}})
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
		tracks: []resolver.ResolvedTrack{{Title: "selected"}, {Title: "first"}, {Title: "last"}},
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

func TestQueueWorkerLimitAfterFailedEnqueue(t *testing.T) {
	ctx := t.Context()
	database, err := db.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	conn := database.Conn()
	svc := NewService(ServiceConfig{Repo: NewRepository(conn), MaxConcurrent: 1})
	svc.PauseQueue()
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, err := conn.ExecContext(ctx, `CREATE TRIGGER reject_queue_job BEFORE INSERT ON download_jobs
		BEGIN SELECT RAISE(FAIL, 'test write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateJob(ctx, "https://www.deezer.com/album/123", "deezer"); err == nil {
		t.Fatal("expected failed insertion")
	}
	if _, err := conn.ExecContext(ctx, "DROP TRIGGER reject_queue_job"); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.CreateJob(ctx, "https://www.deezer.com/album/123", "deezer"); err != nil {
		t.Fatal(err)
	}
	saved := svc.GetSortedJobSnapshots()
	if len(saved) != 1 || saved[0].Status != StatusQueued {
		t.Fatalf("unexpected queue: %+v", saved)
	}
	if _, _, _, err := svc.claimNextJob(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.repo.GetJob(ctx, saved[0].ID); err != nil || got.Status != StatusQueued {
		t.Fatalf("paused queue claimed a job: %+v %v", got, err)
	}
	if _, err := svc.CreateJob(ctx, "https://www.deezer.com/album/456", "deezer"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
		CREATE TRIGGER check_shared_worker_limit AFTER UPDATE OF status ON download_jobs
		WHEN NEW.status='RESOLVING' AND (SELECT COUNT(*) FROM download_jobs WHERE status='RESOLVING') > 1
		BEGIN SELECT RAISE(FAIL, 'worker limit exceeded'); END`); err != nil {
		t.Fatal(err)
	}
	svc.ResumeQueue()
	deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var ready int
		if err := conn.QueryRowContext(deadline, "SELECT COUNT(*) FROM download_jobs WHERE status='READY'").Scan(&ready); err != nil {
			t.Fatal(err)
		}
		if ready == 2 {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatal("worker did not finish both jobs")
		case <-tick.C:
		}
	}
}

func TestQueueRecoversLegacyStagedAudio(t *testing.T) {
	ctx := t.Context()
	database, err := db.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	sb, err := sandbox.NewPathSandbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const rel = "legacy/converted/Song%20Mix.mp3"
	plain, err := testkit.CreateDummyMP3(sb, "legacy/converted/Song Mix.mp3", "Song", "Artist", "Album")
	if err != nil {
		t.Fatal(err)
	}
	literal, err := sb.Resolve(rel, false, sandbox.LiteralPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := safefs.Rename(plain, literal); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Conn().ExecContext(ctx, `INSERT INTO download_jobs(id,status,source,url,total_tracks)
		VALUES('legacy','READY','deezer','https://www.deezer.com/album/1',1)`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(database.Conn())
	svc := NewService(ServiceConfig{Repo: repo, TmpSandbox: sb})
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	job, err := repo.GetJob(ctx, "legacy")
	if err != nil || job == nil || job.Status != StatusReady || len(job.Tracks) != 1 ||
		job.Tracks[0].Title != "Song" || job.Tracks[0].ConvertedPath == nil || *job.Tracks[0].ConvertedPath != rel {
		t.Fatalf("recovered job = %+v, %v", job, err)
	}
	if _, err := svc.RetryJob(ctx, "legacy"); err == nil {
		t.Fatal("retry was allowed to remove recovered media")
	}
	music, err := sandbox.NewPathSandbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result, err := organizer.NewService(music, sb).MoveAlbum("legacy", "Album", "Artist",
		[]id3.TrackTags{{Path: *job.Tracks[0].ConvertedPath, Title: "Song", Artist: "Artist", TrackNumber: 1}}, "")
	if err != nil || !result.Complete() || len(result.Tracks) != 1 {
		t.Fatalf("could not import literal staged path: %+v, %v", result, err)
	}
}

func TestQueueRejectsExpiredPreflight(t *testing.T) {
	const url = "https://www.deezer.com/album/123"
	svc := NewService(ServiceConfig{})
	id := svc.rememberPreflight(url, "deezer", &resolver.ReleaseMetadata{}, []resolver.ResolvedTrack{{Title: "Song"}})
	old := svc.preflightCache[id]
	old.expiresAt = time.Now().Add(-time.Second)
	svc.preflightCache[id] = old
	job, err := svc.CreateJobWithOptions(t.Context(), url, "deezer", JobOptions{PreflightID: id, SelectedTrackIndices: []int{0}})
	if !errors.Is(err, errPreflightExpired) || job != nil || len(svc.GetAllJobs()) != 0 {
		t.Fatalf("expired preflight created a job: %+v, %v", job, err)
	}
}

func TestQueueRestoresSavedStatuses(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := t.Context()
	repo := NewRepository(database.Conn())
	for _, status := range []JobStatus{StatusQueued, StatusResolving, StatusDownloading, StatusConverting, StatusTagging,
		StatusWaitingSource, StatusReady, StatusPartialReady, StatusError, StatusCancelled} {
		job := &Job{ID: string(status), Status: status,
			Tracks: []resolver.ResolvedTrack{{Title: "Song", VersionStatus: "manual", AudioSourceURL: "https://soundcloud.com/a/b"}}}
		if err := repo.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}

	svc := NewService(ServiceConfig{Repo: repo})
	svc.PauseQueue()
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	for _, status := range []JobStatus{StatusQueued, StatusResolving, StatusDownloading, StatusConverting, StatusTagging,
		StatusWaitingSource, StatusReady, StatusPartialReady, StatusError, StatusCancelled} {
		want := status
		switch status {
		case StatusResolving, StatusDownloading, StatusConverting, StatusTagging:
			want = StatusError
		}
		job, err := repo.GetJob(ctx, string(status))
		if err != nil || job == nil || job.Status != want || len(job.Tracks) != 1 ||
			job.Tracks[0].AudioSourceURL != "https://soundcloud.com/a/b" {
			t.Fatalf("%s restored as %+v, %v; want %s and saved source", status, job, err, want)
		}
	}
}
