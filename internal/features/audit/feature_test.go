package audit_test

import (
	"context"
	"music-manager/internal/db"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/auth"
	"music-manager/internal/features/audit"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/scanner"
	"music-manager/internal/testkit"
)

type mockBackfillStarter struct {
	jobID string
	calls []string
}

func (m *mockBackfillStarter) CreateBackfillJob(
	ctx context.Context,
	targetAlbumPath, albumTitle, artistName, deezerAlbumID string,
	trackURLs []string,
	trackNumbers []int,
	positions ...db.BackfillSelection,
) (string, error) {
	m.calls = append(m.calls, albumTitle)
	return m.jobID, nil
}

type mockJobStarter struct {
	jobID string
	calls []string
}

func (m *mockJobStarter) CreateJob(ctx context.Context, rawURL, source string) (string, error) {
	m.calls = append(m.calls, rawURL)
	return m.jobID, nil
}

func setupAuditTest(t *testing.T) (*testkit.TestEnv, *audit.Module, *mockBackfillStarter) {
	t.Helper()
	env := testkit.NewEnv(t)

	mockBackfill := &mockBackfillStarter{
		jobID: "backfill-job-12345",
	}
	mockJob := &mockJobStarter{
		jobID: "standard-job-12345",
	}

	orgSvc := organizer.NewService(env.MusicSandbox, env.TmpSandbox)
	scannerSvc := scanner.NewScanner(env.MusicSandbox, env.DB, nil)
	repo := audit.NewRepository(env.DB.Conn())

	svc := audit.NewService(audit.ServiceConfig{
		MusicSandbox:    env.MusicSandbox,
		TmpSandbox:      env.TmpSandbox,
		Repo:            repo,
		DB:              env.DB.Conn(),
		JobStarter:      mockJob,
		BackfillStarter: mockBackfill,
		Organizer:       orgSvc,
		Scanner:         scannerSvc,
	})

	mod := audit.NewModule(svc)

	r := chi.NewRouter()
	authMiddleware := auth.Middleware(env.SecretKey)

	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		mod.RegisterRoutes(r)
	})

	env.Mount(r)
	env.SetAuthUser("admin")

	return env, mod, mockBackfill
}

func TestAudit_UnauthorizedAccess(t *testing.T) {
	env, _, _ := setupAuditTest(t)
	env.ClearAuth()

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/files/audit/summary"},
		{http.MethodGet, "/api/audit/summary"},
		{http.MethodGet, "/files/audit/duplicates"},
		{http.MethodGet, "/duplicates"},
		{http.MethodGet, "/files/audit/tag-issues"},
		{http.MethodGet, "/tags/audit"},
		{http.MethodPost, "/files/audit/fix-all-paths"},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			req := env.NewRequest(ep.method, ep.path, nil)
			resp := env.Do(req)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("expected 401 Unauthorized for %s, got %d", ep.path, resp.StatusCode)
			}
		})
	}
}

func TestAudit_SummaryAndGoldenSnapshot(t *testing.T) {
	env, _, _ := setupAuditTest(t)

	env.CreateDummyMP3("Album 1 - Artist A/01. Track One.mp3", "Track One", "Artist A", "Album 1")
	env.CreateDummyMP3("Album 2 - Artist B/01. Track Two.mp3", "Track Two", "Artist B", "Album 2")

	scannerSvc := scanner.NewScanner(env.MusicSandbox, env.DB, nil)
	_, err := scannerSvc.ScanAll(context.Background())
	if err != nil {
		t.Fatalf("failed to scan music files: %v", err)
	}

	resp := env.Get("/files/audit/summary")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	body := env.ReadBody(resp)

	var summary audit.AuditSummaryResponse
	env.DecodeJSON(env.Get("/files/audit/summary"), &summary)

	if summary.TotalTracks != 2 {
		t.Fatalf("expected 2 tracks, got %d", summary.TotalTracks)
	}
	if summary.TotalAlbums != 2 {
		t.Fatalf("expected 2 albums, got %d", summary.TotalAlbums)
	}

	env.AssertGoldenJSON(body, "testdata/audit_summary.golden.json")

	aliasResp := env.Get("/api/audit/summary")
	if aliasResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on alias, got %d", aliasResp.StatusCode)
	}
	aliasBody := env.ReadBody(aliasResp)
	if string(body) != string(aliasBody) {
		t.Fatalf("expected alias response to match canonical response exactly")
	}
}

