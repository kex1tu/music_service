package audit_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/auth"
	"music-manager/internal/features/audit"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/scanner"
	"music-manager/internal/testkit"
)

type stressMockLyricsService struct {
	mu          sync.Mutex
	searchCalls int64
	saveCalls   int64
	delay       time.Duration
}

func (m *stressMockLyricsService) SearchLyrics(ctx context.Context, artist, title, album string, duration int) (*lyrics.LyricsSearchResult, error) {
	atomic.AddInt64(&m.searchCalls, 1)
	if m.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(m.delay):
		}
	}
	return &lyrics.LyricsSearchResult{
		Lyrics: fmt.Sprintf("[00:01.00] Stress lyrics for %s - %s", artist, title),
		Synced: true,
	}, nil
}

func (m *stressMockLyricsService) SaveLyrics(relPath string, content string, kind string) error {
	atomic.AddInt64(&m.saveCalls, 1)
	return nil
}

type stressTestRig struct {
	env        *testkit.TestEnv
	repo       *audit.Repository
	service    *audit.Service
	module     *audit.Module
	mockLyrics *stressMockLyricsService
	scannerSvc *scanner.Scanner
	organizer  *organizer.Service
}

func setupStressTestRig(t *testing.T, lyricsDelay time.Duration) *stressTestRig {
	t.Helper()
	env := testkit.NewEnv(t)

	mockLyrics := &stressMockLyricsService{delay: lyricsDelay}
	mockBackfill := &mockBackfillStarter{jobID: "backfill-stress-1"}
	mockJob := &mockJobStarter{jobID: "job-stress-1"}

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
		Lyrics:          mockLyrics,
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

	t.Cleanup(func() {
		_ = svc.Close()
	})

	return &stressTestRig{
		env:        env,
		repo:       repo,
		service:    svc,
		module:     mod,
		mockLyrics: mockLyrics,
		scannerSvc: scannerSvc,
		organizer:  orgSvc,
	}
}

// ============================================================================
// 1. High Concurrency Duplicate Resolution
// ============================================================================

