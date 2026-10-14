package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/config"
	"music-manager/internal/core/auth"
	"music-manager/internal/db"
	"music-manager/internal/services/radio"
)

// RadioHandler обрабатывает запросы радиовещания «Моя Волна», генерации M3U и синхронизации с Navidrome.
type RadioHandler struct {
	selector        *radio.WaveSelector
	streamer        *radio.IcyStreamer
	m3u             *radio.M3UGenerator
	subsonic        *radio.NavidromeSubsonicClient
	navidromeReader *radio.NavidromeReader
	db              *db.DB
	cfg             *config.Config
}

// NewRadioHandler создает новый RadioHandler.
func NewRadioHandler(
	selector *radio.WaveSelector,
	streamer *radio.IcyStreamer,
	m3u *radio.M3UGenerator,
	subsonic *radio.NavidromeSubsonicClient,
	navidromeReader *radio.NavidromeReader,
	database *db.DB,
	cfg *config.Config,
) *RadioHandler {
	return &RadioHandler{
		selector:        selector,
		streamer:        streamer,
		m3u:             m3u,
		subsonic:        subsonic,
		navidromeReader: navidromeReader,
		db:              database,
		cfg:             cfg,
	}
}

// checkStreamAuth проверяет авторизацию для ICY потока (токен в query ?token=..., заголовок X-Stream-Token, заголовок Bearer, либо сессионная cookie).
func (h *RadioHandler) checkStreamAuth(r *http.Request) bool {
	if h.cfg == nil || h.cfg.RadioStreamToken == "" {
		return true
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.Header.Get("X-Stream-Token")
	}
	if token != "" && token == h.cfg.RadioStreamToken {
		return true
	}
	authHdr := r.Header.Get("Authorization")
	if strings.HasPrefix(authHdr, "Bearer ") && strings.TrimPrefix(authHdr, "Bearer ") == h.cfg.RadioStreamToken {
		return true
	}
	if cookie, err := r.Cookie(auth.CookieName); err == nil && cookie.Value != "" {
		if _, valid := auth.ValidateSessionToken(cookie.Value, h.cfg.SecretKey); valid {
			return true
		}
	}
	return false
}

// HandleStreamOptions обрабатывает preflight CORS OPTIONS запросы для радиопотока.
func (h *RadioHandler) HandleStreamOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(http.StatusNoContent)
}

// GetWaveQueue возвращает сформированную очередь треков для веб-плеера «Моя Волна».
func (h *RadioHandler) GetWaveQueue(w http.ResponseWriter, r *http.Request) {
	limit := 25
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	tracks, err := h.selector.GeneratePlaylist(r.Context(), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tracks": tracks,
		"count":  len(tracks),
	})
}

// GetWaveStats возвращает общую статистику для панели Радио.
func (h *RadioHandler) GetWaveStats(w http.ResponseWriter, r *http.Request) {
	genres, _ := h.db.GetAvailableGenres(r.Context(), 10)
	naviAvailable := h.navidromeReader != nil && h.navidromeReader.IsAvailable()
	subsonicConfigured := h.subsonic != nil && h.subsonic.IsConfigured()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"top_genres":          genres,
		"navidrome_available": naviAvailable,
		"subsonic_configured": subsonicConfigured,
		"wave_size":           h.cfg.RadioWaveSize,
		"stream_token_set":    h.cfg.RadioStreamToken != "",
		"stream_token":        h.cfg.RadioStreamToken,
	})
}

// StreamWave организует бесконечный ICY-поток «Моя Волна» без перекодирования.
func (h *RadioHandler) StreamWave(w http.ResponseWriter, r *http.Request) {
	if !h.checkStreamAuth(r) {
		http.Error(w, "Unauthorized: invalid or missing stream token", http.StatusUnauthorized)
		return
	}
	h.streamer.ServeHTTP(w, r, "wave", "")
}

// StreamGenre транслирует непрерывный поток треков выбранного жанра.
func (h *RadioHandler) StreamGenre(w http.ResponseWriter, r *http.Request) {
	if !h.checkStreamAuth(r) {
		http.Error(w, "Unauthorized: invalid or missing stream token", http.StatusUnauthorized)
		return
	}
	genre := chi.URLParam(r, "name")
	h.streamer.ServeHTTP(w, r, "genre", genre)
}

// StreamArtist транслирует непрерывный поток треков выбранного артиста.
func (h *RadioHandler) StreamArtist(w http.ResponseWriter, r *http.Request) {
	if !h.checkStreamAuth(r) {
		http.Error(w, "Unauthorized: invalid or missing stream token", http.StatusUnauthorized)
		return
	}
	artist := chi.URLParam(r, "name")
	h.streamer.ServeHTTP(w, r, "artist", artist)
}

// GetGenres возвращает доступные жанры из локальной базы.
func (h *RadioHandler) GetGenres(w http.ResponseWriter, r *http.Request) {
	limit := 50
	genres, err := h.db.GetAvailableGenres(r.Context(), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"genres": genres,
	})
}

// GenerateM3U генерирует или обновляет плейлист «Playlists/Моя Волна.m3u».
func (h *RadioHandler) GenerateM3U(w http.ResponseWriter, r *http.Request) {
	filePath, err := h.m3u.GenerateWaveM3U(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to generate M3U: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":        "ok",
		"playlist_path": filePath,
	})
}

// SyncNavidromeRadio регистрирует или обновляет поток «Моя Волна» в Navidrome через Subsonic REST API.
func (h *RadioHandler) SyncNavidromeRadio(w http.ResponseWriter, r *http.Request) {
	if h.subsonic == nil || !h.subsonic.IsConfigured() {
		http.Error(w, "Реквизиты Navidrome Subsonic API (NAVIDROME_URL, NAVIDROME_USER, NAVIDROME_PASSWORD) не настроены в .env", http.StatusBadRequest)
		return
	}

	var reqBody struct {
		StreamURL string `json:"stream_url"`
		Name      string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&reqBody)

	stationName := "Моя Волна"
	if strings.TrimSpace(reqBody.Name) != "" {
		stationName = strings.TrimSpace(reqBody.Name)
	}

	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}

	host := r.Host
	if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		host = fwdHost
	}

	streamURL := strings.TrimSpace(reqBody.StreamURL)
	if streamURL == "" {
		streamURL = fmt.Sprintf("%s://%s/api/radio/stream/wave", scheme, host)
		if h.cfg != nil && h.cfg.RadioStreamToken != "" {
			streamURL = fmt.Sprintf("%s?token=%s", streamURL, h.cfg.RadioStreamToken)
		}
	}
	homePageURL := fmt.Sprintf("%s://%s", scheme, host)

	err := h.subsonic.CreateRadioStation(r.Context(), stationName, streamURL, homePageURL)
	if err != nil {
		http.Error(w, fmt.Sprintf("Ошибка экспорта радиостанции в Navidrome: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":     "ok",
		"name":       stationName,
		"stream_url": streamURL,
	})
}
