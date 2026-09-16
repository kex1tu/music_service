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
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(summary)
}

func (h *DownloadHandler) ClearCompleted(w http.ResponseWriter, r *http.Request) {
	count := h.service.Jobs().ClearCompleted()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"cleared": count})
}

func (h *DownloadHandler) ClearQueue(w http.ResponseWriter, r *http.Request) {
	count := h.service.Jobs().ClearCompleted()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "cleared", "count": count})
}
