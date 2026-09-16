package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/id3"
)

func (h *TagHandler) SearchDeezerAlbums(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("query")
	if q == "" {
		http.Error(w, `{"error":"missing query parameter"}`, http.StatusBadRequest)
		return
	}

	albums, err := h.deezer.SearchAlbums(r.Context(), q)
	if err != nil {
		http.Error(w, `{"error":"deezer search failed"}`, http.StatusInternalServerError)
		return
	}

	type searchItem struct {
		ID       string  `json:"id"`
		Title    string  `json:"title"`
		Artist   string  `json:"artist"`
		CoverURL *string `json:"cover_url"`
		NbTracks int     `json:"nb_tracks"`
		Year     *int    `json:"year"`
	}

	items := make([]searchItem, len(albums))
	for i, a := range albums {
		cover := a.BestCoverURL()
		year := a.ReleaseYear()
		var pCover *string
		if cover != "" {
			pCover = &cover
		}
		var pYear *int
		if year > 0 {
			pYear = &year
		}

		items[i] = searchItem{
			ID:       strconv.FormatInt(a.ID, 10),
			Title:    a.Title,
			Artist:   a.Artist.Name,
			CoverURL: pCover,
			NbTracks: a.NbTracks,
			Year:     pYear,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (h *TagHandler) GetDeezerAlbumDetails(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		http.Error(w, `{"error":"missing id parameter"}`, http.StatusBadRequest)
		return
	}

	album, err := h.deezer.GetAlbum(r.Context(), id)
	if err != nil {
		http.Error(w, `{"error":"deezer request failed"}`, http.StatusInternalServerError)
		return
	}

	cover := album.BestCoverURL()
	year := album.ReleaseYear()
	genre := album.PrimaryGenre()

	type trackItem struct {
		TrackNumber int    `json:"track_number"`
		Title       string `json:"title"`
		Artist      string `json:"artist"`
		Duration    int    `json:"duration"`
	}

	var tracks []trackItem
	if album.Tracks != nil {
		for i, t := range album.Tracks.Data {
			pos := t.TrackPosition
			if pos == 0 {
				pos = i + 1
			}
			art := t.Artist.Name
			if art == "" {
				art = album.Artist.Name
			}
			tracks = append(tracks, trackItem{
				TrackNumber: pos,
				Title:       id3.CleanTitle(t.Title, ""),
				Artist:      art,
				Duration:    t.Duration,
			})
		}
	}

	resp := map[string]any{
		"id":        id,
		"title":     album.Title,
		"artist":    album.Artist.Name,
		"year":      year,
		"genre":     genre,
		"cover_url": cover,
		"tracks":    tracks,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
