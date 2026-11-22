package downloader

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"music-manager/internal/core/httputil"
	"music-manager/internal/services/downloadstaging"
	"music-manager/internal/services/resolver"
)

// Preflight выполняет предполетную проверку релиза перед загрузкой (список треков, статусы в медиатеке).
// POST /downloads/preflight, POST /api/downloads/preflight
func (m *Module) Preflight(w http.ResponseWriter, r *http.Request) {
	var req PreflightRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json")
		return
	}

	resp, err := m.service.PreflightRelease(r.Context(), req.URL, req.Source)
	if err != nil {
		httputil.RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, resp)
}

// CreateJob ставит задачу на загрузку одиночного трека или альбома.
// POST /downloads
func (m *Module) CreateJob(w http.ResponseWriter, r *http.Request) {
	var req CreateJobRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json")
		return
	}

	job, err := m.service.CreateJobWithOptions(r.Context(), req.URL, req.Source, JobOptions{
		PreflightID:          req.PreflightID,
		SelectedTrackIndices: req.SelectedTrackIndices,
		SkipExisting:         req.SkipExisting,
	})
	if err != nil {
		respondQueueChangeError(w, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, job)
}

// CreateDownload — алиас для обратной совместимости с существующими клиентами.
func (m *Module) CreateDownload(w http.ResponseWriter, r *http.Request) {
	m.CreateJob(w, r)
}

// CreateBatch ставит пакет задач на загрузку.
// POST /downloads/batch
func (m *Module) CreateBatch(w http.ResponseWriter, r *http.Request) {
	var req CreateBatchJobRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json")
		return
	}

	jobs, err := m.service.CreateBatchJobs(r.Context(), req.URLs, req.Source)
	if err != nil {
		respondQueueChangeError(w, err)
		return
	}

	jobIDs := make([]string, len(jobs))
	for i, j := range jobs {
		jobIDs[i] = j.ID
	}

	httputil.RespondJSON(w, http.StatusOK, BatchJobResponse{
		JobIDs:      jobIDs,
		Jobs:        jobs,
		TotalQueued: len(jobs),
	})
}

// CreateBatchDownload — алиас для обратной совместимости.
func (m *Module) CreateBatchDownload(w http.ResponseWriter, r *http.Request) {
	m.CreateBatch(w, r)
}

// ListJobs возвращает список задач с детерминированной сортировкой по приоритету статусов.
// GET /downloads
func (m *Module) ListJobs(w http.ResponseWriter, r *http.Request) {
	if m.service == nil {
		httputil.RespondJSON(w, http.StatusOK, []*Job{})
		return
	}

	jobs := m.service.GetSortedJobSnapshots()
	httputil.RespondJSON(w, http.StatusOK, jobs)
}

// ListDownloads — алиас для обратной совместимости.
func (m *Module) ListDownloads(w http.ResponseWriter, r *http.Request) {
	m.ListJobs(w, r)
}

// GetJob возвращает статус конкретной задачи по UUID.
// GET /downloads/{id}
func (m *Module) GetJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, ok := m.service.GetJob(id)
	if !ok {
		httputil.RespondError(w, http.StatusNotFound, "job not found")
		return
	}

	httputil.RespondJSON(w, http.StatusOK, job.Snapshot())
}

// GetDownload — алиас для обратной совместимости.
func (m *Module) GetDownload(w http.ResponseWriter, r *http.Request) {
	m.GetJob(w, r)
}

// GetJobTracks возвращает метаданные альбома и треклист задачи.
// GET /downloads/{id}/tracks
func (m *Module) GetJobTracks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, ok := m.service.GetJob(id)
	if !ok {
		httputil.RespondError(w, http.StatusNotFound, "job not found")
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

	// Эвристика для одиночного трека (Single fallback)
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

	if snap.Status == StatusReady || snap.Status == StatusPartialReady {
		snap.Tracks = slices.DeleteFunc(snap.Tracks, func(track resolver.ResolvedTrack) bool {
			return track.ConvertedPath != nil && downloadstaging.TrackMissing(m.service.tmpSandbox, *track.ConvertedPath)
		})
	}

	resp := DownloadTracksResponse{
		JobID:           snap.ID,
		AlbumArtist:     albumArtist,
		Album:           albumTitle,
		Year:            year,
		Genre:           genre,
		DiscTotal:       discTot,
		TrackTotal:      trackTot,
		CoverURL:        coverURL,
		CoverPath:       coverPath,
		Tracks:          snap.Tracks,
		IsBackfill:      snap.IsBackfill,
		TargetAlbumPath: snap.TargetAlbumPath,
	}

	httputil.RespondJSON(w, http.StatusOK, resp)
}

// GetDownloadTracks — алиас для обратной совместимости.
func (m *Module) GetDownloadTracks(w http.ResponseWriter, r *http.Request) {
	m.GetJobTracks(w, r)
}

