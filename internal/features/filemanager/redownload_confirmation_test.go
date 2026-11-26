package filemanager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/safefs"
	"music-manager/internal/db"
	"music-manager/internal/features/downloader"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
	"music-manager/internal/services/scanner"
	"music-manager/internal/testkit"
)

const (
	confirmOriginal = "Album/01. Original.mp3"
	confirmOldURL   = "https://youtu.be/original"
	confirmNewURL   = "https://soundcloud.com/artist/replacement"
	confirmTrackID  = "selected-track"
)

type redownloadConfirmFixture struct {
	env       *testkit.TestEnv
	organizer *organizer.Service
	config    downloader.ServiceConfig
	queue     *downloader.Service
	tags      id3.TrackTags
}

func newRedownloadConfirmFixture(t *testing.T) *redownloadConfirmFixture {
	t.Helper()
	env := testkit.NewEnv(t)
	scan := scanner.NewScanner(env.MusicSandbox, env.DB, nil, env.TmpSandbox)
	org := organizer.NewService(env.MusicSandbox, env.TmpSandbox)
	org.SetTrackChanger(scan)
	cfg := downloader.ServiceConfig{
		Repo: downloader.NewRepository(env.DB.Conn()), MusicSandbox: env.MusicSandbox, TmpSandbox: env.TmpSandbox,
	}
	queue := downloader.NewService(cfg)
	t.Cleanup(func() { _ = queue.Close() })
	tags := id3.TrackTags{Title: "Original", Artist: "Band", AlbumArtist: "Band", Album: "Album", TrackNumber: 1, SourceURL: confirmOldURL}
	path := env.CreateDummyMP3WithTags(confirmOriginal, tags, nil)
	env.CreateDummyFile(env.MusicSandbox, "Album/01. Original.lrc", []byte("[00:01.00]original lyrics"))
	env.CreateDummyFile(env.MusicSandbox, "Album/01. Original.txt", []byte("original notes"))
	if err := scan.IndexFile(path, confirmOriginal); err != nil {
		t.Fatal(err)
	}
	return &redownloadConfirmFixture{env: env, organizer: org, config: cfg, queue: queue, tags: tags}
}

func (f *redownloadConfirmFixture) stage(t *testing.T, id string, tags id3.TrackTags, target downloader.RedownloadTarget) string {
	t.Helper()
	staged := filepath.Join(id, "converted", "track.mp3")
	full, err := testkit.CreateDummyMP3WithTags(f.env.TmpSandbox, staged, tags, nil)
	if err != nil {
		t.Fatal(err)
	}
	folder := "Album"
	target.TrackID = confirmTrackID
	job := &downloader.Job{
		ID: id, Status: downloader.StatusReady, Source: "redownload", IsRedownload: true,
		TargetAlbumPath: &folder, TotalTracks: 1, CompletedTracks: 1,
		RedownloadTargets: []downloader.RedownloadTarget{target},
		Tracks: []resolver.ResolvedTrack{{
			TrackID: confirmTrackID, Title: tags.Title, TrackNumber: tags.TrackNumber, DiscNumber: 1,
			Status: "ready", ConvertedPath: &staged,
		}},
	}
	if err := f.config.Repo.CreateJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	return full
}

func (f *redownloadConfirmFixture) router(queue RedownloadQueue, importer RedownloadOrganizer) http.Handler {
	router := chi.NewRouter()
	NewModule(NewService(ServiceConfig{
		MusicSandbox: f.env.MusicSandbox, Repo: NewRepository(f.env.DB.Conn()),
		RedownloadQueue: queue, RedownloadOrganizer: importer,
	})).RegisterRoutes(router)
	return router
}

func issueRedownloadConfirm(ctx context.Context, handler http.Handler, id string) *httptest.ResponseRecorder {
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/files/redownload/"+id+"/confirm",
		strings.NewReader(`{"track_ids":["`+confirmTrackID+`"]}`))
	reply := httptest.NewRecorder()
	handler.ServeHTTP(reply, request)
	return reply
}