func TestAudit_Duplicates_DetectionAndResolution(t *testing.T) {
	env, _, _ := setupAuditTest(t)

	env.CreateDummyMP3("Artist A/Album 1/01. Hit Song.mp3", "Hit Song", "Artist A", "Album 1")
	env.CreateDummyMP3("Artist A/Album 1/01. Hit Song (1).mp3", "Hit Song", "Artist A", "Album 1")

	env.CreateDummyFile(env.MusicSandbox, "Artist A/Album 1/01. Hit Song (1).lrc", []byte("[00:01.00] Duplicate lyrics"))

	scannerSvc := scanner.NewScanner(env.MusicSandbox, env.DB, nil)
	_, _ = scannerSvc.ScanAll(context.Background())

	resp := env.Get("/files/audit/duplicates")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var dupResp audit.DuplicatesResponse
	env.DecodeJSON(resp, &dupResp)

	if dupResp.TotalDuplicateFiles < 1 {
		t.Fatalf("expected at least 1 duplicate file, got %d", dupResp.TotalDuplicateFiles)
	}
	if len(dupResp.Groups) == 0 {
		t.Fatal("expected non-empty duplicate groups")
	}

	aliasResp := env.Get("/duplicates")
	if aliasResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on /duplicates alias, got %d", aliasResp.StatusCode)
	}

	toDelete := "Artist A/Album 1/01. Hit Song (1).mp3"
	fixReq := audit.FixDuplicatesRequest{
		GroupKey:      dupResp.Groups[0].Key,
		AlbumPath:     "Artist A/Album 1",
		PathsToDelete: []string{toDelete},
	}

	fixResp := env.PostJSON("/files/audit/duplicates/fix", fixReq)
	if fixResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on fix, got %d", fixResp.StatusCode)
	}

	var fixResult audit.FixDuplicatesResponse
	env.DecodeJSON(fixResp, &fixResult)

	if !fixResult.Success {
		t.Fatalf("expected success: true, errors: %v", fixResult.Errors)
	}
	if fixResult.DeletedCount != 1 {
		t.Fatalf("expected 1 deleted file, got %d", fixResult.DeletedCount)
	}

	absDeleted, _ := env.MusicSandbox.Resolve(toDelete, false)
	if _, err := os.Stat(absDeleted); !os.IsNotExist(err) {
		t.Fatalf("expected duplicate MP3 file to be deleted: %s", absDeleted)
	}

	absLrc := strings.TrimSuffix(absDeleted, ".mp3") + ".lrc"
	if _, err := os.Stat(absLrc); !os.IsNotExist(err) {
		t.Fatalf("expected companion .lrc file to be deleted: %s", absLrc)
	}

	absKeep, _ := env.MusicSandbox.Resolve("Artist A/Album 1/01. Hit Song.mp3", false)
	if _, err := os.Stat(absKeep); err != nil {
		t.Fatalf("expected canonical file to remain: %s", absKeep)
	}
}

func TestAudit_Duplicates_FixAll(t *testing.T) {
	env, _, _ := setupAuditTest(t)

	env.CreateDummyMP3("Artist A/Album 1/01. Anthem.mp3", "Anthem", "Artist A", "Album 1")
	env.CreateDummyMP3("Artist A/Album 1/01. Anthem (1).mp3", "Anthem", "Artist A", "Album 1")

	scannerSvc := scanner.NewScanner(env.MusicSandbox, env.DB, nil)
	_, _ = scannerSvc.ScanAll(context.Background())

	fixAllResp := env.PostJSON("/files/audit/duplicates/fix-all", nil)
	if fixAllResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on fix-all, got %d", fixAllResp.StatusCode)
	}

	var fixResult audit.FixDuplicatesResponse
	env.DecodeJSON(fixAllResp, &fixResult)

	if !fixResult.Success {
		t.Fatalf("expected success: true, errors: %v", fixResult.Errors)
	}
	if fixResult.DeletedCount != 1 {
		t.Fatalf("expected 1 deleted file, got %d", fixResult.DeletedCount)
	}

	canonicalAbs, _ := env.MusicSandbox.Resolve("Artist A/Album 1/01. Anthem.mp3", false)
	if _, err := os.Stat(canonicalAbs); err != nil {
		t.Fatalf("expected canonical file to remain: %s", canonicalAbs)
	}
	dupAbs, _ := env.MusicSandbox.Resolve("Artist A/Album 1/01. Anthem (1).mp3", false)
	if _, err := os.Stat(dupAbs); !os.IsNotExist(err) {
		t.Fatalf("expected duplicate file to be removed: %s", dupAbs)
	}
}

func TestAudit_TagIssues_DetectionAndSafeAutofix(t *testing.T) {
	env, _, _ := setupAuditTest(t)

	relPath := "Artist/Album/01. Dirty Song [Official Video] (Lyrics).mp3"
	env.CreateDummyMP3(relPath, "01. Dirty Song [Official Video] (Lyrics)", "Artist", "Album")

	scannerSvc := scanner.NewScanner(env.MusicSandbox, env.DB, nil)
	_, _ = scannerSvc.ScanAll(context.Background())

	resp := env.Get("/files/audit/tag-issues")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var issuesResp audit.TagIssuesResponse
	env.DecodeJSON(resp, &issuesResp)

	if issuesResp.TotalCount == 0 {
		t.Fatal("expected tag issues to be detected")
	}

	fixSafeResp := env.PostJSON("/files/audit/tag-issues/fix-safe", nil)
	if fixSafeResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on fix-safe, got %d", fixSafeResp.StatusCode)
	}

	var batchFix audit.FixBatchResponse
	env.DecodeJSON(fixSafeResp, &batchFix)

	if !batchFix.Success {
		t.Fatalf("expected success: true, errors: %v", batchFix.Errors)
	}
	if batchFix.FixedCount < 1 {
		t.Fatalf("expected at least 1 file fixed, got %d", batchFix.FixedCount)
	}

	absPath, _ := env.MusicSandbox.Resolve(relPath, false)
	tags, err := id3.ReadTags(absPath, relPath)
	if err != nil {
		t.Fatalf("failed to read updated tags: %v", err)
	}

	if tags.Title != "Dirty Song" {
		t.Fatalf("expected title 'Dirty Song', got %q", tags.Title)
	}
}

