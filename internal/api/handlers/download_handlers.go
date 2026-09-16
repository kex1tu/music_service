package handlers


import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

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
func isYouTubeArtistURL(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	if !strings.Contains(lower, "youtube.com") && !strings.Contains(lower, "youtu.be") {
		return false
	}
	// Поддерживаем: /channel/, /@handle, /user/, /c/
	return strings.Contains(lower, "/channel/") ||
		strings.Contains(lower, "/@") ||
		strings.Contains(lower, "/user/") ||
		strings.Contains(lower, "/c/")
}
// GetDiscography обрабатывает запрос дискографии для всех поддерживаемых фронтендом сервисов
func (h *DownloadHandler) GetDiscography(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if rawURL == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Параметр url обязателен"})
		return
	}

	lower := strings.ToLower(rawURL)

	// 1. YouTube & YouTube Music
	if strings.Contains(lower, "youtube.com") || strings.Contains(lower, "youtu.be") {
		h.getYoutubeMusicDiscography(w, r, rawURL)
		return
	}

	// 2. Deezer
	if strings.Contains(lower, "deezer.com") || strings.Contains(lower, "deezer.page.link") {
		h.getDeezerDiscography(w, r, rawURL)
		return
	}

	// 3. Apple Music / iTunes
	if strings.Contains(lower, "music.apple.com") || strings.Contains(lower, "itunes.apple.com") {
		h.getAppleMusicDiscography(w, r, rawURL)
		return
	}

	// 4. Яндекс Музыка или Spotify: пока не реализован прямой API сборщик дискографии
	if strings.Contains(lower, "yandex") || strings.Contains(lower, "spotify.com") {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"detail": "Пакетная дискография для Яндекс Музыки и Spotify пока не поддерживается напрямую. Рекомендуется использовать ссылку на артиста в Deezer, Apple Music или YouTube Music.",
		})
		return
	}

	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"detail": "Нераспознанный URL артиста. Поддерживаются Deezer, YouTube Music и Apple Music.",
	})
}

// getDeezerDiscography — стандартная дискография Deezer
func (h *DownloadHandler) getDeezerDiscography(w http.ResponseWriter, r *http.Request, rawURL string) {
	if h.deezerClient == nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Deezer клиент не инициализирован"})
		return
	}

	resourceType, artistID, err := h.deezerClient.ParseURL(r.Context(), rawURL)
	if err != nil || resourceType != "artist" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Некорректная ссылка на артиста Deezer"})
		return
	}

	artist, err := h.deezerClient.GetArtist(r.Context(), artistID)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": fmt.Sprintf("Не удалось получить профиль артиста Deezer: %v", err)})
		return
	}

	albums, err := h.deezerClient.GetArtistAlbums(r.Context(), artistID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": fmt.Sprintf("Не удалось получить дискографию Deezer: %v", err)})
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

	_ = json.NewEncoder(w).Encode(DiscographyResponse{
		ArtistName:    artist.Name,
		ArtistID:      artist.ID,
		TotalReleases: len(releases),
		Releases:      releases,
	})
}

// getYoutubeMusicDiscography — извлечение релизов YouTube Music через yt-dlp
func (h *DownloadHandler) getYoutubeMusicDiscography(w http.ResponseWriter, r *http.Request, rawURL string) {
	targetURL := rawURL
	// Убираем параметры запроса (?si=...)
	if idx := strings.Index(targetURL, "?"); idx != -1 {
		targetURL = targetURL[:idx]
	}

	// Если ссылка ведет на канал, добавляем /releases
	if strings.Contains(targetURL, "/channel/") && !strings.Contains(targetURL, "/releases") {
		targetURL = strings.TrimRight(targetURL, "/") + "/releases"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	args := []string{
		"--ignore-config",
		"--no-download-archive",
		"--dump-json",
		"--flat-playlist",
		"--no-warnings",
		"--ignore-errors",
		targetURL,
	}

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil && stdout.Len() == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": fmt.Sprintf("Сбой получения релизов из YouTube Music: %s", stderr.String())})
		return
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
					artistName = e.Channel
				} else if e.Uploader != "" {
					artistName = e.Uploader
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

	if len(items) == 0 {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Релизы артиста не найдены на YouTube Music"})
		return
	}

	if artistName == "" {
		artistName = "Исполнитель YouTube Music"
	}

	_ = json.NewEncoder(w).Encode(DiscographyResponse{
		ArtistName:    artistName,
		ArtistID:      0,
		TotalReleases: len(items),
		Releases:      items,
	})
}

// getAppleMusicDiscography — получение дискографии через открытый iTunes Search API
func (h *DownloadHandler) getAppleMusicDiscography(w http.ResponseWriter, r *http.Request, rawURL string) {
	// Извлекаем Apple Music Artist ID: https://music.apple.com/ru/artist/name/123456
	parts := strings.Split(rawURL, "/")
	lastPart := parts[len(parts)-1]
	if idx := strings.Index(lastPart, "?"); idx != -1 {
		lastPart = lastPart[:idx]
	}

	itunesURL := fmt.Sprintf("https://itunes.apple.com/lookup?id=%s&entity=album&limit=100", url.QueryEscape(lastPart))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, itunesURL, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": err.Error()})
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": err.Error()})
		return
	}
	defer resp.Body.Close()

	var itunesData struct {
		Results []struct {
			WrapperType    string `json:"wrapperType"`
			ArtistName     string `json:"artistName"`
			ArtistID       int64  `json:"artistId"`
			CollectionID   int64  `json:"collectionId"`
			CollectionName string `json:"collectionName"`
			ArtworkURL100  string `json:"artworkUrl100"`
			ReleaseDate    string `json:"releaseDate"`
			TrackCount     int    `json:"trackCount"`
			CollectionViewURL string `json:"collectionViewUrl"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&itunesData); err != nil || len(itunesData.Results) == 0 {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Дискография артиста Apple Music не найдена"})
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
			coverBig := strings.Replace(item.ArtworkURL100, "100x100bb", "600x600bb", 1)
			releases = append(releases, DiscographyRelease{
				ID:         item.CollectionID,
				Title:      item.CollectionName,
				Type:       "album",
				Year:       year,
				URL:        item.CollectionViewURL,
				CoverURL:   coverBig,
				TrackCount: item.TrackCount,
			})
		}
	}

	_ = json.NewEncoder(w).Encode(DiscographyResponse{
		ArtistName:    artistName,
		ArtistID:      artistID,
		TotalReleases: len(releases),
		Releases:      releases,
	})
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