// DeleteJob удаляет задачу и очищает её временный каталог в tmp_downloads.
// DELETE /downloads/{id}
func (m *Module) DeleteJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if r.URL.Query().Get("imported") == "true" {
		finished, err := m.service.FinishImport(r.Context(), id)
		if err != nil {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !finished {
			httputil.RespondJSON(w, http.StatusOK, DeleteResponse{Status: "pending", JobID: id})
			return
		}
	} else if err := m.service.DeleteJob(r.Context(), id); err != nil {
		respondQueueChangeError(w, fmt.Errorf("%w: %w", errQueueStorage, err))
		return
	}

	httputil.RespondJSON(w, http.StatusOK, DeleteResponse{Status: "deleted", JobID: id})
}

// DeleteDownload — алиас для обратной совместимости.
func (m *Module) DeleteDownload(w http.ResponseWriter, r *http.Request) {
	m.DeleteJob(w, r)
}

// RetryJob сбрасывает и перезапускает упавшую или отмененную задачу.
// POST /downloads/{id}/retry
func (m *Module) RetryJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, err := m.service.RetryJob(r.Context(), id)
	if err != nil {
		respondQueueChangeError(w, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, job)
}

// RetryDownload — алиас для обратной совместимости.
func (m *Module) RetryDownload(w http.ResponseWriter, r *http.Request) {
	m.RetryJob(w, r)
}

// GetQueueSummary возвращает метрики и агрегированный статус очереди.
// GET /downloads/queue/summary
func (m *Module) GetQueueSummary(w http.ResponseWriter, r *http.Request) {
	summary := m.service.GetSummary()
	httputil.RespondJSON(w, http.StatusOK, summary)
}

// ClearQueue очищает завершенные и упавшие задачи из очереди.
// POST /downloads/queue/clear
func (m *Module) ClearQueue(w http.ResponseWriter, r *http.Request) {
	count := m.service.ClearCompletedJobs(r.Context())
	httputil.RespondJSON(w, http.StatusOK, ClearResponse{Status: "cleared", Cleared: count, Count: count})
}

// ClearCompleted удаляет завершенные задачи (DELETE /downloads/completed и POST /downloads/clear-completed).
func (m *Module) ClearCompleted(w http.ResponseWriter, r *http.Request) {
	count := m.service.ClearCompletedJobs(r.Context())
	httputil.RespondJSON(w, http.StatusOK, ClearResponse{Status: "cleared", Cleared: count, Count: count})
}

// PauseQueue приостанавливает выполнение очереди загрузок.
// POST /downloads/pause
func (m *Module) PauseQueue(w http.ResponseWriter, r *http.Request) {
	m.service.PauseQueue()
	httputil.RespondJSON(w, http.StatusOK, PauseResponse{Status: "paused", IsPaused: true})
}

// ResumeQueue возобновляет работу очереди загрузок.
// POST /downloads/resume
func (m *Module) ResumeQueue(w http.ResponseWriter, r *http.Request) {
	m.service.ResumeQueue()
	httputil.RespondJSON(w, http.StatusOK, PauseResponse{Status: "resumed", IsPaused: false})
}

// RetryAllFailed перезапускает все ошибочные и отмененные задачи.
// POST /downloads/retry-failed
func (m *Module) RetryAllFailed(w http.ResponseWriter, r *http.Request) {
	jobIDs, err := m.service.RetryAllFailedJobs(r.Context())
	if err != nil {
		respondQueueChangeError(w, err)
		return
	}
	httputil.RespondJSON(w, http.StatusOK, RetryFailedResponse{
		Status:       "retried",
		Count:        len(jobIDs),
		RetriedCount: len(jobIDs),
		JobIDs:       jobIDs,
	})
}

func respondQueueChangeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errQueueStorage):
		log.Printf("[Downloader] %v", err)
		httputil.RespondError(w, http.StatusInternalServerError, errQueueStorage.Error())
	case errors.Is(err, ErrJobNotFound):
		httputil.RespondError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errSelectionConflict), errors.Is(err, errPreflightExpired):
		httputil.RespondError(w, http.StatusConflict, err.Error())
	default:
		httputil.RespondError(w, http.StatusBadRequest, err.Error())
	}
}

// UploadTorrent обрабатывает загрузку .torrent файла.
// POST /downloads/torrent
func (m *Module) UploadTorrent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "Превышен размер файла или неверный формат формы")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "Файл .torrent не передан")
		return
	}
	defer file.Close()

	if !strings.HasSuffix(strings.ToLower(header.Filename), ".torrent") {
		httputil.RespondError(w, http.StatusBadRequest, "Допускаются только файлы с расширением .torrent")
		return
	}

	httputil.RespondJSON(w, http.StatusBadRequest, map[string]string{
		"status":  "error",
		"message": "Загрузка через .torrent временно недоступна в текущей версии. Пожалуйста, используйте ссылки Deezer, YouTube, Spotify или Яндекс Музыки.",
	})
}
