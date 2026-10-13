package handlers

import (
	"encoding/json"
	"net/http"

	"music-manager/internal/services/downloader"
)

func jobStatusPriority(s downloader.JobStatus) int {
	switch s {
	case downloader.StatusQueued:
		return 1 // Ожидают
	case downloader.StatusResolving, downloader.StatusDownloading, downloader.StatusConverting, downloader.StatusTagging:
		return 2 // Скачиваются / в процессе
	case downloader.StatusReady:
		return 3 // Скачаны
	case downloader.StatusError:
		return 4 // Ошибки
	case downloader.StatusCancelled:
		return 5 // Отменены
	default:
		return 6
	}
}

func (h *DownloadHandler) GetQueueSummary(w http.ResponseWriter, r *http.Request) {
	summary := h.service.Jobs().GetSummary()
	summary.IsPaused = h.service.IsQueuePaused()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(summary)
}

func (h *DownloadHandler) ClearCompleted(w http.ResponseWriter, r *http.Request) {
	count := h.service.ClearCompletedJobs()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "cleared", "cleared": count, "count": count})
}

func (h *DownloadHandler) ClearQueue(w http.ResponseWriter, r *http.Request) {
	count := h.service.ClearCompletedJobs()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "cleared", "count": count, "cleared": count})
}

func (h *DownloadHandler) PauseQueue(w http.ResponseWriter, r *http.Request) {
	h.service.PauseQueue()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "paused", "is_paused": true})
}

func (h *DownloadHandler) ResumeQueue(w http.ResponseWriter, r *http.Request) {
	h.service.ResumeQueue()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "resumed", "is_paused": false})
}

func (h *DownloadHandler) RetryAllFailed(w http.ResponseWriter, r *http.Request) {
	jobIDs, err := h.service.RetryAllFailedJobs()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "retried", "count": len(jobIDs), "retried_count": len(jobIDs), "job_ids": jobIDs})
}