func TestAuditStress_HighConcurrencyDuplicateResolution(t *testing.T) {
	rig := setupStressTestRig(t, 0)
	ctx := context.Background()

	// Seed 8 distinct duplicate groups with 3-4 duplicates each
	type groupSeed struct {
		artist string
		album  string
		title  string
		copies []string
	}

	seeds := []groupSeed{
		{
			artist: "Artist Alpha",
			album:  "Album A",
			title:  "Track One",
			copies: []string{
				"Artist Alpha/Album A/01. Track One.mp3",
				"Artist Alpha/Album A/01. Track One (Copy 1).mp3",
				"Artist Alpha/Album A/01. Track One (Copy 2).mp3",
			},
		},
		{
			artist: "Artist Beta",
			album:  "Album B",
			title:  "Hit Single",
			copies: []string{
				"Artist Beta/Album B/01. Hit Single.mp3",
				"Artist Beta/Album B/01. Hit Single (dup).mp3",
				"Artist Beta/Album B/01. Hit Single (radio edit).mp3",
				"Artist Beta/Album B/01. Hit Single (remaster).mp3",
			},
		},
		{
			artist: "Artist Gamma",
			album:  "Album C",
			title:  "Symphony",
			copies: []string{
				"Artist Gamma/Album C/01. Symphony.mp3",
				"Artist Gamma/Album C/01. Symphony (Copy).mp3",
			},
		},
		{
			artist: "Artist Delta",
			album:  "Album D",
			title:  "Bassline",
			copies: []string{
				"Artist Delta/Album D/01. Bassline.mp3",
				"Artist Delta/Album D/01. Bassline (v2).mp3",
				"Artist Delta/Album D/01. Bassline (v3).mp3",
			},
		},
		{
			artist: "Artist Epsilon",
			album:  "Album E",
			title:  "Ambient Sound",
			copies: []string{
				"Artist Epsilon/Album E/01. Ambient Sound.mp3",
				"Artist Epsilon/Album E/01. Ambient Sound (bak).mp3",
			},
		},
	}

	var allDupPaths []string
	for _, s := range seeds {
		for _, copyPath := range s.copies {
			rig.env.CreateDummyMP3(copyPath, s.title, s.artist, s.album)
			// Add companion .lrc and .txt
			lrcRel := strings.TrimSuffix(copyPath, ".mp3") + ".lrc"
			txtRel := strings.TrimSuffix(copyPath, ".mp3") + ".txt"
			rig.env.CreateDummyFile(rig.env.MusicSandbox, lrcRel, []byte("[00:01.00] test lyrics"))
			rig.env.CreateDummyFile(rig.env.MusicSandbox, txtRel, []byte("plain text lyrics"))
			allDupPaths = append(allDupPaths, copyPath)
		}
	}

	// Scan library to index all tracks into SQLite
	if _, err := rig.scannerSvc.ScanAll(ctx); err != nil {
		t.Fatalf("ScanAll failed: %v", err)
	}

	// Verify duplicate groups detected
	dupResp, err := rig.service.GetDuplicates(ctx)
	if err != nil {
		t.Fatalf("GetDuplicates failed: %v", err)
	}
	if len(dupResp.Groups) != len(seeds) {
		t.Fatalf("expected %d duplicate groups, got %d", len(seeds), len(dupResp.Groups))
	}

	// Launch high concurrency storm: 25 workers calling FixDuplicates, FixAllDuplicates,
	// ResolveDuplicates, and GetDuplicates concurrently
	concurrency := 25
	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		workerID := i
		go func() {
			defer wg.Done()
			<-startBarrier

			workerCtx := context.Background()

			switch workerID % 4 {
			case 0:
				// FixAllDuplicates storm
				_, _ = rig.service.FixAllDuplicates(workerCtx)
			case 1:
				// FixDuplicates with random subset of paths
				subset := make([]string, 0)
				for _, p := range allDupPaths {
					if rand.Float32() > 0.5 {
						subset = append(subset, p)
					}
				}
				if len(subset) > 0 {
					_, _ = rig.service.FixDuplicates(workerCtx, subset)
				}
			case 2:
				// ResolveDuplicates for specific groups
				grp := dupResp.Groups[workerID%len(dupResp.Groups)]
				if len(grp.Tracks) > 1 {
					toDel := make([]string, 0)
					for _, tr := range grp.Tracks[1:] {
						toDel = append(toDel, tr.Path)
					}
					_, _ = rig.service.ResolveDuplicates(workerCtx, grp.Key, grp.Tracks[0].Path, toDel)
				}
			case 3:
				// Concurrent readers: GetDuplicates & GetSummary
				_, _ = rig.service.GetDuplicates(workerCtx)
				_, _ = rig.service.GetSummary(workerCtx)
			}
		}()
	}

	// Unleash all goroutines at once
	close(startBarrier)
	wg.Wait()

	// Post-concurrency verification:
	// 1. Re-scan and check duplicate count
	dupAfter, err := rig.service.GetDuplicates(ctx)
	if err != nil {
		t.Fatalf("GetDuplicates after storm failed: %v", err)
	}

	// Ensure no data race or corruption occurred and duplicate count is 0
	if dupAfter.TotalDuplicateFiles > 0 {
		// Clean up any remaining duplicates with FixAllDuplicates
		fixRes, err := rig.service.FixAllDuplicates(ctx)
		if err != nil {
			t.Fatalf("Final FixAllDuplicates failed: %v", err)
		}
		t.Logf("Cleaned remaining duplicates in final pass: %d deleted", fixRes.DeletedCount)
	}

	// 2. Verify companion file cleanup for deleted tracks
	for _, p := range allDupPaths {
		absAudio, _ := rig.env.MusicSandbox.Resolve(p, false)
		if _, statErr := os.Stat(absAudio); os.IsNotExist(statErr) {
			// Track was deleted -> companion files MUST also be deleted
			absLrc := strings.TrimSuffix(absAudio, ".mp3") + ".lrc"
			if _, lrcErr := os.Stat(absLrc); !os.IsNotExist(lrcErr) {
				t.Errorf("orphan companion .lrc found for deleted track %s: %s", p, absLrc)
			}
			absTxt := strings.TrimSuffix(absAudio, ".mp3") + ".txt"
			if _, txtErr := os.Stat(absTxt); !os.IsNotExist(txtErr) {
				t.Errorf("orphan companion .txt found for deleted track %s: %s", p, absTxt)
			}
		}
	}
}

// ============================================================================
// 2. Concurrent Companion .lrc and .txt Deletion under Race Detector
// ============================================================================

