package handlers

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"music-manager/internal/config"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
)

type TagHandler struct {
	musicSandbox *sandbox.PathSandbox
	tmpSandbox   *sandbox.PathSandbox
	organizer    *organizer.Service
	deezer       *resolver.DeezerClient
	enricher     *resolver.FeatEnricher
	musicbrainz  *resolver.MusicBrainzClient
	shazam       *resolver.ShazamProvider
	cfg          *config.Config
	database     *db.DB
}

func (h *TagHandler) SetDB(database *db.DB) {
	h.database = database
}

func NewTagHandler(
	musicSandbox *sandbox.PathSandbox,
	tmpSandbox *sandbox.PathSandbox,
	organizer *organizer.Service,
	deezer *resolver.DeezerClient,
	enricher ...*resolver.FeatEnricher,
) *TagHandler {
	var e *resolver.FeatEnricher
	if len(enricher) > 0 {
		e = enricher[0]
	}
	return &TagHandler{
		musicSandbox: musicSandbox,
		tmpSandbox:   tmpSandbox,
		organizer:    organizer,
		deezer:       deezer,
		enricher:     e,
		musicbrainz:  resolver.NewMusicBrainzClient(""),
		shazam:       resolver.NewShazamProvider(nil),
	}
}

func (h *TagHandler) SetConfig(cfg *config.Config) {
	h.cfg = cfg
}

func (h *TagHandler) SetMusicBrainz(mb *resolver.MusicBrainzClient) {
	h.musicbrainz = mb
}

func (h *TagHandler) SetShazam(sh *resolver.ShazamProvider) {
	h.shazam = sh
}

func (h *TagHandler) SetEnricher(e *resolver.FeatEnricher) {
	h.enricher = e
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
