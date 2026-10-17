package radio

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/httputil"
	"music-manager/internal/db"
)

// HandleStreamOptions обрабатывает preflight CORS OPTIONS запросы для радиопотока.
func (m *Module) HandleStreamOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(http.StatusNoContent)
}

// GetWaveQueue обрабатывает GET /api/radio/wave/queue.
func (m *Module) GetWaveQueue(w http.ResponseWriter, r *http.Request) {
	limit := httputil.QueryInt(r, "limit", 25)

	tracks, err := m.service.GetWaveQueue(r.Context(), limit)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if tracks == nil {
		tracks = []*db.TrackEntity{}
	}

	httputil.RespondJSON(w, http.StatusOK, WaveQueueResponse{
		Tracks: tracks,
		Count:  len(tracks),
	})
}

// GetWaveStats обрабатывает GET /api/radio/wave/stats.
func (m *Module) GetWaveStats(w http.ResponseWriter, r *http.Request) {
	stats, err := m.service.GetWaveStats(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, stats)
}

// GetNowPlaying обрабатывает GET /api/radio/now-playing и GET /api/radio/stream/now-playing.
func (m *Module) GetNowPlaying(w http.ResponseWriter, r *http.Request) {
	stationType := httputil.QueryString(r, "type", "wave")
	stationParam := httputil.QueryString(r, "param", "")

	current, history := m.service.GetNowPlaying(stationType, stationParam)

	httputil.RespondJSON(w, http.StatusOK, NowPlayingResponse{
		CurrentTrack: current,
		History:      history,
	})
}

// StreamWave обрабатывает GET /api/radio/stream/wave и GET /api/radio/stream/wave.mp3.
func (m *Module) StreamWave(w http.ResponseWriter, r *http.Request) {
	if !m.service.CheckStreamAuth(r) {
		httputil.RespondError(w, http.StatusUnauthorized, "Unauthorized: invalid or missing stream token")
		return
	}
	m.service.ServeStream(w, r, "wave", "")
}

// StreamGenre обрабатывает GET /api/radio/stream/genre/{name}.
func (m *Module) StreamGenre(w http.ResponseWriter, r *http.Request) {
	if !m.service.CheckStreamAuth(r) {
		httputil.RespondError(w, http.StatusUnauthorized, "Unauthorized: invalid or missing stream token")
		return
	}
	genre, _ := url.PathUnescape(chi.URLParam(r, "name"))
	genre = strings.TrimSpace(strings.TrimSuffix(genre, ".mp3"))
	m.service.ServeStream(w, r, "genre", genre)
}

// StreamArtist обрабатывает GET /api/radio/stream/artist/{name}.
func (m *Module) StreamArtist(w http.ResponseWriter, r *http.Request) {
	if !m.service.CheckStreamAuth(r) {
		httputil.RespondError(w, http.StatusUnauthorized, "Unauthorized: invalid or missing stream token")
		return
	}
	artist, _ := url.PathUnescape(chi.URLParam(r, "name"))
	artist = strings.TrimSpace(strings.TrimSuffix(artist, ".mp3"))
	m.service.ServeStream(w, r, "artist", artist)
}

// GetGenres обрабатывает GET /api/radio/genres.
func (m *Module) GetGenres(w http.ResponseWriter, r *http.Request) {
	limit := httputil.QueryInt(r, "limit", 50)
	genres, err := m.service.GetGenres(r.Context(), limit)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, GenresResponse{
		Genres: genres,
	})
}

// GenerateM3U обрабатывает POST /api/radio/m3u/generate.
func (m *Module) GenerateM3U(w http.ResponseWriter, r *http.Request) {
	filePath, err := m.service.GenerateM3U(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, "failed to generate M3U: "+err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, GenerateM3UResponse{
		Status:       "ok",
		PlaylistPath: filePath,
	})
}

// SyncNavidromeRadio обрабатывает POST /api/radio/navidrome/sync-radio.
func (m *Module) SyncNavidromeRadio(w http.ResponseWriter, r *http.Request) {
	var req SyncRadioRequest
	_ = httputil.DecodeJSON(r, &req)

	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		host = fwdHost
	}

	res, err := m.service.SyncNavidromeRadio(r.Context(), req, scheme, host)
	if err != nil {
		httputil.RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, res)
}
