package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/services/downloader"
	"music-manager/internal/services/resolver"
)

type DownloadHandler struct {
	service      *downloader.Service
	deezerClient *resolver.DeezerClient
}

func NewDownloadHandler(service *downloader.Service, deezerClient *resolver.DeezerClient) *DownloadHandler {
	return &DownloadHandler{
		service:      service,
		deezerClient: deezerClient,
	}
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

type DiscographyResponse struct {
	ArtistName    string               `json:"artist_name"`
	ArtistID      int64                `json:"artist_id"`
	TotalReleases int                  `json:"total_releases"`
	Releases      []DiscographyRelease `json:"releases"`
}

type DiscographyRelease struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Type       string `json:"type"`
	Year       int    `json:"year"`
	URL        string `json:"url"`
	CoverURL   string `json:"cover_url"`
	TrackCount int    `json:"track_count"`
}

// GetDiscography fetches all albums, singles, and compilations for a Deezer artist URL.
func (h *DownloadHandler) GetDiscography(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if rawURL == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Параметр url обязателен"})
		return
	}

	if h.deezerClient == nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Deezer клиент не настроен"})
		return
	}

	resourceType, artistID, err := h.deezerClient.ParseURL(r.Context(), rawURL)
	if err != nil || resourceType != "artist" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Поддерживаются только ссылки на артистов Deezer (например, deezer.com/artist/123)"})
		return
	}

	artist, err := h.deezerClient.GetArtist(r.Context(), artistID)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": fmt.Sprintf("Не удалось получить профиль артиста: %v", err)})
		return
	}

	albums, err := h.deezerClient.GetArtistAlbums(r.Context(), artistID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": fmt.Sprintf("Не удалось получить дискографию: %v", err)})
		return
	}

	releases := make([]DiscographyRelease, 0, len(albums))
	for _, alb := range albums {
		relType := strings.ToLower(alb.RecordType)
		if relType == "" {
			relType = "album"
		}
		releases = append(releases, DiscographyRelease{
			ID:         alb.ID,
			Title:      alb.Title,
			Type:       relType,
			Year:       alb.ReleaseYear(),
			URL:        fmt.Sprintf("https://www.deezer.com/album/%d", alb.ID),
			CoverURL:   alb.BestCoverURL(),
			TrackCount: alb.NbTracks,
		})
	}

	resp := DiscographyResponse{
		ArtistName:    artist.Name,
		ArtistID:      artist.ID,
		TotalReleases: len(releases),
		Releases:      releases,
	}

	_ = json.NewEncoder(w).Encode(resp)
}

// UploadTorrent handles .torrent file upload and responds with informative message.
func (h *DownloadHandler) UploadTorrent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Limit upload size to 10MB
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Превышен размер файла или неверный формат формы"})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Файл .torrent не передан"})
		return
	}
	defer file.Close()

	if !strings.HasSuffix(strings.ToLower(header.Filename), ".torrent") {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Допускаются только файлы с расширением .torrent"})
		return
	}

	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "error",
		"message": "Загрузка через .torrent временно недоступна в текущей версии. Пожалуйста, используйте ссылки Deezer, YouTube, Spotify или Яндекс Музыки.",
	})
}
