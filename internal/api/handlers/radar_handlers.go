package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/db"
	"music-manager/internal/services/radar"
)

// RadarHandler обрабатывает HTTP-запросы ленты новинок, отслеживания артистов и обнаружения пробелов.
type RadarHandler struct {
	radar *radar.Service
	db    *db.DB
}

// NewRadarHandler создает новый RadarHandler.
func NewRadarHandler(r *radar.Service, database *db.DB) *RadarHandler {
	return &RadarHandler{
		radar: r,
		db:    database,
	}
}

// GetFeed возвращает ленту релизов Радара с фильтрами и пагинацией.
func (h *RadarHandler) GetFeed(w http.ResponseWriter, r *http.Request) {
	relType := r.URL.Query().Get("type")
	search := r.URL.Query().Get("search")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 30
	}
	offset := (page - 1) * limit

	items, total, err := h.db.GetRadarFeed(r.Context(), relType, search, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	unread, _ := h.db.GetRadarUnreadCount(r.Context())

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items":  items,
		"total":  total,
		"page":   page,
		"limit":  limit,
		"unread": unread,
	})
}

// GetTrackedArtists возвращает список всех исполнителей в каталоге отслеживания.
func (h *RadarHandler) GetTrackedArtists(w http.ResponseWriter, r *http.Request) {
	onlyFollowed := r.URL.Query().Get("followed") == "true"
	artists, err := h.db.GetTrackedArtists(r.Context(), onlyFollowed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items": artists,
		"total": len(artists),
	})
}

type addTrackedArtistReq struct {
	Name string `json:"name"`
}

// AddTrackedArtist добавляет артиста на отслеживание вручную.
func (h *RadarHandler) AddTrackedArtist(w http.ResponseWriter, r *http.Request) {
	var req addTrackedArtistReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		http.Error(w, `{"error":"invalid artist name"}`, http.StatusBadRequest)
		return
	}

	if err := h.db.UpsertTrackedArtist(r.Context(), req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type followArtistReq struct {
	Followed bool `json:"followed"`
}

// FollowArtist включает/отключает отслеживание артиста.
func (h *RadarHandler) FollowArtist(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var req followArtistReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	if err := h.radar.FollowArtist(r.Context(), name, req.Followed); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type autoDownloadReq struct {
	AutoDownload bool `json:"auto_download"`
}

// SetAutoDownload включает/отключает автозагрузку новых релизов артиста.
func (h *RadarHandler) SetAutoDownload(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var req autoDownloadReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	if err := h.radar.SetAutoDownload(r.Context(), name, req.AutoDownload); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type customLinksReq struct {
	Alias         string `json:"alias"`
	DeezerID      string `json:"deezer_id"`
	MusicBrainzID string `json:"musicbrainz_id"`
	YandexID      string `json:"yandex_id"`
}

// UpdateCustomLinks обновляет пользовательские идентификаторы артиста в каталогах.
func (h *RadarHandler) UpdateCustomLinks(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var req customLinksReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	if err := h.radar.UpdateArtistCustomLinks(r.Context(), name, req.Alias, req.DeezerID, req.MusicBrainzID, req.YandexID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// GetArtistDiscography возвращает дискографию артиста с маркировкой статусов релизов.
func (h *RadarHandler) GetArtistDiscography(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	releases, err := h.db.GetArtistDiscography(r.Context(), name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tracked, _ := h.db.GetTrackedArtistByName(r.Context(), name)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"artist":   name,
		"tracked":  tracked,
		"releases": releases,
	})
}

type scanReq struct {
	Artist string `json:"artist"`
}

// TriggerScan запускает сканирование релизов для одного артиста или всей медиатеки.
func (h *RadarHandler) TriggerScan(w http.ResponseWriter, r *http.Request) {
	var req scanReq
	_ = json.NewDecoder(r.Body).Decode(&req)

	if strings.TrimSpace(req.Artist) != "" {
		res, err := h.radar.ScanArtist(r.Context(), req.Artist)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
		return
	}

	if err := h.radar.ScanAll(r.Context(), "manual_ui"); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

// GetScanProgress возвращает текущий статус фонового сканирования.
func (h *RadarHandler) GetScanProgress(w http.ResponseWriter, r *http.Request) {
	prog := h.radar.GetProgress()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(prog)
}

// CancelScan прерывает текущее сканирование.
func (h *RadarHandler) CancelScan(w http.ResponseWriter, r *http.Request) {
	h.radar.CancelScan()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "cancelled"})
}

type setReleaseStatusReq struct {
	Status string `json:"status"`
}

// SetReleaseStatus изменяет статус релиза ('ignored', 'downloaded', 'new').
func (h *RadarHandler) SetReleaseStatus(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var req setReleaseStatusReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Status == "" {
		http.Error(w, `{"error":"invalid status"}`, http.StatusBadRequest)
		return
	}

	if err := h.db.SetRadarReleaseStatus(r.Context(), id, req.Status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// GetUnreadCount возвращает количество непросмотренных новинок Радара.
func (h *RadarHandler) GetUnreadCount(w http.ResponseWriter, r *http.Request) {
	count, err := h.db.GetRadarUnreadCount(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"unread": count})
}