func readConfirmResult(t *testing.T, reply *httptest.ResponseRecorder) redownloadResult {
	t.Helper()
	if reply.Code != http.StatusOK {
		t.Fatalf("confirmation HTTP %d: %s", reply.Code, reply.Body)
	}
	var body struct{ Results []redownloadResult }
	if err := json.Unmarshal(reply.Body.Bytes(), &body); err != nil || len(body.Results) != 1 {
		t.Fatalf("invalid confirmation result %s: %v", reply.Body, err)
	}
	if body.Results[0].TrackID != confirmTrackID {
		t.Fatalf("confirmation returned wrong track: %+v", body.Results[0])
	}
	return body.Results[0]
}

func (f *redownloadConfirmFixture) storedTarget(t *testing.T, id string) downloader.RedownloadTarget {
	t.Helper()
	job, err := f.config.Repo.GetJob(t.Context(), id)
	if err != nil || job == nil || len(job.RedownloadTargets) != 1 {
		t.Fatalf("unexpected stored job: %+v, %v", job, err)
	}
	return job.RedownloadTargets[0]
}

func (f *redownloadConfirmFixture) assertIndexed(t *testing.T, path, sourceURL string, trackNumber int) {
	t.Helper()
	var count int
	var indexedPath, indexedSource string
	if err := f.env.DB.Conn().QueryRowContext(t.Context(), `
		SELECT COUNT(*), COALESCE(MIN(t.file_path), ''), COALESCE(MIN(t.source_url), '')
		FROM tracks t JOIN albums a ON a.id = t.album_id
		WHERE a.folder_path = 'Album' AND MAX(1, COALESCE(t.disc_number, 1)) = 1 AND t.track_number = ?
	`, trackNumber).Scan(&count, &indexedPath, &indexedSource); err != nil {
		t.Fatal(err)
	}
	if count != 1 || indexedPath != path || indexedSource != sourceURL {
		t.Fatalf("position %d has %d index rows for %q, source %q", trackNumber, count, indexedPath, indexedSource)
	}
}

func (f *redownloadConfirmFixture) assertOriginalCompanions(t *testing.T) {
	t.Helper()
	for ext, want := range map[string]string{".lrc": "[00:01.00]original lyrics", ".txt": "original notes"} {
		full, err := f.env.MusicSandbox.Resolve(strings.TrimSuffix(confirmOriginal, ".mp3")+ext, false)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := safefs.ReadFile(full); err != nil || string(got) != want {
			t.Fatalf("original companion %s = %q, error = %v", ext, got, err)
		}
	}
}

