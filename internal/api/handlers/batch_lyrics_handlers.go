package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func (h *FileHandler) BatchLyricsFetch(w http.ResponseWriter, r *http.Request) {
	h.lyricsState.Lock()
	if h.lyricsState.isRunning {
		resp := map[string]any{
			"is_running":    true,
			"total":         h.lyricsState.total,
			"current":       h.lyricsState.current,
			"found":         h.lyricsState.found,
			"failed":        h.lyricsState.failed,
			"current_track": h.lyricsState.currentTrack,
		}
		h.lyricsState.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	type lyricsJobTrack struct {
		path     string
		title    string
		artist   string
		album    string
		duration int
	}

	rows, err := h.database.Conn().QueryContext(r.Context(), `
		SELECT t.file_path, t.title, COALESCE(ar.name, ''), COALESCE(al.title, ''), COALESCE(t.duration, 0)
		FROM tracks t
		LEFT JOIN artists ar ON t.artist_id = ar.id
		LEFT JOIN albums al ON t.album_id = al.id
		WHERE t.lyrics_status = 'none'
		ORDER BY t.id
	`)
	if err != nil {
		h.lyricsState.Unlock()
		http.Error(w, `{"error":"failed to query tracks"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var tracks []lyricsJobTrack
	for rows.Next() {
		var tr lyricsJobTrack
		var dur sql.NullFloat64
		if err := rows.Scan(&tr.path, &tr.title, &tr.artist, &tr.album, &dur); err == nil {
			if dur.Valid {
				tr.duration = int(dur.Float64)
			}
			tracks = append(tracks, tr)
		}
	}

	if len(tracks) == 0 {
		h.lyricsState.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"is_running":    false,
			"total":         0,
			"current":       0,
			"found":         0,
			"failed":        0,
			"current_track": "",
		})
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	h.lyricsState.isRunning = true
	h.lyricsState.total = len(tracks)
	h.lyricsState.current = 0
	h.lyricsState.found = 0
	h.lyricsState.failed = 0
	h.lyricsState.currentTrack = ""
	h.lyricsState.cancel = cancel
	h.lyricsState.Unlock()

	go func() {
		defer func() {
			h.lyricsState.Lock()
			h.lyricsState.isRunning = false
			h.lyricsState.currentTrack = ""
			h.lyricsState.Unlock()
		}()

		for i, tr := range tracks {
			select {
			case <-ctx.Done():
				return
			default:
			}

			h.lyricsState.Lock()
			h.lyricsState.current = i + 1
			h.lyricsState.currentTrack = fmt.Sprintf("%s - %s", tr.artist, tr.title)
			h.lyricsState.Unlock()

			if h.lyrics != nil {
				res, err := h.lyrics.SearchLyrics(ctx, tr.artist, tr.title, tr.album, tr.duration)
				if err == nil && res != nil && res.Lyrics != "" {
					saveErr := h.lyrics.SaveLyrics(tr.path, res.Lyrics, "auto")
					if saveErr == nil {
						kind := "txt"
						if res.Synced {
							kind = "lrc"
						}
						_, _ = h.database.Conn().Exec("UPDATE tracks SET lyrics_status = ?, updated_at = CURRENT_TIMESTAMP WHERE file_path = ?", kind, tr.path)
						_, _ = h.database.Conn().Exec("UPDATE audit_issues SET is_resolved = 1 WHERE entity_type = 'track' AND file_path = ? AND issue_type = 'missing_lyrics'", tr.path)

						h.lyricsState.Lock()
						h.lyricsState.found++
						h.lyricsState.Unlock()
					} else {
						h.lyricsState.Lock()
						h.lyricsState.failed++
						h.lyricsState.Unlock()
					}
				} else {
					h.lyricsState.Lock()
					h.lyricsState.failed++
					h.lyricsState.Unlock()
				}
			}

			time.Sleep(100 * time.Millisecond)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"is_running":    true,
		"total":         len(tracks),
		"current":       0,
		"found":         0,
		"failed":        0,
		"current_track": "",
	})
}

func (h *FileHandler) BatchLyricsProgress(w http.ResponseWriter, r *http.Request) {
	h.lyricsState.Lock()
	resp := map[string]any{
		"is_running":    h.lyricsState.isRunning,
		"total":         h.lyricsState.total,
		"current":       h.lyricsState.current,
		"found":         h.lyricsState.found,
		"failed":        h.lyricsState.failed,
		"current_track": h.lyricsState.currentTrack,
	}
	h.lyricsState.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
