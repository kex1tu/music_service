package downloader

import (
	"encoding/json"
	"fmt"
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