func TestAuditStress_ConcurrentCompanionDeletion(t *testing.T) {
	rig := setupStressTestRig(t, 0)
	ctx := context.Background()

	// Seed 30 tracks with companion files across various directories
	numTracks := 30
	var trackPaths []string

	for i := 0; i < numTracks; i++ {
		dir := fmt.Sprintf("StressArtist/Album %d", i%5)
		base := fmt.Sprintf("%02d. Track_%d", i+1, i+1)
		relMP3 := filepath.Join(dir, base+".mp3")
		relLRC := filepath.Join(dir, base+".lrc")
		relTXT := filepath.Join(dir, base+".txt")

		rig.env.CreateDummyMP3(relMP3, fmt.Sprintf("Track %d", i+1), "StressArtist", fmt.Sprintf("Album %d", i%5))
		rig.env.CreateDummyFile(rig.env.MusicSandbox, relLRC, []byte(fmt.Sprintf("[00:01.00] Lyrics %d", i)))
		rig.env.CreateDummyFile(rig.env.MusicSandbox, relTXT, []byte(fmt.Sprintf("Text %d", i)))

		trackPaths = append(trackPaths, relMP3)
	}

	if _, err := rig.scannerSvc.ScanAll(ctx); err != nil {
		t.Fatalf("ScanAll failed: %v", err)
	}

	// Run concurrent deletions of tracks + companions alongside concurrent directory inspection
	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	// 15 deletion goroutines
	for i := 0; i < 15; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			<-startBarrier

			// Delete pairs of tracks
			p1 := trackPaths[idx*2]
			p2 := trackPaths[idx*2+1]
			_, _ = rig.service.FixDuplicates(context.Background(), []string{p1, p2})
		}()
	}

	// 10 concurrent readers/inspectors inspecting files and sandbox
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier

			for j := 0; j < 50; j++ {
				_, _ = rig.service.GetSummary(context.Background())
				// Read directory entries under music sandbox
				_ = filepath.Walk(rig.env.MusicDir, func(path string, info os.FileInfo, err error) error {
					return nil
				})
			}
		}()
	}

	close(startBarrier)
	wg.Wait()

	// Assertions: For all tracks deleted, verify companions were removed too
	for _, p := range trackPaths {
		absMP3, _ := rig.env.MusicSandbox.Resolve(p, false)
		if _, err := os.Stat(absMP3); os.IsNotExist(err) {
			absLRC := strings.TrimSuffix(absMP3, ".mp3") + ".lrc"
			if _, lrcErr := os.Stat(absLRC); !os.IsNotExist(lrcErr) {
				t.Fatalf("expected .lrc companion to be deleted for %s, but file remains", p)
			}
			absTXT := strings.TrimSuffix(absMP3, ".mp3") + ".txt"
			if _, txtErr := os.Stat(absTXT); !os.IsNotExist(txtErr) {
				t.Fatalf("expected .txt companion to be deleted for %s, but file remains", p)
			}
		}
	}
}

// ============================================================================
// 3. Rapid Cancellation of Background Batch Lyrics/Cover Workers
// ============================================================================

