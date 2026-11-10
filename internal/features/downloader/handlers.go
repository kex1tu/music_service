package downloader

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/httputil"
	"music-manager/internal/core/mediaexec"
	"music-manager/internal/core/safefs"
	"music-manager/internal/id3"
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
		SelectedTrackIndices: req.SelectedTrackIndices,
		SkipExisting:         req.SkipExisting,
	})
	if err != nil {
		httputil.RespondError(w, http.StatusBadRequest, err.Error())
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
		httputil.RespondError(w, http.StatusBadRequest, err.Error())
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
	_ = m.service.DeleteJob(r.Context(), id)
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
		httputil.RespondError(w, http.StatusBadRequest, err.Error())
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
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, RetryFailedResponse{
		Status:       "retried",
		Count:        len(jobIDs),
		RetriedCount: len(jobIDs),
		JobIDs:       jobIDs,
	})
}

// SwitchTrackVersion переключает аудиоисточник трека на альтернативный SoundCloud URL.
// POST /downloads/tracks/{trackId}/switch-version
// POST /downloads/tracks/{trackId}/bind-source
// POST /api/downloads/tracks/{trackId}/switch-version
// POST /api/downloads/tracks/{trackId}/bind-source
func (m *Module) SwitchTrackVersion(w http.ResponseWriter, r *http.Request) {
	trackID := chi.URLParam(r, "trackId")
	if trackID == "" {
		httputil.RespondError(w, http.StatusBadRequest, "trackId is required")
		return
	}

	var req SwitchVersionRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	req.TargetURL = strings.TrimSpace(req.TargetURL)
	if req.TargetURL == "" {
		httputil.RespondError(w, http.StatusBadRequest, "target_url is required")
		return
	}

	if req.VersionPolicy == "" {
		req.VersionPolicy = resolver.PolicyRelease
	}
	if !resolver.ValidVersionPolicy(req.VersionPolicy) {
		httputil.RespondError(w, 400, "invalid version_policy")
		return
	}
	result, err := m.service.SwitchTrackVersion(r.Context(), trackID, req.TargetURL, req.VersionPolicy)
	if err != nil {
		if errors.Is(err, ErrInvalidURL) {
			httputil.RespondError(w, http.StatusBadRequest, err.Error())
		} else {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	httputil.RespondJSON(w, http.StatusOK, result)
}

// GetCandidates ищет альтернативные версии трека на YouTube Music, SoundCloud и YouTube.
// GET /downloads/candidates
// GET /api/downloads/candidates
func (m *Module) GetCandidates(w http.ResponseWriter, r *http.Request) {
	policy := resolver.VersionPolicy(r.URL.Query().Get("version_policy"))
	if policy == "" {
		policy = resolver.PolicyRelease
	}
	if !resolver.ValidVersionPolicy(policy) {
		httputil.RespondError(w, 400, "invalid version_policy")
		return
	}
	artist := strings.TrimSpace(httputil.QueryString(r, "artist", ""))
	title := strings.TrimSpace(httputil.QueryString(r, "title", ""))
	customQ := strings.TrimSpace(httputil.QueryString(r, "q", ""))
	dur := httputil.QueryInt(r, "duration", 0)

	if customQ == "" && (artist == "" && title == "") {
		httputil.RespondError(w, http.StatusBadRequest, "query, or artist and title are required")
		return
	}

	candidates, err := m.service.GetCandidatesWithLimit(r.Context(), artist, title, dur, customQ, r.URL.Query().Get("isrc"), httputil.QueryInt(r, "soundcloud_limit", 30))
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	candidates = resolver.RankCandidateItems(candidates, title, artist, dur, policy)
	httputil.RespondJSON(w, http.StatusOK, CandidatesResponse{
		VersionPolicy: policy, Selection: "manual",
		Artist:     artist,
		Title:      title,
		Query:      customQ,
		Candidates: candidates,
		Total:      len(candidates),
	})
}

// PreviewAudio стримит 15-секундный MP3 фрагмент трека в реальном времени без скачивания на диск.
// GET /downloads/preview
// GET /api/downloads/preview
func (m *Module) PreviewAudio(w http.ResponseWriter, r *http.Request) {
	targetURL := strings.TrimSpace(httputil.QueryString(r, "url", ""))
	if targetURL == "" {
		httputil.RespondError(w, http.StatusBadRequest, "url parameter is required")
		return
	}

	if !resolver.IsAllowedURL(targetURL) {
		httputil.RespondError(w, http.StatusBadRequest, "domain is not in allowed whitelist")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	streamer := m.getPreviewStreamer()
	if streamer == nil {
		streamer = defaultPreviewStreamer
	}

	if err := streamer(ctx, targetURL, w); err != nil {
		httputil.RespondError(w, http.StatusBadGateway, err.Error())
		return
	}
}

// defaultPreviewStreamer реализует реальный стриминг через yt-dlp и ffmpeg
// с автоматической изоляцией подпроцессов в тестовом режиме.
func defaultPreviewStreamer(ctx context.Context, targetURL string, w http.ResponseWriter) error {
	if os.Getenv("MUSIC_MANAGER_TEST_MODE") == "1" {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Transfer-Encoding", "chunked")

		// Минимальные валидные кадры MPEG-1 Layer 3 (320 kbps, 44.1 kHz, stereo)
		dummyPayload := make([]byte, 1044*4)
		for i := 0; i < 4; i++ {
			offset := i * 1044
			dummyPayload[offset+0] = 0xFF
			dummyPayload[offset+1] = 0xFB
			dummyPayload[offset+2] = 0xE0
			dummyPayload[offset+3] = 0x04
		}
		_, _ = w.Write(dummyPayload)
		return nil
	}

	cmdDlp := mediaexec.CommandContext(ctx, "yt-dlp",
		"--no-warnings",
		"--no-playlist",
		"--format", "bestaudio/best",
		"-g",
		targetURL,
	)
	var outDlp, errDlp bytes.Buffer
	cmdDlp.Stdout = &outDlp
	cmdDlp.Stderr = &errDlp

	if err := cmdDlp.Run(); err != nil || strings.TrimSpace(outDlp.String()) == "" {
		cleanErr := resolver.CleanYtDlpStderr(errDlp.String())
		if cleanErr == "" {
			cleanErr = "failed to resolve audio stream URL"
		}
		return errors.New(cleanErr)
	}

	streamURL := strings.TrimSpace(strings.Split(outDlp.String(), "\n")[0])
	if streamURL == "" {
		return errors.New("empty stream url resolved")
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Transfer-Encoding", "chunked")

	cmdFfmpeg := mediaexec.CommandContext(ctx, "ffmpeg",
		"-ss", "30",
		"-i", streamURL,
		"-t", "15",
		"-acodec", "libmp3lame",
		"-b:a", "128k",
		"-f", "mp3",
		"-vn",
		"-",
	)
	cmdFfmpeg.Stdout = w
	if err := cmdFfmpeg.Run(); err != nil {
		cmdFallback := mediaexec.CommandContext(ctx, "ffmpeg",
			"-ss", "0",
			"-i", streamURL,
			"-t", "15",
			"-acodec", "libmp3lame",
			"-b:a", "128k",
			"-f", "mp3",
			"-vn",
			"-",
		)
		cmdFallback.Stdout = w
		_ = cmdFallback.Run()
	}
	return nil
}

// BindBackfillSource привязывает выбранный URL к отсутствующему треку релиза и запускает докачку.
// POST /downloads/backfill/bind-source
// POST /api/downloads/backfill/bind-source
func (m *Module) BindBackfillSource(w http.ResponseWriter, r *http.Request) {
	var req BindBackfillSourceRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.VersionPolicy == "" {
		req.VersionPolicy = resolver.PolicyRelease
	}
	if !resolver.ValidVersionPolicy(req.VersionPolicy) {
		httputil.RespondError(w, 400, "invalid version_policy")
		return
	}

	req.TargetURL = strings.TrimSpace(req.TargetURL)
	if req.TargetURL == "" {
		httputil.RespondError(w, http.StatusBadRequest, "target_url is required")
		return
	}

	if !resolver.IsAllowedURL(req.TargetURL) {
		httputil.RespondError(w, http.StatusBadRequest, "target_url domain is not allowed")
		return
	}

	if m.service.Repo() != nil {
		tTitle := req.TrackTitle
		if tTitle == "" {
			tTitle = req.AlbumTitle
		}
		cacheKey := resolver.BuildCacheKey("", req.ArtistName, tTitle)
		_ = m.service.Repo().SavePinnedSource(r.Context(), PinnedTrackSourceDTO{
			CacheKey:   cacheKey,
			ArtistName: req.ArtistName,
			TrackTitle: tTitle,
			PinnedURL:  req.TargetURL,
			SourceType: "manual",
			UpdatedAt:  time.Now().UTC(),
		})
	}

	selection, err := m.service.SwitchReleaseSource(r.Context(), SwitchReleaseSourceRequest{SourceURL: req.TargetURL, TargetPath: req.Path, VersionPolicy: req.VersionPolicy, TrackMapping: map[int]string{0: req.TargetURL}, TargetTracks: []resolver.ResolvedTrack{{Title: req.TrackTitle, Artist: req.ArtistName, Album: req.AlbumTitle, TrackNumber: req.TrackNumber, Duration: req.Duration}}})
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	job, _ := m.service.GetJob(selection.JobID)

	httputil.RespondJSON(w, http.StatusOK, job)
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

// isHostMatchingDomain проверяет точное совпадение хоста или принадлежность поддомену.
func isHostMatchingDomain(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// GetDiscography возвращает дискографию артиста по внешней ссылке.
// GET /downloads/discography
func (m *Module) GetDiscography(w http.ResponseWriter, r *http.Request) {
	rawURL := strings.TrimSpace(httputil.QueryString(r, "url", ""))
	if rawURL == "" {
		httputil.RespondError(w, http.StatusBadRequest, "Параметр url обязателен")
		return
	}

	// 1. Валидация схемы: разрешены только HTTP и HTTPS
	if strings.Contains(rawURL, "://") && !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		httputil.RespondError(w, http.StatusBadRequest, "Поддерживаются только схемы HTTP и HTTPS")
		return
	}

	// 2. Строгая проверка через белый список доверенных источников (SSRF Guard)
	if !resolver.IsAllowedURL(rawURL) {
		httputil.RespondError(w, http.StatusBadRequest, "Домен не входит в список разрешенных источников")
		return
	}

	// 3. Синтаксический разбор URL и извлечение канонического хоста
	cleanURL := rawURL
	if !strings.HasPrefix(cleanURL, "http://") && !strings.HasPrefix(cleanURL, "https://") {
		cleanURL = "https://" + cleanURL
	}
	parsedURL, err := url.Parse(cleanURL)
	if err != nil || parsedURL.Hostname() == "" {
		httputil.RespondError(w, http.StatusBadRequest, "Некорректный URL")
		return
	}
	host := strings.ToLower(parsedURL.Hostname())

	// 4. Диспетчеризация строго по валидированному хосту

	// YouTube & YouTube Music
	if isHostMatchingDomain(host, "youtube.com") || isHostMatchingDomain(host, "youtu.be") {
		m.getYoutubeMusicDiscography(w, r, rawURL)
		return
	}

	// Deezer
	if isHostMatchingDomain(host, "deezer.com") || isHostMatchingDomain(host, "deezer.page.link") || host == "link.deezer.com" {
		m.getDeezerDiscography(w, r, rawURL)
		return
	}

	// Apple Music / iTunes
	if host == "music.apple.com" || host == "itunes.apple.com" || isHostMatchingDomain(host, "apple.com") {
		m.getAppleMusicDiscography(w, r, rawURL)
		return
	}

	// Яндекс Музыка
	if isHostMatchingDomain(host, "music.yandex.ru") || isHostMatchingDomain(host, "music.yandex.com") || host == "yandex.ru" || host == "yandex.com" {
		m.getYandexMusicDiscography(w, r, rawURL)
		return
	}

	// Spotify (информативный ответ об отсутствии прямой пакетной дискографии)
	if isHostMatchingDomain(host, "spotify.com") || isHostMatchingDomain(host, "spotify.link") {
		httputil.RespondError(w, http.StatusBadRequest, "Пакетная дискография для Spotify пока не поддерживается напрямую. Рекомендуется использовать ссылку на артиста в Яндекс Музыке, Deezer, Apple Music или YouTube Music.")
		return
	}

	httputil.RespondError(w, http.StatusBadRequest, "Нераспознанный URL артиста. Поддерживаются Яндекс Музыка, Deezer, YouTube Music и Apple Music.")
}

func (m *Module) getDeezerDiscography(w http.ResponseWriter, r *http.Request, rawURL string) {
	var deezer *resolver.DeezerClient
	if m.deezerClient != nil {
		deezer = m.deezerClient
	} else if m.service != nil && m.service.Resolver() != nil {
		deezer = m.service.Resolver().Deezer()
	}
	if deezer == nil {
		deezer = resolver.NewDeezerClient()
	}

	resourceType, artistID, err := deezer.ParseURL(r.Context(), rawURL)
	if err != nil || resourceType != "artist" {
		httputil.RespondError(w, http.StatusBadRequest, "Некорректная ссылка на артиста Deezer")
		return
	}

	artist, err := deezer.GetArtist(r.Context(), artistID)
	if err != nil {
		httputil.RespondError(w, http.StatusBadRequest, fmt.Sprintf("Не удалось получить профиль артиста Deezer: %v", err))
		return
	}

	albums, err := deezer.GetArtistAlbums(r.Context(), artistID)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, fmt.Sprintf("Не удалось получить дискографию Deezer: %v", err))
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

	httputil.RespondJSON(w, http.StatusOK, DiscographyResponse{
		ArtistName:    artist.Name,
		ArtistID:      artist.ID,
		ArtistAvatar:  artist.Picture,
		Source:        "deezer",
		TotalReleases: len(releases),
		Releases:      releases,
	})
}

func (m *Module) getYandexMusicDiscography(w http.ResponseWriter, r *http.Request, rawURL string) {
	var yandexClient *resolver.YandexMusicClient
	if m.service != nil && m.service.Resolver() != nil {
		yandexClient = m.service.Resolver().YandexClient()
	}
	if yandexClient == nil {
		yandexClient = resolver.NewYandexMusicClient("")
	}

	resType, artistID, err := yandexClient.ParseURL(rawURL)
	if err != nil || resType != "artist" {
		httputil.RespondError(w, http.StatusBadRequest, "Некорректная ссылка на артиста Яндекс Музыки (ожидается ссылка вида https://music.yandex.ru/artist/{id})")
		return
	}

	artist, err := yandexClient.GetArtistBriefInfo(r.Context(), artistID)
	if err != nil {
		statusCode := http.StatusBadRequest
		if strings.Contains(err.Error(), "451") || strings.Contains(err.Error(), "Geoblock") {
			statusCode = http.StatusUnavailableForLegalReasons
		}
		httputil.RespondError(w, statusCode, fmt.Sprintf("Не удалось получить профиль артиста Яндекс Музыки: %v", err))
		return
	}

	albums, err := yandexClient.GetArtistDirectAlbums(r.Context(), artistID, 0, 100)
	if err != nil {
		statusCode := http.StatusInternalServerError
		if strings.Contains(err.Error(), "451") || strings.Contains(err.Error(), "Geoblock") {
			statusCode = http.StatusUnavailableForLegalReasons
		}
		httputil.RespondError(w, statusCode, fmt.Sprintf("Не удалось получить дискографию Яндекс Музыки: %v", err))
		return
	}

	releases := make([]DiscographyRelease, 0, len(albums))
	for _, alb := range albums {
		releases = append(releases, DiscographyRelease{
			ID:         alb.ID,
			Title:      alb.Title,
			Type:       alb.RecordType(),
			Year:       alb.Year,
			URL:        fmt.Sprintf("https://music.yandex.ru/album/%d", alb.ID),
			CoverURL:   alb.BestCoverURL(),
			TrackCount: alb.TrackCount,
		})
	}

	httputil.RespondJSON(w, http.StatusOK, DiscographyResponse{
		ArtistName:    artist.Name,
		ArtistID:      artist.ID,
		ArtistAvatar:  artist.AvatarURL(),
		Source:        "yandex",
		TotalReleases: len(releases),
		Releases:      releases,
	})
}

func (m *Module) getYoutubeMusicDiscography(w http.ResponseWriter, r *http.Request, rawURL string) {
	targetURL := rawURL
	if idx := strings.Index(targetURL, "?"); idx != -1 {
		targetURL = targetURL[:idx]
	}

	isReleases := strings.Contains(targetURL, "/releases")
	isPlaylists := strings.Contains(targetURL, "/playlists")
	if !isReleases && !isPlaylists {
		targetURL = strings.TrimRight(targetURL, "/") + "/releases"
	}

	var cookiesPath string
	if m.service != nil {
		cookiesPath = m.service.CookiesPath()
	}

	fetcher := m.getYtReleasesFetcher()
	if fetcher == nil {
		fetcher = defaultYtReleasesFetcher
	}

	items, artistName, err := fetcher(r.Context(), targetURL, cookiesPath)
	if (len(items) == 0 || err != nil) && strings.Contains(targetURL, "/releases") {
		fallbackURL := strings.Replace(targetURL, "/releases", "/playlists", 1)
		if fbItems, fbArtist, fbErr := fetcher(r.Context(), fallbackURL, cookiesPath); fbErr == nil && len(fbItems) > 0 {
			items = fbItems
			if artistName == "" {
				artistName = fbArtist
			}
			err = nil
		}
	}

	if err != nil && len(items) == 0 {
		httputil.RespondError(w, http.StatusBadRequest, fmt.Sprintf("Сбой получения релизов из YouTube: %v", err))
		return
	}

	if len(items) == 0 {
		httputil.RespondError(w, http.StatusNotFound, "Релизы артиста не найдены на YouTube. Рекомендуется использовать ссылку на артиста в Яндекс Музыке, Deezer или Apple Music.")
		return
	}

	if artistName == "" {
		artistName = "Исполнитель YouTube"
	}

	httputil.RespondJSON(w, http.StatusOK, DiscographyResponse{
		ArtistName:    artistName,
		ArtistID:      0,
		Source:        "youtube",
		TotalReleases: len(items),
		Releases:      items,
	})
}

// defaultYtReleasesFetcher выполняет yt-dlp для целевого URL с автоматической
// защитой от запуска реальных внешних процессов в тестовом окружении.
func defaultYtReleasesFetcher(ctx context.Context, targetURL, cookiesPath string) ([]DiscographyRelease, string, error) {
	if os.Getenv("MUSIC_MANAGER_TEST_MODE") == "1" {
		mockReleases := []DiscographyRelease{
			{
				ID:         1,
				Title:      "Mock Test Album",
				Type:       "album",
				Year:       2026,
				URL:        targetURL,
				CoverURL:   "https://lh3.googleusercontent.com/mock-cover",
				TrackCount: 10,
			},
		}
		return mockReleases, "Mock YouTube Artist", nil
	}

	args := []string{
		"--ignore-config",
		"--no-download-archive",
		"--dump-json",
		"--flat-playlist",
		"--no-warnings",
		"--ignore-errors",
	}
	useCookies := resolver.IsValidNetscapeCookiesFile(cookiesPath)
	if useCookies {
		args = append(args, "--cookies", cookiesPath)
	}
	args = append(args, targetURL)

	cmdCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := mediaexec.CommandContext(cmdCtx, "yt-dlp", args...)
	defer cmd.Close()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if useCookies && strings.Contains(stderr.String(), "does not look like a Netscape format cookies file") {
			argsNoCookies := make([]string, 0, len(args))
			for i := 0; i < len(args); i++ {
				if args[i] == "--cookies" {
					i++
					continue
				}
				argsNoCookies = append(argsNoCookies, args[i])
			}
			cmdRetry := mediaexec.CommandContext(cmdCtx, "yt-dlp", argsNoCookies...)
			defer cmdRetry.Close()
			stdout.Reset()
			stderr.Reset()
			cmdRetry.Stdout = &stdout
			cmdRetry.Stderr = &stderr
			err = cmdRetry.Run()
		}
		if err != nil && stdout.Len() == 0 {
			cleanStderr := resolver.CleanYtDlpStderr(stderr.String())
			return nil, "", fmt.Errorf("yt-dlp error: %w: %s", err, cleanStderr)
		}
	}

	type YtReleaseItem struct {
		ID            string `json:"id"`
		Title         string `json:"title"`
		Uploader      string `json:"uploader"`
		Channel       string `json:"channel"`
		URL           string `json:"url"`
		WebpageURL    string `json:"webpage_url"`
		Thumbnail     string `json:"thumbnail"`
		ReleaseYear   int    `json:"release_year"`
		PlaylistCount int    `json:"playlist_count"`
	}

	var items []DiscographyRelease
	artistName := ""
	scanner := bufio.NewScanner(&stdout)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	idx := int64(1)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e YtReleaseItem
		if err := json.Unmarshal(line, &e); err == nil && (e.ID != "" || e.URL != "") {
			if artistName == "" {
				if e.Channel != "" {
					artistName = resolver.CleanChannelName(e.Channel)
				} else if e.Uploader != "" {
					artistName = resolver.CleanChannelName(e.Uploader)
				}
			}

			relURL := e.WebpageURL
			if relURL == "" {
				relURL = e.URL
			}
			if relURL == "" && e.ID != "" {
				relURL = fmt.Sprintf("https://music.youtube.com/playlist?list=%s", e.ID)
			}

			relType := "album"
			if e.PlaylistCount == 1 {
				relType = "single"
			}

			items = append(items, DiscographyRelease{
				ID:         idx,
				Title:      e.Title,
				Type:       relType,
				Year:       e.ReleaseYear,
				URL:        relURL,
				CoverURL:   e.Thumbnail,
				TrackCount: e.PlaylistCount,
			})
			idx++
		}
	}

	return items, artistName, nil
}

func (m *Module) getAppleMusicDiscography(w http.ResponseWriter, r *http.Request, rawURL string) {
	parts := strings.Split(rawURL, "/")
	lastPart := parts[len(parts)-1]
	if idx := strings.Index(lastPart, "?"); idx != -1 {
		lastPart = lastPart[:idx]
	}

	itunesURL := fmt.Sprintf("https://itunes.apple.com/lookup?id=%s&entity=album&limit=100", url.QueryEscape(lastPart))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, itunesURL, nil)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer resp.Body.Close()

	var itunesData struct {
		Results []struct {
			WrapperType       string `json:"wrapperType"`
			ArtistName        string `json:"artistName"`
			ArtistID          int64  `json:"artistId"`
			CollectionID      int64  `json:"collectionId"`
			CollectionName    string `json:"collectionName"`
			ArtworkURL100     string `json:"artworkUrl100"`
			ReleaseDate       string `json:"releaseDate"`
			TrackCount        int    `json:"trackCount"`
			CollectionViewURL string `json:"collectionViewUrl"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&itunesData); err != nil || len(itunesData.Results) == 0 {
		httputil.RespondError(w, http.StatusNotFound, "Дискография артиста Apple Music не найдена")
		return
	}

	artistName := itunesData.Results[0].ArtistName
	artistID := itunesData.Results[0].ArtistID
	var releases []DiscographyRelease

	for _, item := range itunesData.Results {
		if item.WrapperType == "collection" {
			year := 0
			if len(item.ReleaseDate) >= 4 {
				year, _ = strconv.Atoi(item.ReleaseDate[:4])
			}
			cover := strings.Replace(item.ArtworkURL100, "100x100bb.jpg", "600x600bb.jpg", 1)
			relType := "album"
			if item.TrackCount <= 3 {
				relType = "single"
			}
			releases = append(releases, DiscographyRelease{
				ID:         item.CollectionID,
				Title:      item.CollectionName,
				Type:       relType,
				Year:       year,
				URL:        item.CollectionViewURL,
				CoverURL:   cover,
				TrackCount: item.TrackCount,
			})
		}
	}

	httputil.RespondJSON(w, http.StatusOK, DiscographyResponse{
		ArtistName:    artistName,
		ArtistID:      artistID,
		Source:        "apple",
		TotalReleases: len(releases),
		Releases:      releases,
	})
}

// GetReleaseCandidates возвращает список кандидатов цельных релизов (альбомов/сетов)
// для активной задачи, неполного релиза или папки библиотеки.
// GET /downloads/release-candidates
// GET /api/downloads/release-candidates
func (m *Module) GetReleaseCandidates(w http.ResponseWriter, r *http.Request) {
	artist := strings.TrimSpace(httputil.QueryString(r, "artist", ""))
	album := strings.TrimSpace(httputil.QueryString(r, "album", ""))
	jobID := strings.TrimSpace(httputil.QueryString(r, "job_id", ""))
	path := strings.TrimSpace(httputil.QueryString(r, "path", ""))

	var targetTracks []resolver.ResolvedTrack

	// 1. Если указан job_id — загружаем треки из задачи
	if jobID != "" {
		if job, ok := m.service.GetJob(jobID); ok {
			targetTracks = job.Tracks
			if artist == "" && job.ArtistName != nil {
				artist = *job.ArtistName
			}
			if album == "" && job.AlbumTitle != nil {
				album = *job.AlbumTitle
			}
		}
	}

	// 2. Если указан path папки в библиотеке — считываем MP3 через песочницу
	if len(targetTracks) == 0 && path != "" && m.service.MusicSandbox() != nil {
		if realPath, err := m.service.MusicSandbox().Resolve(path, false); err == nil {
			if entries, err := safefs.ReadDir(realPath); err == nil {
				for _, e := range entries {
					if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp3") {
						itemFull := filepath.Join(realPath, e.Name())
						relTrack := filepath.Join(path, e.Name())
						if t, err := id3.ReadTags(itemFull, relTrack); err == nil && t != nil {
							if artist == "" && t.Artist != "" {
								artist = t.Artist
							}
							if album == "" && t.Album != "" {
								album = t.Album
							}
							targetTracks = append(targetTracks, resolver.ResolvedTrack{
								TrackID:     fmt.Sprintf("local_%d", len(targetTracks)+1),
								TrackNumber: t.TrackNumber,
								Title:       t.Title,
								Artist:      t.Artist,
								Album:       t.Album,
								Duration:    int(t.Duration),
							})
						}
					}
				}
			}
		}
	}

	// 3. Если треков все еще нет, но есть artist и album — пробуем найти эталонный треклист через Deezer
	if len(targetTracks) == 0 && artist != "" && album != "" && m.deezerClient != nil {
		if albums, err := m.deezerClient.SearchAlbums(r.Context(), fmt.Sprintf("%s %s", artist, album)); err == nil && len(albums) > 0 {
			if dAlbum, err := m.deezerClient.GetAlbum(r.Context(), strconv.FormatInt(albums[0].ID, 10)); err == nil && dAlbum != nil {
				for _, dt := range dAlbum.Tracks.Data {
					targetTracks = append(targetTracks, resolver.ResolvedTrack{
						TrackID:     fmt.Sprintf("dz_%d", dt.ID),
						TrackNumber: dt.TrackPosition,
						Title:       dt.Title,
						Artist:      dt.Artist.Name,
						Album:       dAlbum.Title,
						Duration:    dt.Duration,
					})
				}
			}
		}
	}

	if artist == "" && album == "" && len(targetTracks) == 0 {
		httputil.RespondError(w, http.StatusBadRequest, "artist and album or job_id are required")
		return
	}

	matcher := m.service.Resolver().FullReleaseMatcher()
	cands, isCyrillic, err := matcher.SearchReleaseCandidates(r.Context(), artist, album, targetTracks, httputil.QueryInt(r, "soundcloud_limit", 30))
	policy := resolver.VersionPolicy(r.URL.Query().Get("version_policy"))
	if policy == "" {
		policy = resolver.PolicyRelease
	}
	if !resolver.ValidVersionPolicy(policy) {
		httputil.RespondError(w, 400, "invalid version_policy")
		return
	}
	cands = resolver.RankReleaseItems(cands, artist, album, targetTracks, policy)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, ReleaseCandidatesResponse{
		VersionPolicy: policy, Selection: "manual",
		Artist:       artist,
		Album:        album,
		IsCyrillic:   isCyrillic,
		TargetTracks: targetTracks,
		Candidates:   cands,
	})
}

// ParseReleaseURL парсит произвольную ссылку на сет/альбом и сопоставляет треклист с целевыми треками.
// POST /downloads/release-candidates/parse-url
// POST /api/downloads/release-candidates/parse-url
func (m *Module) ParseReleaseURL(w http.ResponseWriter, r *http.Request) {
	var req ParseReleaseURLRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		httputil.RespondError(w, http.StatusBadRequest, "url is required")
		return
	}

	if !resolver.IsAllowedURL(req.URL) {
		httputil.RespondError(w, http.StatusBadRequest, "domain is not in allowed whitelist")
		return
	}

	matcher := m.service.Resolver().FullReleaseMatcher()
	cand, err := matcher.ParseAndMatchReleaseURL(r.Context(), req.URL, req.Artist, req.Album, req.TargetTracks)
	if err != nil {
		httputil.RespondError(w, http.StatusBadRequest, fmt.Sprintf("ошибка парсинга ссылки: %v", err))
		return
	}

	httputil.RespondJSON(w, http.StatusOK, cand)
}

// SwitchReleaseSource применяет выбранный источник цельного релиза к задаче и запускает скачивание.
// POST /downloads/releases/switch-source
// POST /api/downloads/releases/switch-source
func (m *Module) SwitchReleaseSource(w http.ResponseWriter, r *http.Request) {
	var req SwitchReleaseSourceRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	result, err := m.service.SwitchReleaseSource(r.Context(), req)
	if err != nil {
		httputil.RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, result)
}