func confirmFileHash(t *testing.T, path string) string {
	t.Helper()
	digest, err := organizer.FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestRedownloadConfirmationRecoversFromSQLiteFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		condition string
		published bool
	}{
		{"before publication", `json_extract(NEW.selection_json, '$.redownload_targets[0].confirmed') IS NULL`, false},
		{"after publication", `json_extract(NEW.selection_json, '$.redownload_targets[0].confirmed') = 1`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRedownloadConfirmFixture(t)
			const jobID = "replacement"
			destination, err := f.env.MusicSandbox.Resolve(confirmOriginal, false)
			if err != nil {
				t.Fatal(err)
			}
			originalHash := confirmFileHash(t, destination)
			tags := f.tags
			tags.SourceURL = confirmNewURL
			staged := f.stage(t, jobID, tags, downloader.RedownloadTarget{LibraryPath: confirmOriginal, OriginalHash: originalHash})
			stagedHash := confirmFileHash(t, staged)
			conn := f.env.DB.Conn()
			if _, err := conn.ExecContext(t.Context(), `
				CREATE TRIGGER reject_confirmation BEFORE UPDATE OF selection_json ON download_jobs
				WHEN NEW.id = 'replacement' AND `+tc.condition+`
				BEGIN SELECT RAISE(ABORT, 'test SQLite confirmation failure'); END
			`); err != nil {
				t.Fatal(err)
			}

			handler := f.router(f.queue, f.organizer)
			result := readConfirmResult(t, issueRedownloadConfirm(t.Context(), handler, jobID))
			if !strings.Contains(result.Error, "test SQLite confirmation failure") || result.Path != "" {
				t.Fatalf("expected SQLite failure for the track, got %+v", result)
			}
			failed := f.storedTarget(t, jobID)
			if failed.Confirmed {
				t.Fatalf("failed confirmation was persisted as successful: %+v", failed)
			}
			publishedHash := confirmFileHash(t, destination)
			if tc.published {
				if failed.ResultPath != confirmOriginal || failed.ResultHash != publishedHash || publishedHash == originalHash {
					t.Fatalf("published MP3 and stored receipt disagree: %+v", failed)
				}
				if _, err := safefs.Stat(staged); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("published staging file still exists: %v", err)
				}
				f.assertIndexed(t, confirmOriginal, confirmNewURL, 1)
			} else {
				if failed.ResultPath != "" || failed.ResultHash != "" || publishedHash != originalHash || confirmFileHash(t, staged) != stagedHash {
					t.Fatalf("receipt failure changed media or the stored receipt: %+v", failed)
				}
				f.assertIndexed(t, confirmOriginal, confirmOldURL, 1)
			}
			f.assertOriginalCompanions(t)

			if _, err := conn.ExecContext(t.Context(), "DROP TRIGGER reject_confirmation"); err != nil {
				t.Fatal(err)
			}
			if err := f.queue.Close(); err != nil {
				t.Fatal(err)
			}
			reloaded := downloader.NewService(f.config)
			t.Cleanup(func() { _ = reloaded.Close() })
			result = readConfirmResult(t, issueRedownloadConfirm(t.Context(), f.router(reloaded, f.organizer), jobID))
			if result.Error != "" || result.Path != confirmOriginal {
				t.Fatalf("retry after queue reload failed: %+v", result)
			}
			got := f.storedTarget(t, jobID)
			finalHash := confirmFileHash(t, destination)
			if !got.Confirmed || got.ResultPath != confirmOriginal || got.ResultHash != finalHash ||
				finalHash == originalHash || (tc.published && finalHash != publishedHash) {
				t.Fatalf("retry left an invalid receipt: %+v", got)
			}
			f.assertIndexed(t, confirmOriginal, confirmNewURL, 1)
			f.assertOriginalCompanions(t)
			if _, err := safefs.Stat(staged); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("confirmed staging file still exists: %v", err)
			}
		})
	}
}