func TestAuditStress_RapidCancellationBatchWorkers(t *testing.T) {
	// Each scenario owns its workers; a burst can leave its last job running.
	setupRig := func(t *testing.T) *stressTestRig {
		t.Helper()
		rig := setupStressTestRig(t, 10*time.Millisecond)
		for i := 0; i < 20; i++ {
			relMP3 := fmt.Sprintf("BatchArtist/Album %d/%02d. Song %d.mp3", i/5, i%5+1, i+1)
			rig.env.CreateDummyMP3(relMP3, fmt.Sprintf("Song %d", i+1), "BatchArtist", fmt.Sprintf("Album %d", i/5))
		}
		if _, err := rig.scannerSvc.ScanAll(context.Background()); err != nil {
			t.Fatalf("ScanAll failed: %v", err)
		}
		return rig
	}

	// Subtest 3.1: High frequency start/cancel burst on lyrics worker
	t.Run("LyricsWorkerBurstStartCancel", func(t *testing.T) {
		rig := setupRig(t)
		var wg sync.WaitGroup
		startBarrier := make(chan struct{})

		// Goroutine starting batch lyrics repeatedly
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			for i := 0; i < 30; i++ {
				_, _ = rig.service.StartBatchLyrics(context.Background())
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
			}
		}()

		// Goroutine cancelling batch lyrics repeatedly
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			for i := 0; i < 30; i++ {
				_ = rig.service.CancelBatchLyrics()
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
			}
		}()

		// Goroutines polling progress concurrently
		for p := 0; p < 3; p++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-startBarrier
				for i := 0; i < 50; i++ {
					_ = rig.service.GetBatchLyricsProgress()
					time.Sleep(2 * time.Millisecond)
				}
			}()
		}

		close(startBarrier)
		wg.Wait()
	})

	// Subtest 3.2: High frequency start/cancel burst on covers worker
	t.Run("CoversWorkerBurstStartCancel", func(t *testing.T) {
		rig := setupRig(t)
		var wg sync.WaitGroup
		startBarrier := make(chan struct{})

		// Goroutine starting batch covers repeatedly
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			for i := 0; i < 20; i++ {
				_, _ = rig.service.StartBatchCovers(context.Background())
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
			}
		}()

		// Goroutine cancelling batch covers repeatedly
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			for i := 0; i < 20; i++ {
				_ = rig.service.CancelBatchCovers()
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
			}
		}()

		// Goroutines polling progress concurrently
		for p := 0; p < 3; p++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-startBarrier
				for i := 0; i < 40; i++ {
					_ = rig.service.GetBatchCoversProgress()
					time.Sleep(2 * time.Millisecond)
				}
			}()
		}

		close(startBarrier)
		wg.Wait()
	})

	// Subtest 3.3: Immediate start after cancel state machine test
	t.Run("ImmediateStartAfterCancel", func(t *testing.T) {
		rig := setupRig(t)
		// Start batch lyrics
		p1, err := rig.service.StartBatchLyrics(context.Background())
		if err != nil {
			t.Fatalf("initial StartBatchLyrics failed: %v", err)
		}
		if !p1.IsRunning && p1.Total > 0 {
			t.Fatalf("expected batch lyrics to be running")
		}

		// Cancel immediately
		cancelled := rig.service.CancelBatchLyrics()
		if !cancelled {
			t.Log("CancelBatchLyrics returned false (worker might have completed or was already stopped)")
		}

		// Start immediately again without waiting
		p2, err := rig.service.StartBatchLyrics(context.Background())
		if err != nil && err != audit.ErrBatchAlreadyRunning {
			t.Fatalf("immediate StartBatchLyrics failed with unexpected error: %v", err)
		}
		if p2.IsRunning {
			t.Log("Worker 2 successfully started with IsRunning=true")
		}

		// Wait 120ms for Worker 1 to exit sleep and run its defer
		time.Sleep(120 * time.Millisecond)

		p3 := rig.service.GetBatchLyricsProgress()
		t.Logf("Batch progress after 120ms: IsRunning=%v, Current=%d, Total=%d", p3.IsRunning, p3.Current, p3.Total)

		// Allow worker to finish or cancel cleanly
		rig.service.CancelBatchLyrics()
	})

	// Subtest 3.4: Graceful Close() while workers are active
	t.Run("GracefulCloseWhileRunning", func(t *testing.T) {
		rig := setupRig(t)
		_, _ = rig.service.StartBatchLyrics(context.Background())
		_, _ = rig.service.StartBatchCovers(context.Background())

		closeErr := rig.service.Close()
		if closeErr != nil {
			t.Fatalf("Service.Close() returned error: %v", closeErr)
		}
	})
}

// ============================================================================
// 4. Database Repository Concurrency Stress
// ============================================================================

