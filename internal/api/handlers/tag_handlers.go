package handlers

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

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
	full, _, err := h.resolvePathWithTmp(relPath)
	return full, err
}

func (h *TagHandler) resolvePathWithTmp(relPath string) (string, bool, error) {
	cleanRel := strings.TrimPrefix(relPath, "tmp_downloads/")
	cleanRel = strings.TrimPrefix(cleanRel, "tmp/")

	// Try tmp sandbox first
	if h.tmpSandbox != nil {
		if full, err := h.tmpSandbox.Resolve(cleanRel, true); err == nil && fileExists(full) {
			return full, true, nil
		}
	}

	// Try music sandbox
	full, err := h.musicSandbox.Resolve(relPath, true)
	return full, false, err
}

func (h *TagHandler) GetTags(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, `{"error":"missing path query param"}`, http.StatusBadRequest)
		return
	}

	fullPath, isTmp, err := h.resolvePathWithTmp(relPath)
	if err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}

	st, err := os.Stat(fullPath)
	if err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}

	if st.IsDir() {
		var tracks []id3.TrackTags
		var coverPath *string
		hasCover := false

		// Check for cover image file in the directory
		for _, cand := range []string{"cover.jpg", "cover.png", "folder.jpg", "folder.png", "cover.jpeg"} {
			imgPath := filepath.Join(fullPath, cand)
			if _, err := os.Stat(imgPath); err == nil {
				cRel := filepath.Join(relPath, cand)
				coverPath = &cRel
				hasCover = true
				break
			}
		}

		err := filepath.WalkDir(fullPath, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d == nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if strings.HasPrefix(name, ".") || name == "tmp" || name == "tmp_downloads" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.EqualFold(filepath.Ext(path), ".mp3") {
				return nil
			}

			var fileRel string
			if isTmp {
				if r, err := filepath.Rel(h.tmpSandbox.Root(), path); err == nil {
					fileRel = "tmp_downloads/" + filepath.ToSlash(r)
				}
			} else {
				fileRel, _ = h.musicSandbox.ToRelative(path)
			}
			if fileRel == "" {
				fileRel = path
			}

			t, err := id3.ReadTags(path, fileRel)
			if err == nil && t != nil {
				if t.HasCover {
					hasCover = true
				}
				tracks = append(tracks, *t)
			}
			return nil
		})
		if err != nil {
			http.Error(w, `{"error":"failed to read directory"}`, http.StatusInternalServerError)
			return
		}

		// Sort tracks by DiscNumber, TrackNumber, Filename
		sort.SliceStable(tracks, func(i, j int) bool {
			if tracks[i].DiscNumber != tracks[j].DiscNumber {
				return tracks[i].DiscNumber < tracks[j].DiscNumber
			}
			if tracks[i].TrackNumber != tracks[j].TrackNumber {
				return tracks[i].TrackNumber < tracks[j].TrackNumber
			}
			return tracks[i].Path < tracks[j].Path
		})

		albumArtist := ""
		albumTitle := filepath.Base(relPath)
		if albumTitle == "." || albumTitle == "/" {
			albumTitle = ""
		}
		var year *int
		genre := ""
		discTotal := 1
		trackTotal := len(tracks)

		for _, t := range tracks {
			if albumTitle == "" || albumTitle == filepath.Base(relPath) {
				if t.Album != "" {
					albumTitle = t.Album
				}
			}
			if albumArtist == "" {
				if t.AlbumArtist != "" {
					albumArtist = t.AlbumArtist
				} else if t.Artist != "" {
					albumArtist = t.Artist
				}
			}
			if year == nil && t.Year > 0 {
				y := t.Year
				year = &y
			}
			if genre == "" && t.Genre != "" {
				genre = t.Genre
			}
			if t.DiscNumber > discTotal {
				discTotal = t.DiscNumber
			}
			if t.DiscTotal > discTotal {
				discTotal = t.DiscTotal
			}
			if t.TrackTotal > trackTotal {
				trackTotal = t.TrackTotal
			}
		}

		resp := map[string]any{
			"album": map[string]any{
				"album_artist": albumArtist,
				"album":        albumTitle,
				"year":         year,
				"genre":        genre,
				"disc_total":   discTotal,
				"track_total":  trackTotal,
			},
			"tracks":     tracks,
			"source_dir": relPath,
			"cover_path": coverPath,
			"has_cover":  hasCover,
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	// Single file mode
	tags, err := id3.ReadTags(fullPath, relPath)
	if err != nil {
		http.Error(w, `{"error":"failed to read tags"}`, http.StatusInternalServerError)
		return
	}

	var year *int
	if tags.Year > 0 {
		y := tags.Year
		year = &y
	}

	var coverPath *string
	trackBase := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))
	trackRelBase := strings.TrimSuffix(relPath, filepath.Ext(relPath))
	for _, candExt := range []string{".jpg", ".png", ".jpeg", ".webp"} {
		if _, err := os.Stat(trackBase + candExt); err == nil {
			cRel := trackRelBase + candExt
			coverPath = &cRel
			break
		}
	}
	if coverPath == nil {
		dir := filepath.Dir(fullPath)
		for _, cand := range []string{"cover.jpg", "cover.png", "folder.jpg", "folder.png", "cover.jpeg"} {
			if _, err := os.Stat(filepath.Join(dir, cand)); err == nil {
				cRel := filepath.Join(filepath.Dir(relPath), cand)
				coverPath = &cRel
				break
			}
		}
	}
	hasCover := tags.HasCover || coverPath != nil

	albumTitle := tags.Album
	if albumTitle == "" {
		albumTitle = tags.Title
	}
	albumArtist := tags.AlbumArtist
	if albumArtist == "" {
		albumArtist = tags.Artist
	}

	resp := map[string]any{
		"album": map[string]any{
			"album_artist": albumArtist,
			"album":        albumTitle,
			"year":         year,
			"genre":        tags.Genre,
			"disc_total":   tags.DiscTotal,
			"track_total":  tags.TrackTotal,
		},
		"tracks":     []id3.TrackTags{*tags},
		"source_dir": filepath.Dir(relPath),
		"cover_path": coverPath,
		"has_cover":  hasCover,

		// Top-level fields for single-track backwards compatibility
		"title":         tags.Title,
		"artist":        tags.Artist,
		"album_artist":  tags.AlbumArtist,
		"path":          tags.Path,
		"track_number":  tags.TrackNumber,
		"track_total":   tags.TrackTotal,
		"disc_number":   tags.DiscNumber,
		"disc_total":    tags.DiscTotal,
		"year":          tags.Year,
		"genre":         tags.Genre,
		"duration":      tags.Duration,
		"bitrate":       tags.Bitrate,
		"lyrics_status": tags.LyricsStatus,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
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

	st, err := os.Stat(fullPath)
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

	// 2. Check for track-specific cover or cover.jpg / folder.jpg in directory
	var candidates []string
	if !st.IsDir() {
		trackBase := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))
		candidates = append(candidates,
			trackBase+".jpg", trackBase+".png", trackBase+".jpeg", trackBase+".webp",
		)
	}
	dir := fullPath
	if !st.IsDir() {
		dir = filepath.Dir(fullPath)
	}
	for _, cand := range []string{"cover.jpg", "cover.png", "folder.jpg", "folder.png", "cover.jpeg"} {
		candidates = append(candidates, filepath.Join(dir, cand))
	}

	for _, candPath := range candidates {
		if data, err := os.ReadFile(candPath); err == nil {
			mime := "image/jpeg"
			if strings.HasSuffix(candPath, ".png") {
				mime = "image/png"
			} else if strings.HasSuffix(candPath, ".webp") {
				mime = "image/webp"
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

type songrecResult struct {
	Matches []any `json:"matches"`
	Track   *struct {
		Title    string `json:"title"`
		Subtitle string `json:"subtitle"`
		Genres   struct {
			Primary string `json:"primary"`
		} `json:"genres"`
		Images struct {
			CoverArt   string `json:"coverart"`
			CoverArtHQ string `json:"coverarthq"`
		} `json:"images"`
		Sections []struct {
			Type     string `json:"type"`
			Metadata []struct {
				Title string `json:"title"`
				Text  string `json:"text"`
			} `json:"metadata"`
		} `json:"sections"`
	} `json:"track"`
}

type recognizeAudioRequest struct {
	Path string `json:"path"`
}

type recognizeAudioResponse struct {
	Recognized bool   `json:"recognized"`
	Title      string `json:"title,omitempty"`
	Artist     string `json:"artist,omitempty"`
	Album      string `json:"album,omitempty"`
	Year       int    `json:"year,omitempty"`
	Genre      string `json:"genre,omitempty"`
	CoverURL   string `json:"cover_url,omitempty"`
}

// RecognizeAudio performs acoustic music recognition via songrec (Shazam CLI) with safe fallback.
func (h *TagHandler) RecognizeAudio(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req recognizeAudioRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		_ = json.NewEncoder(w).Encode(recognizeAudioResponse{Recognized: false})
		return
	}

	fullPath, err := h.resolvePath(req.Path)
	if err != nil || !fileExists(fullPath) {
		_ = json.NewEncoder(w).Encode(recognizeAudioResponse{Recognized: false})
		return
	}

	songrecBin, err := exec.LookPath("songrec")
	if err != nil {
		_ = json.NewEncoder(w).Encode(recognizeAudioResponse{Recognized: false})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, songrecBin, "audio-file-to-recognized-song", fullPath)
	out, err := cmd.Output()
	if err != nil {
		_ = json.NewEncoder(w).Encode(recognizeAudioResponse{Recognized: false})
		return
	}

	var res songrecResult
	if err := json.Unmarshal(out, &res); err != nil || res.Track == nil {
		_ = json.NewEncoder(w).Encode(recognizeAudioResponse{Recognized: false})
		return
	}

	title := res.Track.Title
	artist := res.Track.Subtitle
	genre := res.Track.Genres.Primary
	coverURL := res.Track.Images.CoverArtHQ
	if coverURL == "" {
		coverURL = res.Track.Images.CoverArt
	}
	if coverURL != "" {
		coverURL = strings.ReplaceAll(coverURL, "/400x400cc.jpg", "/1000x1000cc.jpg")
		coverURL = strings.ReplaceAll(coverURL, "/800x800cc.jpg", "/1000x1000cc.jpg")
	}

	album := ""
	year := 0
	for _, sec := range res.Track.Sections {
		for _, m := range sec.Metadata {
			switch strings.ToLower(m.Title) {
			case "album":
				album = m.Text
			case "released":
				if y, yErr := strconv.Atoi(strings.TrimSpace(m.Text)); yErr == nil {
					year = y
				}
			}
		}
	}

	_ = json.NewEncoder(w).Encode(recognizeAudioResponse{
		Recognized: true,
		Title:      title,
		Artist:     artist,
		Album:      album,
		Year:       year,
		Genre:      genre,
		CoverURL:   coverURL,
	})
}
