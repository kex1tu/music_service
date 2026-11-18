package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/db"
	"music-manager/internal/services/downloader"
	"music-manager/internal/services/downloadstaging"
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
	if jobs == nil {
		jobs = make([]*downloader.DownloadJob, 0)
	}
	// Sort by status priority: Queued (1) -> Downloading/Converting/Tagging (2) -> Ready (3) -> Error/Cancelled (4/5)
	// Within the same status, sort by CreatedAt DESC, then ID ASC
	sort.Slice(jobs, func(i, j int) bool {
		pI := jobStatusPriority(jobs[i].Status)
		pJ := jobStatusPriority(jobs[j].Status)
		if pI != pJ {
			return pI < pJ
		}
		if jobs[i].CreatedAt != jobs[j].CreatedAt {
			return jobs[i].CreatedAt > jobs[j].CreatedAt
		}
		return jobs[i].ID < jobs[j].ID
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

	albumArtist := snap.AlbumMetadata.AlbumArtist
	albumTitle := snap.AlbumMetadata.AlbumTitle

	var year *int
	if snap.AlbumMetadata.Year > 0 {
		year = &snap.AlbumMetadata.Year
	}
	var genre *string
	if snap.AlbumMetadata.Genre != "" {
		genre = &snap.AlbumMetadata.Genre
	}

	if trackTot == 1 {
		t := snap.Tracks[0]
		if albumArtist == "" && t.Artist != "" {
			albumArtist = t.Artist
		}
		if (albumTitle == "" || strings.EqualFold(albumTitle, "single") || strings.EqualFold(albumTitle, "singles")) && t.Title != "" {
			albumTitle = t.Title
		}
		if year == nil && t.Year != nil && *t.Year > 0 {
			year = t.Year
		}
		if (genre == nil || *genre == "") && t.Genre != nil && *t.Genre != "" {
			genre = t.Genre
		}
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

	if snap.Status == downloader.StatusReady || snap.Status == downloader.StatusPartialReady {
		snap.Tracks = slices.DeleteFunc(snap.Tracks, func(track resolver.ResolvedTrack) bool {
			return track.ConvertedPath != nil && downloadstaging.TrackMissing(h.service.TmpSandbox(), *track.ConvertedPath)
		})
	}

	resp := map[string]any{
		"job_id":       snap.ID,
		"album_artist": albumArtist,
		"album":        albumTitle,
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

func (h *DownloadHandler) DeleteDownload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	status := "deleted"
	if r.URL.Query().Get("imported") == "true" {
		finished, err := h.service.FinishImport(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !finished {
			status = "pending"
		}
	} else {
		_ = h.service.DeleteJob(id)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status, "job_id": id})
}

func (h *DownloadHandler) RetryDownload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, err := h.service.RetryJob(id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(job)
}

type switchVersionRequest struct {
	TargetURL string `json:"target_url"`
}

// SwitchTrackVersion переключает версию трека на выбранный SoundCloud URL.
func (h *DownloadHandler) SwitchTrackVersion(w http.ResponseWriter, r *http.Request) {
	trackID := chi.URLParam(r, "trackId")
	if trackID == "" {
		http.Error(w, `{"error":"trackId is required"}`, http.StatusBadRequest)
		return
	}

	var req switchVersionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json body"}`, http.StatusBadRequest)
		return
	}

	if req.TargetURL == "" {
		http.Error(w, `{"error":"target_url is required"}`, http.StatusBadRequest)
		return
	}

	result, err := h.service.SwitchTrackVersion(r.Context(), trackID, req.TargetURL)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		if errors.Is(err, downloader.ErrInvalidURL) {
			w.WriteHeader(http.StatusBadRequest)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// GetCandidates ищет кандидатов на YouTube Music, SoundCloud и YouTube для ручного выбора.
func (h *DownloadHandler) GetCandidates(w http.ResponseWriter, r *http.Request) {
	artist := strings.TrimSpace(r.URL.Query().Get("artist"))
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	customQ := strings.TrimSpace(r.URL.Query().Get("q"))
	durStr := strings.TrimSpace(r.URL.Query().Get("duration"))
	dur, _ := strconv.Atoi(durStr)

	if customQ == "" && (artist == "" && title == "") {
		http.Error(w, `{"error":"query, or artist and title are required"}`, http.StatusBadRequest)
		return
	}

	if h.service == nil || h.service.Resolver() == nil || h.service.Resolver().Matcher() == nil {
		http.Error(w, `{"error":"audio matcher is not available"}`, http.StatusServiceUnavailable)
		return
	}

	soundCloudLimit, _ := strconv.Atoi(r.URL.Query().Get("soundcloud_limit"))
	candidates, err := h.service.Resolver().Matcher().SearchMultiCandidatesWithLimit(r.Context(), artist, title, dur, customQ, soundCloudLimit)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	policy := resolver.VersionPolicy(r.URL.Query().Get("version_policy"))
	if policy == "" {
		policy = resolver.PolicyRelease
	}
	if !resolver.ValidVersionPolicy(policy) {
		http.Error(w, `{"error":"invalid version_policy"}`, http.StatusBadRequest)
		return
	}
	candidates = resolver.RankCandidateItems(candidates, title, artist, dur, policy)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"artist":         artist,
		"title":          title,
		"query":          customQ,
		"candidates":     candidates,
		"total":          len(candidates),
		"version_policy": policy,
		"selection":      "manual",
	})
}

type bindBackfillSourceRequest struct {
	Path        string `json:"path"`
	AlbumTitle  string `json:"album_title"`
	ArtistName  string `json:"artist_name"`
	TrackTitle  string `json:"track_title"`
	TrackNumber int    `json:"track_number"`
	TargetURL   string `json:"target_url"`
}

// BindBackfillSource привязывает выбранный URL к отсутствующему треку релиза и запускает докачку.
func (h *DownloadHandler) BindBackfillSource(w http.ResponseWriter, r *http.Request) {
	var req bindBackfillSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json body"}`, http.StatusBadRequest)
		return
	}

	req.TargetURL = strings.TrimSpace(req.TargetURL)
	if req.TargetURL == "" {
		http.Error(w, `{"error":"target_url is required"}`, http.StatusBadRequest)
		return
	}

	if !resolver.IsAllowedURL(req.TargetURL) {
		http.Error(w, `{"error":"target_url domain is not allowed"}`, http.StatusBadRequest)
		return
	}

	// 1. Сохранение ручного пина в SQLite
	if h.service.Database() != nil {
		tTitle := req.TrackTitle
		if tTitle == "" {
			tTitle = req.AlbumTitle
		}
		cacheKey := resolver.BuildCacheKey("", req.ArtistName, tTitle)
		_ = h.service.Database().SavePinnedSource(r.Context(), db.PinnedTrackSource{
			CacheKey:   cacheKey,
			ArtistName: req.ArtistName,
			TrackTitle: tTitle,
			PinnedURL:  req.TargetURL,
			SourceType: "manual",
		})
	}

	// 2. Создание задачи дозагрузки (Backfill Job)
	job, err := h.service.CreateBackfillJob(
		req.Path,
		req.AlbumTitle,
		req.ArtistName,
		"",
		[]string{req.TargetURL},
		[]int{req.TrackNumber},
	)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(job)
}
