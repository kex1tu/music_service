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
	var tokens [3]string
	keys := []string{"track", "album", "playlist"}
	for i, key := range keys {
		if raw := r.URL.Query().Get(key + "_offset"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < -1 || n > 10000 {
				httputil.RespondError(w, http.StatusBadRequest, "invalid search offset")
				return
			}
			offsets[i] = n
		}
		if tok := strings.TrimSpace(r.URL.Query().Get(key + "_token")); tok != "" {
			if len(tok) > 2048 {
				httputil.RespondError(w, http.StatusBadRequest, "token too long")
				return
			}
			tokens[i] = tok
		}
	}

	if t := strings.TrimSpace(r.URL.Query().Get("type")); t != "" {
		switch t {
		case "track":
			offsets[1], offsets[2] = -1, -1
			tokens[1], tokens[2] = "", ""
		case "album":
			offsets[0], offsets[2] = -1, -1
			tokens[0], tokens[2] = "", ""
		case "playlist":
			offsets[0], offsets[1] = -1, -1
			tokens[0], tokens[1] = "", ""
		default:
			httputil.RespondError(w, http.StatusBadRequest, "invalid type parameter")
			return
		}
	}

	if m.service == nil || m.service.Resolver() == nil {
		httputil.RespondError(w, http.StatusServiceUnavailable, "SoundCloud search is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	groups, err := m.service.Resolver().FullReleaseMatcher().SearchSoundCloudWithTokens(ctx, query, offsets, tokens)
	if err != nil {
		if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "mismatch") {
			httputil.RespondError(w, http.StatusBadRequest, err.Error())
			return
		}
		httputil.RespondError(w, http.StatusBadGateway, "SoundCloud временно недоступен. Повторите поиск.")
		return
	}
	httputil.RespondJSON(w, http.StatusOK, map[string]any{"query": query, "groups": groups})
}