func TestAudit_PathTraversalRejection(t *testing.T) {
	env, _, _ := setupAuditTest(t)

	// 1. POST /files/audit/fix-path with path traversal
	fixReq := audit.FixPathRequest{
		Path: "../../etc/passwd",
	}
	resp := env.PostJSON("/files/audit/fix-path", fixReq)
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 403 or 404 for path traversal in fix-path, got %d", resp.StatusCode)
	}

	// 2. POST /files/audit/tag-issues/fix with traversal
	tagReq := audit.FixTagIssueRequest{
		Path:  "../../../etc/shadow",
		Title: "Hacked",
	}
	tagResp := env.PostJSON("/files/audit/tag-issues/fix", tagReq)
	if tagResp.StatusCode != http.StatusForbidden && tagResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 403 or 404 for path traversal in tag fix, got %d", tagResp.StatusCode)
	}
}

func TestAudit_InvalidPaths_Fix(t *testing.T) {
	env, _, _ := setupAuditTest(t)

	relPath := "Downloads/01. Favorite Song.mp3"
	env.CreateDummyMP3(relPath, "Favorite Song", "The Artist", "Great Album")

	scannerSvc := scanner.NewScanner(env.MusicSandbox, env.DB, nil)
	_, _ = scannerSvc.ScanAll(context.Background())

	// Mark as invalid path in database
	expectedTarget := filepath.Join("Great Album - The Artist", "01. Favorite Song.mp3")
	_, err := env.DB.Conn().Exec("UPDATE tracks SET is_path_valid = 0, suggested_path = ?, path_violation_reason = 'non_canonical' WHERE file_path = ?", filepath.ToSlash(expectedTarget), relPath)
	if err != nil {
		t.Fatalf("failed to setup invalid path in db: %v", err)
	}

	resp := env.Get("/files/audit/invalid-paths")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var invalidPaths []audit.InvalidPathItem
	env.DecodeJSON(resp, &invalidPaths)

	if len(invalidPaths) == 0 {
		t.Fatal("expected invalid path to be found")
	}

	fixReq := audit.FixPathRequest{
		Path:      relPath,
		Overwrite: true,
	}
	fixResp := env.PostJSON("/files/audit/fix-path", fixReq)
	if fixResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", fixResp.StatusCode)
	}

	var fixRes audit.FixPathResponse
	env.DecodeJSON(fixResp, &fixRes)

	if !fixRes.Success {
		t.Fatalf("expected success: true")
	}

	if fixRes.TargetPath != filepath.ToSlash(expectedTarget) {
		t.Fatalf("expected target path %q, got %q", expectedTarget, fixRes.TargetPath)
	}

	oldDirAbs, _ := env.MusicSandbox.Resolve("Downloads", false)
	if _, err := os.Stat(oldDirAbs); !os.IsNotExist(err) {
		t.Fatalf("expected empty old dir to be pruned: %s", oldDirAbs)
	}
}

func TestAudit_IncompleteAlbums_DownloadMissing(t *testing.T) {
	env, _, mockBackfill := setupAuditTest(t)

	env.CreateDummyMP3("Artist/Incomplete Album/01. Track One.mp3", "Track One", "Artist", "Incomplete Album")

	scannerSvc := scanner.NewScanner(env.MusicSandbox, env.DB, nil)
	_, _ = scannerSvc.ScanAll(context.Background())

	_, _ = env.DB.Conn().Exec("UPDATE albums SET expected_tracks_count = 2, total_tracks = 2, is_complete = 0")

	dzID := "123456"
	req := audit.DownloadMissingRequest{
		Path:          "Artist/Incomplete Album",
		DeezerAlbumID: &dzID,
		TrackNumbers:  []int{2},
	}

	resp := env.PostJSON("/files/audit/incomplete-albums/download-missing", req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var jobs []*audit.BackfillJobInfo
	env.DecodeJSON(resp, &jobs)

	if len(jobs) == 0 {
		t.Fatal("expected at least 1 backfill job returned")
	}
	if jobs[0].ID != "backfill-job-12345" {
		t.Fatalf("expected job ID 'backfill-job-12345', got %q", jobs[0].ID)
	}
	if len(mockBackfill.calls) == 0 {
		t.Fatal("expected mock backfill starter to receive call")
	}
}
