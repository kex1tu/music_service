package downloader

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"music-manager/internal/services/resolver"
)

func TestJobManager_ConcurrentAccess(t *testing.T) {
	mgr := NewJobManager(nil)

	// Create 5 jobs
	for i := 1; i <= 5; i++ {
		job := &DownloadJob{
			ID:          fmt.Sprintf("job-%d", i),
			Status:      StatusQueued,
			Source:      "deezer",
			URL:         fmt.Sprintf("https://www.deezer.com/album/%d", i),
			CreatedAt:   time.Now().UTC().Format(time.RFC3339),
			TotalTracks: 10,
			Tracks: []resolver.ResolvedTrack{
				{TrackNumber: 1, Title: "Track 1", Status: "queued"},
				{TrackNumber: 2, Title: "Track 2", Status: "queued"},
			},
		}
		mgr.Add(job)
	}

	var wg sync.WaitGroup
	numWriters := 8
	numReaders := 8
	iterations := 100

	// Writers: simulate background downloader updating progress, status, tracks
	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for it := 0; it < iterations; it++ {
				jobID := fmt.Sprintf("job-%d", (it%5)+1)
				if j, ok := mgr.Get(jobID); ok {
					prog := float64((it % 100))
					j.UpdateProgress(prog, fmt.Sprintf("Worker %d step %d", workerID, it))
					if it%20 == 0 {
						j.SetStatus(StatusDownloading)
					} else if it%20 == 10 {
						j.SetStatus(StatusConverting)
					}
					j.IncrementCompleted(10)
					j.UpdateTrack(0, func(tr *resolver.ResolvedTrack) {
						tr.Status = "downloading"
					})
				}
				time.Sleep(100 * time.Microsecond)
			}
		}(w)
	}

	// Readers: simulate HTTP handlers querying jobs, encoding to JSON, getting summaries
	for r := 0; r < numReaders; r++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			for it := 0; it < iterations; it++ {
				// 1. GetAll and JSON serialize
				all := mgr.GetAll()
				for _, j := range all {
					bytes, err := json.Marshal(j)
					if err != nil {
						t.Errorf("failed to marshal job: %v", err)
					}
					if len(bytes) == 0 {
						t.Errorf("empty json output")
					}
				}

				// 2. Get specific job snapshot
				jobID := fmt.Sprintf("job-%d", (it%5)+1)
				if j, ok := mgr.Get(jobID); ok {
					snap := j.Snapshot()
					if snap.ID != jobID {
						t.Errorf("unexpected job ID: %s", snap.ID)
					}
				}

				// 3. Queue summary
				summary := mgr.GetSummary()
				if summary.TotalTracks < 0 {
					t.Errorf("negative total tracks: %d", summary.TotalTracks)
				}

				time.Sleep(150 * time.Microsecond)
			}
		}(r)
	}

	wg.Wait()
}

func TestDownloadJob_MonotonicTrackProgress(t *testing.T) {
	job := &DownloadJob{
		ID:          "mono-job",
		Status:      StatusDownloading,
		TotalTracks: 2,
		Tracks: []resolver.ResolvedTrack{
			{TrackNumber: 1, Title: "Track 1", Status: "downloading"},
			{TrackNumber: 2, Title: "Track 2", Status: "pending"},
		},
		trackProgress: make([]float64, 2),
	}

	// 1. Track 1 advances to 50%
	job.UpdateTrackProgress(0, 50.0, "Загрузка 1 (50%)")
	snap1 := job.Snapshot()
	if snap1.Progress < 10.0 {
		t.Fatalf("expected progress >= 10, got %f", snap1.Progress)
	}

	// 2. Out-of-order / fragment reset reports 0% for track 1
	job.UpdateTrackProgress(0, 0.0, "Загрузка 1 (0%)")
	snap2 := job.Snapshot()
	if snap2.Progress < snap1.Progress {
		t.Fatalf("progress dropped from %f to %f after 0%% report", snap1.Progress, snap2.Progress)
	}

	// 3. Track 1 reaches 100%
	job.UpdateTrackProgress(0, 100.0, "Загрузка 1 (100%)")
	snap3 := job.Snapshot()
	if snap3.Progress < snap2.Progress {
		t.Fatalf("progress dropped from %f to %f", snap2.Progress, snap3.Progress)
	}

	// 4. Multi-track step should remain aggregated and not jump
	if snap3.CurrentStep == "" {
		t.Fatalf("expected non-empty current step")
	}
}