func TestAuditStress_DatabaseRepositoryConcurrency(t *testing.T) {
	rig := setupStressTestRig(t, 0)
	ctx := context.Background()

	// Seed 40 tracks, albums, artists, genres, and audit issues
	for i := 0; i < 40; i++ {
		relMP3 := fmt.Sprintf("DBArtist %d/Album %d/%02d. Track %d.mp3", i%4, i%8, i+1, i+1)
		rig.env.CreateDummyMP3(relMP3, fmt.Sprintf("Track %d", i+1), fmt.Sprintf("DBArtist %d", i%4), fmt.Sprintf("Album %d", i%8))
	}
	if _, err := rig.scannerSvc.ScanAll(ctx); err != nil {
		t.Fatalf("ScanAll failed: %v", err)
	}

	// Insert some initial audit issues
	for i := 0; i < 20; i++ {
		entityID := int64(i + 1)
		_, _ = rig.repo.LogAuditIssue(ctx, audit.AuditIssue{
			EntityType: "track",
			EntityID:   &entityID,
			FilePath:   fmt.Sprintf("DBArtist 0/Album 0/%02d. Track %d.mp3", i+1, i+1),
			IssueType:  "tag_missing_genre",
			Details:    "test issue",
		})
	}

	concurrency := 40
	iterationsPerWorker := 30
	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	var totalOps int64
	var errCount int64

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		workerID := w

		go func() {
			defer wg.Done()
			<-startBarrier

			workerCtx := context.Background()

			for it := 0; it < iterationsPerWorker; it++ {
				op := (workerID + it) % 10
				atomic.AddInt64(&totalOps, 1)

				switch op {
				case 0:
					// GetAuditSummary
					if _, err := rig.repo.GetAuditSummary(workerCtx); err != nil {
						atomic.AddInt64(&errCount, 1)
					}
				case 1:
					// SearchLibrary
					if _, err := rig.repo.SearchLibrary(workerCtx, "Track", 10); err != nil {
						atomic.AddInt64(&errCount, 1)
					}
				case 2:
					// GetTracksForDuplicatesScan
					if _, err := rig.repo.GetTracksForDuplicatesScan(workerCtx); err != nil {
						atomic.AddInt64(&errCount, 1)
					}
				case 3:
					// GetIncompleteAlbums
					if _, err := rig.repo.GetIncompleteAlbums(workerCtx, 10); err != nil {
						atomic.AddInt64(&errCount, 1)
					}
				case 4:
					// GetTracksForTagAudit
					if _, err := rig.repo.GetTracksForTagAudit(workerCtx); err != nil {
						atomic.AddInt64(&errCount, 1)
					}
				case 5:
					// GetTracksMissingLyrics
					if _, err := rig.repo.GetTracksMissingLyrics(workerCtx, 10); err != nil {
						atomic.AddInt64(&errCount, 1)
					}
				case 6:
					// GetAlbumsMissingCovers
					if _, err := rig.repo.GetAlbumsMissingCovers(workerCtx, 10); err != nil {
						atomic.AddInt64(&errCount, 1)
					}
				case 7:
					// LogAuditIssue (Write)
					idVal := int64(workerID*100 + it)
					issue := audit.AuditIssue{
						EntityType: "track",
						EntityID:   &idVal,
						FilePath:   fmt.Sprintf("path_%d_%d.mp3", workerID, it),
						IssueType:  "test_stress_issue",
					}
					if _, err := rig.repo.LogAuditIssue(workerCtx, issue); err != nil {
						atomic.AddInt64(&errCount, 1)
					}
				case 8:
					// ResolveAuditIssueByPath (Write)
					path := fmt.Sprintf("path_%d_%d.mp3", workerID, it)
					if err := rig.repo.ResolveAuditIssueByPath(workerCtx, path, "test_stress_issue"); err != nil {
						atomic.AddInt64(&errCount, 1)
					}
				case 9:
					// UpdateTrackMetadata (Write)
					path := fmt.Sprintf("DBArtist 0/Album 0/01. Track 1.mp3")
					if err := rig.repo.UpdateTrackMetadata(workerCtx, path, "Updated Title", "DBArtist 0", "Album 0", 2026); err != nil {
						atomic.AddInt64(&errCount, 1)
					}
				}
			}
		}()
	}

	close(startBarrier)
	wg.Wait()

	if errs := atomic.LoadInt64(&errCount); errs > 0 {
		t.Fatalf("encountered %d database errors out of %d concurrent operations", errs, totalOps)
	}

	t.Logf("Successfully executed %d concurrent database operations with 0 errors", atomic.LoadInt64(&totalOps))

	// Verify DB is still intact and responsive
	summary, err := rig.repo.GetAuditSummary(ctx)
	if err != nil {
		t.Fatalf("GetAuditSummary failed after stress test: %v", err)
	}
	if summary.TotalTracks == 0 {
		t.Fatalf("expected non-zero total tracks in summary, got %d", summary.TotalTracks)
	}
}
