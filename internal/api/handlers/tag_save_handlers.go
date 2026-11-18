package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"music-manager/internal/id3"
)

func (h *TagHandler) SaveTags(w http.ResponseWriter, r *http.Request) {
	var tags id3.TrackTags
	if err := json.NewDecoder(r.Body).Decode(&tags); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	fullPath, err := h.resolvePath(tags.Path)
	if err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}

	if err := id3.WriteTags(fullPath, tags, nil); err != nil {
		http.Error(w, `{"error":"failed to write tags"}`, http.StatusInternalServerError)
		return
	}

	if h.database != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = h.database.ReconcileAllRadarReleases(ctx)
		}()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "track": tags})
}

type batchSaveRequest struct {
	Album struct {
		AlbumArtist string `json:"album_artist"`
		Album       string `json:"album"`
		Year        *int   `json:"year"`
		Genre       string `json:"genre"`
	} `json:"album"`
	Tracks      []id3.TrackTags `json:"tracks"`
	SourceDir   string          `json:"source_dir"`
	CoverBase64 string          `json:"cover_base64,omitempty"`
	CoverURL    string          `json:"cover_url,omitempty"`
	Reorganize  bool            `json:"reorganize"`
}

func (h *TagHandler) BatchSaveTags(w http.ResponseWriter, r *http.Request) {
	var req batchSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	// Single-track synchronization: if len(req.Tracks) == 1, populate album-level metadata from track if missing
	if len(req.Tracks) == 1 {
		t := &req.Tracks[0]
		if strings.TrimSpace(req.Album.AlbumArtist) == "" && strings.TrimSpace(t.Artist) != "" {
			req.Album.AlbumArtist = strings.TrimSpace(t.Artist)
		}
		if strings.TrimSpace(req.Album.Album) == "" || strings.EqualFold(req.Album.Album, "single") || strings.EqualFold(req.Album.Album, "singles") {
			if strings.TrimSpace(t.Title) != "" {
				req.Album.Album = strings.TrimSpace(t.Title)
			}
		}
		if (req.Album.Year == nil || *req.Album.Year == 0) && t.Year > 0 {
			yr := t.Year
			req.Album.Year = &yr
		}
		if strings.TrimSpace(req.Album.Genre) == "" && strings.TrimSpace(t.Genre) != "" {
			req.Album.Genre = strings.TrimSpace(t.Genre)
		}
	}

	// Apply album fields to tracks ONLY if track's corresponding field is empty
	for i := range req.Tracks {
		if strings.TrimSpace(req.Tracks[i].Album) == "" {
			if strings.TrimSpace(req.Album.Album) != "" && !strings.EqualFold(req.Album.Album, "singles") && !strings.EqualFold(req.Album.Album, "single") {
				req.Tracks[i].Album = strings.TrimSpace(req.Album.Album)
			} else if strings.TrimSpace(req.Tracks[i].Title) != "" {
				req.Tracks[i].Album = strings.TrimSpace(req.Tracks[i].Title)
			}
		} else if strings.EqualFold(req.Tracks[i].Album, "single") || strings.EqualFold(req.Tracks[i].Album, "singles") {
			if strings.TrimSpace(req.Tracks[i].Title) != "" {
				req.Tracks[i].Album = strings.TrimSpace(req.Tracks[i].Title)
			}
		}
		if strings.TrimSpace(req.Tracks[i].AlbumArtist) == "" {
			req.Tracks[i].AlbumArtist = strings.TrimSpace(req.Album.AlbumArtist)
		}
		if req.Album.Year != nil && *req.Album.Year > 0 && req.Tracks[i].Year == 0 {
			req.Tracks[i].Year = *req.Album.Year
		}
		if strings.TrimSpace(req.Album.Genre) != "" && strings.TrimSpace(req.Tracks[i].Genre) == "" {
			req.Tracks[i].Genre = strings.TrimSpace(req.Album.Genre)
		}
	}

	if req.Album.Genre != "" {
		req.Album.Genre = id3.CanonicalizeGenre(req.Album.Genre)
	}
	for i := range req.Tracks {
		if req.Tracks[i].Genre != "" {
			req.Tracks[i].Genre = id3.CanonicalizeGenre(req.Tracks[i].Genre)
		}
	}
	req.Album.AlbumArtist, req.Tracks = id3.SyncAlbumArtistAndTrackArtists(req.Album.AlbumArtist, req.Tracks)

	res, err := h.organizer.MoveAlbum(req.SourceDir, req.Album.Album, req.Album.AlbumArtist, req.Tracks, req.CoverBase64)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if h.database != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = h.database.ReconcileAllRadarReleases(ctx)
		}()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":         res.Complete(),
		"track_results":   res.TrackResults,
		"destination_dir": res.DestinationDir,
		"tracks":          res.Tracks,
	})
}
