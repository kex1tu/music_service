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
