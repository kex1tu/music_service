package radar

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/httputil"
	"music-manager/internal/core/textkey"
)

// GetFeed обрабатывает GET /api/radar/feed.
func (m *Module) GetFeed(w http.ResponseWriter, r *http.Request) {
	relType := httputil.QueryString(r, "type", "all")
	search := httputil.QueryString(r, "search", "")
	page := httputil.QueryInt(r, "page", 1)
	if page < 1 {
		page = 1
	}
	limit := httputil.QueryInt(r, "limit", 30)
	if limit <= 0 {
		limit = 30
	}
	offset := (page - 1) * limit

	items, total, err := m.service.GetFeed(r.Context(), relType, search, limit, offset)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	unread, _ := m.service.GetUnreadCount(r.Context())

	httputil.RespondJSON(w, http.StatusOK, FeedResponse{
		Items:  items,
		Total:  total,
		Page:   page,
		Limit:  limit,
		Unread: unread,
	})
}

// GetTrackedArtists обрабатывает GET /api/radar/artists.
func (m *Module) GetTrackedArtists(w http.ResponseWriter, r *http.Request) {
	onlyFollowed := httputil.QueryBool(r, "followed", false)
	artists, err := m.service.GetTrackedArtists(r.Context(), onlyFollowed)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if term := textkey.Search(r.URL.Query().Get("search")); term != "" {
		filtered := make([]TrackedArtist, 0)
		for _, artist := range artists {
			if strings.Contains(textkey.Search(artist.Name), term) ||
				strings.Contains(textkey.Search(artist.CustomSearchAlias), term) {
				filtered = append(filtered, artist)
			}
		}
		artists = filtered
	}

	httputil.RespondJSON(w, http.StatusOK, TrackedArtistsResponse{
		Items: artists,
		Total: len(artists),
	})
}

// AddTrackedArtist обрабатывает POST /api/radar/artists.
func (m *Module) AddTrackedArtist(w http.ResponseWriter, r *http.Request) {
	var req AddTrackedArtistRequest
	if err := httputil.DecodeJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		httputil.RespondError(w, http.StatusBadRequest, "Некорректное имя артиста")
		return
	}

	if err := m.service.AddTrackedArtist(r.Context(), req.Name); err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// FollowArtist обрабатывает POST /api/radar/artists/{name}/follow.
func (m *Module) FollowArtist(w http.ResponseWriter, r *http.Request) {
	name, _ := url.PathUnescape(chi.URLParam(r, "name"))
	var req FollowArtistRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "Некорректный JSON запрос")
		return
	}

	if err := m.service.FollowArtist(r.Context(), name, req.Followed, req.ArtistID); err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// SetAutoDownload обрабатывает POST /api/radar/artists/{name}/auto-download.
func (m *Module) SetAutoDownload(w http.ResponseWriter, r *http.Request) {
	name, _ := url.PathUnescape(chi.URLParam(r, "name"))
	var req AutoDownloadRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "Некорректный JSON запрос")
		return
	}

	if err := m.service.SetAutoDownload(r.Context(), name, req.AutoDownload, req.ArtistID); err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// UpdateCustomLinks обрабатывает POST /api/radar/artists/{name}/custom-links.
func (m *Module) UpdateCustomLinks(w http.ResponseWriter, r *http.Request) {
	name, _ := url.PathUnescape(chi.URLParam(r, "name"))
	var req CustomLinksRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "Некорректный JSON запрос")
		return
	}

	if err := m.service.UpdateCustomLinks(r.Context(), name, req.Alias, req.DeezerID, req.MusicBrainzID, req.YandexID, req.ArtistID); err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetArtistDiscography обрабатывает GET /api/radar/artists/{name}/discography.
func (m *Module) GetArtistDiscography(w http.ResponseWriter, r *http.Request) {
	name, _ := url.PathUnescape(chi.URLParam(r, "name"))
	releases, tracked, err := m.service.GetArtistDiscography(r.Context(), name)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, ArtistDiscographyResponse{
		Artist:   name,
		Tracked:  tracked,
		Releases: releases,
	})
}

// TriggerScan обрабатывает POST /api/radar/scan.
func (m *Module) TriggerScan(w http.ResponseWriter, r *http.Request) {
	var req ScanRequest
	_ = httputil.DecodeJSON(r, &req)

	if strings.TrimSpace(req.Artist) != "" {
		res, err := m.service.ScanArtist(r.Context(), req.Artist)
		if err != nil {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		httputil.RespondJSON(w, http.StatusOK, res)
		return
	}

	if err := m.service.ScanAll(r.Context(), "manual_ui"); err != nil {
		httputil.RespondError(w, http.StatusConflict, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

// GetScanProgress обрабатывает GET /api/radar/progress.
func (m *Module) GetScanProgress(w http.ResponseWriter, r *http.Request) {
	prog := m.service.GetProgress()
	httputil.RespondJSON(w, http.StatusOK, prog)
}

// CancelScan обрабатывает POST /api/radar/cancel.
func (m *Module) CancelScan(w http.ResponseWriter, r *http.Request) {
	m.service.CancelScan()
	httputil.RespondJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// SetReleaseStatus обрабатывает POST /api/radar/releases/{id}/status.
func (m *Module) SetReleaseStatus(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "Некорректный ID релиза")
		return
	}

	var req SetReleaseStatusRequest
	if err := httputil.DecodeJSON(r, &req); err != nil || strings.TrimSpace(req.Status) == "" {
		httputil.RespondError(w, http.StatusBadRequest, "Некорректный статус релиза")
		return
	}

	if err := m.service.SetReleaseStatus(r.Context(), id, req.Status); err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetUnreadCount обрабатывает GET /api/radar/unread-count.
func (m *Module) GetUnreadCount(w http.ResponseWriter, r *http.Request) {
	count, err := m.service.GetUnreadCount(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, UnreadCountResponse{Unread: count})
}
