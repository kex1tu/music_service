package audit

import (
	"context"
	"net/http"

	"music-manager/internal/core/httputil"
	"music-manager/internal/db"
)

type artistConflictIndex interface {
	ArtistConflicts(context.Context) ([]db.ArtistConflict, error)
	ResolveArtistConflict(context.Context, string, bool, int64) error
}

func (m *Module) ArtistConflicts(w http.ResponseWriter, r *http.Request) {
	index, ok := m.service.scanner.(artistConflictIndex)
	if !ok {
		httputil.RespondError(w, http.StatusServiceUnavailable, "Индекс медиатеки недоступен")
		return
	}
	conflicts, err := index.ArtistConflicts(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, "Не удалось прочитать конфликты исполнителей")
		return
	}
	httputil.RespondJSON(w, http.StatusOK, conflicts)
}

func (m *Module) ResolveArtistConflict(w http.ResponseWriter, r *http.Request) {
	index, ok := m.service.scanner.(artistConflictIndex)
	if !ok {
		httputil.RespondError(w, http.StatusServiceUnavailable, "Индекс медиатеки недоступен")
		return
	}
	var req struct {
		Path        string `json:"path"`
		AlbumArtist bool   `json:"album_artist"`
		ArtistID    int64  `json:"artist_id"`
	}
	if err := httputil.DecodeJSON(r, &req); err != nil || req.Path == "" || req.ArtistID <= 0 {
		httputil.RespondError(w, http.StatusBadRequest, "Укажите файл и исполнителя")
		return
	}
	if err := index.ResolveArtistConflict(r.Context(), req.Path, req.AlbumArtist, req.ArtistID); err != nil {
		httputil.RespondError(w, http.StatusConflict, "Не удалось применить выбор. Обновите список конфликтов.")
		return
	}
	httputil.RespondJSON(w, http.StatusOK, map[string]bool{"success": true})
}
