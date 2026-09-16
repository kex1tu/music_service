package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
)

type TagHandler struct {
	musicSandbox *sandbox.PathSandbox
	tmpSandbox   *sandbox.PathSandbox
	organizer    *organizer.Service
	deezer       *resolver.DeezerClient
}

func NewTagHandler(
	musicSandbox *sandbox.PathSandbox,
	tmpSandbox *sandbox.PathSandbox,
	organizer *organizer.Service,
	deezer *resolver.DeezerClient,
) *TagHandler {
	return &TagHandler{
		musicSandbox: musicSandbox,
		tmpSandbox:   tmpSandbox,
		organizer:    organizer,
		deezer:       deezer,
	}
}

func (h *TagHandler) resolvePath(relPath string) (string, error) {
	cleanRel := strings.TrimPrefix(relPath, "tmp_downloads/")
	cleanRel = strings.TrimPrefix(cleanRel, "tmp/")

	// Try tmp sandbox first
	if h.tmpSandbox != nil {
		if full, err := h.tmpSandbox.Resolve(cleanRel, false); err == nil && fileExists(full) {
			return full, nil
		}
	}

	// Try music sandbox
	return h.musicSandbox.Resolve(relPath, false)
}

func (h *TagHandler) GetTags(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, `{"error":"missing path query param"}`, http.StatusBadRequest)
		return
	}

	fullPath, err := h.resolvePath(relPath)
	if err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}

	tags, err := id3.ReadTags(fullPath, relPath)
	if err != nil {
		http.Error(w, `{"error":"failed to read tags"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tags)
}

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

	// Apply album fields to tracks if missing
	for i := range req.Tracks {
		if req.Tracks[i].Album == "" {
			req.Tracks[i].Album = req.Album.Album
		}
		if req.Tracks[i].AlbumArtist == "" {
			req.Tracks[i].AlbumArtist = req.Album.AlbumArtist
		}
		if req.Album.Year != nil && req.Tracks[i].Year == 0 {
			req.Tracks[i].Year = *req.Album.Year
		}
		if req.Album.Genre != "" && req.Tracks[i].Genre == "" {
			req.Tracks[i].Genre = req.Album.Genre
		}
	}

	res, err := h.organizer.MoveAlbum(req.SourceDir, req.Album.Album, req.Album.AlbumArtist, req.Tracks, req.CoverBase64)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":         true,
		"destination_dir": res.DestinationDir,
		"tracks":          res.Tracks,
	})
}

func (h *TagHandler) GetCover(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}

	fullPath, err := h.resolvePath(relPath)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	// 1. Try reading embedded APIC from MP3
	if strings.EqualFold(filepath.Ext(fullPath), ".mp3") {
		cover, err := id3.ReadCover(fullPath)
		if err == nil && cover != nil && len(cover.Data) > 0 {
			w.Header().Set("Content-Type", cover.MimeType)
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = w.Write(cover.Data)
			return
		}
	}

	// 2. Check for cover.jpg / folder.jpg in directory
	dir := fullPath
	if fi, err := os.Stat(fullPath); err == nil && !fi.IsDir() {
		dir = filepath.Dir(fullPath)
	}

	for _, cand := range []string{"cover.jpg", "cover.png", "folder.jpg", "folder.png"} {
		imgPath := filepath.Join(dir, cand)
		if data, err := os.ReadFile(imgPath); err == nil {
			mime := "image/jpeg"
			if strings.HasSuffix(cand, ".png") {
				mime = "image/png"
			}
			w.Header().Set("Content-Type", mime)
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = w.Write(data)
			return
		}
	}

	http.Error(w, "cover art not found", http.StatusNotFound)
}

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

type coverSearchResultItem struct {
	Source   string `json:"source"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	CoverURL string `json:"cover_url"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Year     *int   `json:"year,omitempty"`
}

func (h *TagHandler) SearchCovers(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("query")
	if query == "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]coverSearchResultItem{})
		return
	}

	limit := 12
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}

	albums, err := h.deezer.SearchAlbums(r.Context(), query)
	if err != nil {
		http.Error(w, `{"error":"failed to search covers"}`, http.StatusInternalServerError)
		return
	}

	var items []coverSearchResultItem
	for _, a := range albums {
		cover := a.BestCoverURL()
		if cover == "" {
			continue
		}
		year := a.ReleaseYear()
		var pYear *int
		if year > 0 {
			pYear = &year
		}
		items = append(items, coverSearchResultItem{
			Source:   "deezer",
			Title:    a.Title,
			Artist:   a.Artist.Name,
			CoverURL: cover,
			Width:    1000,
			Height:   1000,
			Year:     pYear,
		})
		if len(items) >= limit {
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