type gatedRedownloadOrganizer struct {
	*organizer.Service
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (g *gatedRedownloadOrganizer) ApplyRedownload(ctx context.Context, req organizer.RedownloadImport, verify func() error, receipt func(string, string) error) (string, error) {
	g.calls.Add(1)
	return g.Service.ApplyRedownload(ctx, req, verify, func(path, digest string) error {
		if err := receipt(path, digest); err != nil {
			return err
		}
		select {
		case g.entered <- struct{}{}:
		default:
		}
		select {
		case <-g.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}

type observedRedownloadQueue struct {
	*downloader.Service
	entered chan struct{}
}

func (q observedRedownloadQueue) ConfirmRedownload(ctx context.Context, id string, apply func(*downloader.Job, func() error) error) error {
	q.entered <- struct{}{}
	return q.Service.ConfirmRedownload(ctx, id, apply)
}

func TestConcurrentRedownloadConfirmationPublishesOnce(t *testing.T) {
	f := newRedownloadConfirmFixture(t)
	const jobID = "concurrent"
	full, err := f.env.MusicSandbox.Resolve(confirmOriginal, false)
	if err != nil {
		t.Fatal(err)
	}
	tags := f.tags
	tags.SourceURL = confirmNewURL
	staged := f.stage(t, jobID, tags, downloader.RedownloadTarget{LibraryPath: confirmOriginal, OriginalHash: confirmFileHash(t, full)})
	queue := observedRedownloadQueue{Service: f.queue, entered: make(chan struct{}, 2)}
	importer := &gatedRedownloadOrganizer{Service: f.organizer, entered: make(chan struct{}, 1), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(importer.release) })
	handler := f.router(queue, importer)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	await := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	replies := make(chan *httptest.ResponseRecorder, 2)
	var requests sync.WaitGroup
	defer func() {
		release()
		requests.Wait()
	}()
	send := func() { replies <- issueRedownloadConfirm(ctx, handler, jobID) }
	requests.Go(send)
	await(queue.entered)
	await(importer.entered)
	requests.Go(send)
	await(queue.entered)
	release()

	for range 2 {
		select {
		case reply := <-replies:
			result := readConfirmResult(t, reply)
			if result.Error != "" || result.Path != confirmOriginal {
				t.Fatalf("concurrent confirmation failed: %+v", result)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	target := f.storedTarget(t, jobID)
	if importer.calls.Load() != 1 || !target.Confirmed || target.ResultPath != confirmOriginal ||
		target.ResultHash != confirmFileHash(t, full) {
		t.Fatalf("publication count=%d, receipt=%+v", importer.calls.Load(), target)
	}
	f.assertIndexed(t, confirmOriginal, confirmNewURL, 1)
	f.assertOriginalCompanions(t)
	if _, err := safefs.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmed staging file still exists: %v", err)
	}
}

func TestRedownloadSecondJobCannotFillOccupiedPosition(t *testing.T) {
	f := newRedownloadConfirmFixture(t)
	conn := f.env.DB.Conn()
	var albumID, revision int64
	if err := conn.QueryRowContext(t.Context(), "SELECT id FROM albums WHERE folder_path = 'Album'").Scan(&albumID); err != nil {
		t.Fatal(err)
	}
	catalog := []db.CatalogTrack{
		{BackfillSelection: db.BackfillSelection{TrackPosition: db.ReleasePosition(1, 1)}, Title: f.tags.Title, Artist: f.tags.Artist},
		{BackfillSelection: db.BackfillSelection{TrackPosition: db.ReleasePosition(1, 2)}, Title: "Missing", Artist: f.tags.Artist},
	}
	if _, err := db.VerifyCatalog(t.Context(), conn, albumID, "42", catalog); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(t.Context(), "SELECT catalog_revision FROM albums WHERE id = ?", albumID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	target := downloader.RedownloadTarget{AlbumID: albumID, CatalogRevision: revision}
	tags := f.tags
	tags.Title, tags.TrackNumber, tags.SourceURL = "Missing", 2, confirmNewURL
	f.stage(t, "first-job", tags, target)
	tags.SourceURL = "https://soundcloud.com/artist/other"
	secondStage := f.stage(t, "second-job", tags, target)
	secondHash := confirmFileHash(t, secondStage)
	handler := f.router(f.queue, f.organizer)

	result := readConfirmResult(t, issueRedownloadConfirm(t.Context(), handler, "first-job"))
	const destination = "Album/02. Missing.mp3"
	if result.Path != destination || result.Error != "" {
		t.Fatalf("first task failed to fill position: %+v", result)
	}
	full, err := f.env.MusicSandbox.Resolve(destination, false)
	if err != nil {
		t.Fatal(err)
	}
	hash := confirmFileHash(t, full)
	result = readConfirmResult(t, issueRedownloadConfirm(t.Context(), handler, "second-job"))
	if result.Path != "" || !strings.Contains(result.Error, "файл в этой позиции изменился") {
		t.Fatalf("second task did not reject occupied position: %+v", result)
	}
	if hash != confirmFileHash(t, full) || secondHash != confirmFileHash(t, secondStage) {
		t.Fatal("second confirmation changed the published or staged audio")
	}
	second := f.storedTarget(t, "second-job")
	if second.Confirmed || second.ResultPath != "" || second.ResultHash != "" {
		t.Fatalf("rejected second task recorded a publication: %+v", second)
	}
	f.assertIndexed(t, destination, confirmNewURL, 2)
}
