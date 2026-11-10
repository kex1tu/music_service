package downloader

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"music-manager/internal/core/httputil"
)

func (m *Module) SearchSoundCloud(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" || len([]rune(query)) > 200 {
		httputil.RespondError(w, http.StatusBadRequest, "Введите запрос длиной от 1 до 200 символов")
		return
	}
	var offsets [3]int
	for i, key := range []string{"track_offset", "album_offset", "playlist_offset"} {
		if raw := r.URL.Query().Get(key); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < -1 || n > 10000 {
				httputil.RespondError(w, http.StatusBadRequest, "invalid search offset")
				return
			}
			offsets[i] = n
		}
	}
	if m.service == nil || m.service.Resolver() == nil {
		httputil.RespondError(w, http.StatusServiceUnavailable, "SoundCloud search is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	groups, err := m.service.Resolver().FullReleaseMatcher().SearchSoundCloud(ctx, query, offsets)
	if err != nil {
		httputil.RespondError(w, http.StatusBadGateway, "SoundCloud временно недоступен. Повторите поиск.")
		return
	}
	httputil.RespondJSON(w, http.StatusOK, map[string]any{"query": query, "groups": groups})
}
