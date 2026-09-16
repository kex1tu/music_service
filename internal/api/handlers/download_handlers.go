package handlers

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/services/downloader"
)

type DownloadHandler struct {
	service *downloader.Service
}

func NewDownloadHandler(service *downloader.Service) *DownloadHandler {
	return &DownloadHandler{service: service}
}

type createDownloadRequest struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}

type createBatchDownloadRequest struct {
	URLs   []string `json:"urls"`
	Source string   `json:"source"`
}

func (h *DownloadHandler) CreateDownload(w http.ResponseWriter, r *http.Request) {
	var req createDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	job, err := h.service.CreateJob(req.URL, req.Source)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(job)
}

func (h *DownloadHandler) CreateBatchDownload(w http.ResponseWriter, r *http.Request) {
	var req createBatchDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	jobs, err := h.service.CreateBatchJobs(req.URLs, req.Source)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	jobIDs := make([]string, len(jobs))
	for i, j := range jobs {
		jobIDs[i] = j.ID
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"job_ids":      jobIDs,
		"jobs":         jobs,
		"total_queued": len(jobs),
	})
}

func (h *DownloadHandler) ListDownloads(w http.ResponseWriter, r *http.Request) {
	jobs := h.service.Jobs().GetAll()
	// Sort newest first
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt > jobs[j].CreatedAt
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jobs)
}

func (h *DownloadHandler) GetDownload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, ok := h.service.Jobs().Get(id)
	if !ok {
		http.Error(w, `{"error":"job not found"}`, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(job)
}

func (h *DownloadHandler) GetDownloadTracks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, ok := h.service.Jobs().Get(id)
	if !ok {
		http.Error(w, `{"error":"job not found"}`, http.StatusNotFound)
		return
	}

	snap := job.Snapshot()

	discTot := 1
	trackTot := len(snap.Tracks)

	var year *int
	if snap.AlbumMetadata.Year > 0 {
		year = &snap.AlbumMetadata.Year
	}
	var genre *string
	if snap.AlbumMetadata.Genre != "" {
		genre = &snap.AlbumMetadata.Genre
	}

	var coverPath *string
	if snap.CoverRelativePath != "" {
		c := snap.CoverRelativePath
		coverPath = &c
	}
	var coverURL *string
	if snap.AlbumMetadata.CoverURL != "" {
		c := snap.AlbumMetadata.CoverURL
		coverURL = &c
	}

	resp := map[string]any{
		"job_id":       snap.ID,
		"album_artist": snap.AlbumMetadata.AlbumArtist,
		"album":        snap.AlbumMetadata.AlbumTitle,
		"year":         year,
		"genre":        genre,
		"disc_total":   discTot,
		"track_total":  trackTot,
		"cover_url":    coverURL,
		"cover_path":   coverPath,
		"tracks":       snap.Tracks,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
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

func (h *DownloadHandler) DeleteDownload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_ = h.service.DeleteJob(id)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "job_id": id})
}