func TestJobManager_WeightedOverallProgress(t *testing.T) {
	mgr := NewJobManager(nil)

	// 1-track job at 100%
	j1 := &DownloadJob{
		ID:              "j1",
		Status:          StatusReady,
		TotalTracks:     1,
		CompletedTracks: 1,
		Progress:        100.0,
		Tracks:          []resolver.ResolvedTrack{{TrackNumber: 1, Status: "ready"}},
	}
	// 9-track job at 0%
	j2 := &DownloadJob{
		ID:          "j2",
		Status:      StatusQueued,
		TotalTracks: 9,
		Progress:    0.0,
		Tracks: []resolver.ResolvedTrack{
			{TrackNumber: 1}, {TrackNumber: 2}, {TrackNumber: 3},
			{TrackNumber: 4}, {TrackNumber: 5}, {TrackNumber: 6},
			{TrackNumber: 7}, {TrackNumber: 8}, {TrackNumber: 9},
		},
	}

	mgr.Add(j1)
	mgr.Add(j2)

	summary := mgr.GetSummary()
	// Total tracks: 10, weighted progress should be (100*1 + 0*9) / 10 = 10%
	if summary.TotalTracks != 10 {
		t.Fatalf("expected 10 total tracks, got %d", summary.TotalTracks)
	}
	if summary.OverallProgress != 10.0 {
		t.Fatalf("expected 10.0%% overall progress, got %f", summary.OverallProgress)
	}
}

func TestDownloadJob_SetPartialReady(t *testing.T) {
	mgr := NewJobManager(nil)

	job := &DownloadJob{
		ID:              "j-partial",
		Status:          StatusDownloading,
		TotalTracks:     10,
		CompletedTracks: 8,
		Tracks: []resolver.ResolvedTrack{
			{TrackNumber: 1, Status: "ready"},
			{TrackNumber: 2, Status: "waiting_source"},
		},
	}
	mgr.Add(job)

	job.SetPartialReady(9, 10)

	snap := job.Snapshot()
	if snap.Status != StatusPartialReady {
		t.Errorf("expected status %s, got %s", StatusPartialReady, snap.Status)
	}
	if snap.Progress != 100.0 {
		t.Errorf("expected progress 100.0, got %f", snap.Progress)
	}
	if snap.CompletedTracks != 9 {
		t.Errorf("expected completed tracks 9, got %d", snap.CompletedTracks)
	}
	expectedStep := "Готов частично (9/10 готово, требуется выбор источника)"
	if snap.CurrentStep != expectedStep {
		t.Errorf("expected step %q, got %q", expectedStep, snap.CurrentStep)
	}

	summary := mgr.GetSummary()
	if summary.CompletedJobs != 1 {
		t.Errorf("expected summary.CompletedJobs to count PARTIAL_READY, got %d", summary.CompletedJobs)
	}
}

func TestTrack_MatchScoreAndLowMatch(t *testing.T) {
	job := &DownloadJob{
		ID:          "job-low-match",
		Status:      StatusDownloading,
		TotalTracks: 2,
		Tracks: []resolver.ResolvedTrack{
			{
				TrackNumber: 1,
				Title:       "Track Low Match",
				Status:      "downloading",
				MatchScore:  62.5,
				IsLowMatch:  true,
			},
			{
				TrackNumber: 2,
				Title:       "Track High Match",
				Status:      "ready",
				MatchScore:  95.0,
				IsLowMatch:  false,
			},
		},
	}

	snap := job.Snapshot()
	if len(snap.Tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(snap.Tracks))
	}
	if !snap.Tracks[0].IsLowMatch || snap.Tracks[0].MatchScore != 62.5 {
		t.Errorf("expected track 0 low match with score 62.5, got low=%v, score=%f", snap.Tracks[0].IsLowMatch, snap.Tracks[0].MatchScore)
	}
	if snap.Tracks[1].IsLowMatch || snap.Tracks[1].MatchScore != 95.0 {
		t.Errorf("expected track 1 high match with score 95.0, got low=%v, score=%f", snap.Tracks[1].IsLowMatch, snap.Tracks[1].MatchScore)
	}

	data, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("failed to marshal job: %v", err)
	}
	jsonStr := string(data)
	if !strings.Contains(jsonStr, `"is_low_match":true`) {
		t.Errorf("expected json to contain is_low_match:true, got %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"match_score":62.5`) {
		t.Errorf("expected json to contain match_score:62.5, got %s", jsonStr)
	}
}


