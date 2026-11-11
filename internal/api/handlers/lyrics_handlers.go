package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/resolver"
)

type LyricsHandler struct {
	service *lyrics.Service
}

func NewLyricsHandler(service *lyrics.Service) *LyricsHandler {
	return &LyricsHandler{service: service}
}

func (h *LyricsHandler) GetLyrics(w http.ResponseWriter, r *http.Request) {
	trackPath := r.URL.Query().Get("path")
	if trackPath == "" {
		trackPath = r.URL.Query().Get("track_path")
	}
	if trackPath == "" {
		http.Error(w, `{"error":"missing path param"}`, http.StatusBadRequest)
		return
	}

	resp, err := h.service.GetLyrics(trackPath)
	if err != nil {
		http.Error(w, `{"error":"failed to get lyrics"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *LyricsHandler) SearchLyrics(w http.ResponseWriter, r *http.Request) {
	artist := r.URL.Query().Get("artist")
	title := r.URL.Query().Get("title")
	album := r.URL.Query().Get("album")
	durationStr := r.URL.Query().Get("duration")
	duration := 0
	if durationStr != "" {
		duration, _ = strconv.Atoi(durationStr)
	}

	var opts []lyrics.SearchOption
	yandexIDStr := r.URL.Query().Get("yandex_id")
	if yandexIDStr == "" {
		yandexIDStr = r.URL.Query().Get("catalog_track_id")
	}
	if yandexIDStr != "" {
		if id, ok := resolver.ExtractYandexTrackID(yandexIDStr, yandexIDStr); ok {
			opts = append(opts, lyrics.WithYandexTrackID(id))
		}
	}

	res, err := h.service.SearchLyricsWithOptions(r.Context(), artist, title, album, duration, opts...)
	if err != nil {
		http.Error(w, `{"error":"search failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

type saveLyricsRequest struct {
	TrackPath string `json:"track_path"`
	Content   string `json:"content"`
	Kind      string `json:"kind"` // auto, lrc, txt
}

func (h *LyricsHandler) SaveLyrics(w http.ResponseWriter, r *http.Request) {
	var req saveLyricsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	kind := req.Kind
	if kind == "" {
		kind = "auto"
	}

	if err := h.service.SaveLyrics(req.TrackPath, req.Content, kind); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
}

func (h *LyricsHandler) DeleteLyrics(w http.ResponseWriter, r *http.Request) {
	trackPath := r.URL.Query().Get("path")
	if trackPath == "" {
		trackPath = r.URL.Query().Get("track_path")
	}
	if trackPath == "" {
		http.Error(w, `{"error":"missing path param"}`, http.StatusBadRequest)
		return
	}

	deleted, err := h.service.DeleteLyrics(trackPath)
	if err != nil {
		http.Error(w, `{"error":"failed to delete lyrics"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"deleted": deleted,
	})
}
