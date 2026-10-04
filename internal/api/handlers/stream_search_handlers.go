package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (h *FileHandler) StreamAudio(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}

	fullPath, err := h.musicSandbox.Resolve(relPath, false)
	if err != nil {
		// Try tmp sandbox if path looks like temporary download
		if h.tmpSandbox != nil {
			cleanRel := strings.TrimPrefix(relPath, "tmp_downloads/")
			cleanRel = strings.TrimPrefix(cleanRel, "tmp/")
			if p, err2 := h.tmpSandbox.Resolve(cleanRel, false); err2 == nil {
				fullPath = p
			}
		}
	}

	if fullPath == "" || !fileExists(fullPath) {
		http.Error(w, "audio file not found", http.StatusNotFound)
		return
	}

	// ServeFile natively handles HTTP 206 Partial Content range requests for seeking
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeFile(w, r, fullPath)
}

func (h *FileHandler) SearchLibrary(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		q = strings.TrimSpace(r.URL.Query().Get("query"))
	}

	result, err := h.database.SearchLibrary(r.Context(), q, 50)
	if err != nil {
		http.Error(w, `{"error":"search failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
