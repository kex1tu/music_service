package downloader

import "time"

// Recompute under one lock: concurrent manual track downloads cannot overwrite
// a completed job with a stale partial count.
func (j *Job) reconcileManualSources() {
	j.mu.Lock()
	ready, processing := 0, false
	for _, tr := range j.Tracks {
		if tr.Status == "ready" {
			ready++
		}
		if tr.Status == "downloading" || tr.Status == "converting" || tr.Status == "tagging" {
			processing = true
		}
	}
	j.CompletedTracks = ready
	j.TotalTracks = len(j.Tracks)
	j.Error = nil
	j.UpdatedAt = time.Now().UTC()
	switch {
	case len(j.Tracks) > 0 && ready == len(j.Tracks):
		j.Status = StatusReady
		j.Progress = 100
		j.CurrentStep = "Выбранные источники загружены"
	case processing:
		j.Status = StatusDownloading
	case ready > 0:
		j.Status = StatusPartialReady
		j.CurrentStep = "Часть треков готова; выберите источники остальных"
	default:
		j.Status = StatusWaitingSource
		j.CurrentStep = "Требуется ручной выбор источника"
	}
	j.mu.Unlock()
	j.notifyUpdate()
}
