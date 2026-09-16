package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"music-manager/internal/id3"
)

func (h *FileHandler) BackfillEditorData(w http.ResponseWriter, r *http.Request) {
	albumPath := r.URL.Query().Get("path")
	jobID := r.URL.Query().Get("job_id")

	var tracks []id3.TrackTags

	// 1. Read existing library tracks
	if albumPath != "" {
		if fullPath, err := h.musicSandbox.Resolve(albumPath, true); err == nil {
			entries, _ := os.ReadDir(fullPath)
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp3") {
					itemRel := filepath.Join(albumPath, e.Name())
					itemFull := filepath.Join(fullPath, e.Name())
					if t, err := id3.ReadTags(itemFull, itemRel); err == nil {
						tracks = append(tracks, *t)
					}
				}
			}
		}
	}

	// 2. Read backfilled tracks from job tmp directory if jobID provided
	if jobID != "" && h.tmpSandbox != nil {
		jobDirRel := filepath.Join(jobID, "converted")
		if fullJobDir, err := h.tmpSandbox.Resolve(jobDirRel, false); err == nil {
			entries, _ := os.ReadDir(fullJobDir)
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp3") {
					itemRel := filepath.Join("tmp_downloads", jobID, "converted", e.Name())
					itemFull := filepath.Join(fullJobDir, e.Name())
					if t, err := id3.ReadTags(itemFull, itemRel); err == nil {
						tracks = append(tracks, *t)
					}
				}
			}
		}
	}

	// Deterministic sort by DiscNumber ASC, TrackNumber ASC, Title ASC
	sort.SliceStable(tracks, func(i, j int) bool {
		if tracks[i].DiscNumber != tracks[j].DiscNumber {
			return tracks[i].DiscNumber < tracks[j].DiscNumber
		}
		if tracks[i].TrackNumber != tracks[j].TrackNumber {
			return tracks[i].TrackNumber < tracks[j].TrackNumber
		}
		return tracks[i].Title < tracks[j].Title
	})

	albumArtist := ""
	albumTitle := filepath.Base(albumPath)
	albumGenre := ""
	albumYear := 0

	for _, t := range tracks {
		if t.AlbumArtist != "" && albumArtist == "" {
			albumArtist = t.AlbumArtist
		}
		if t.Artist != "" && albumArtist == "" {
			albumArtist = t.Artist
		}
		if t.Album != "" && (albumTitle == "" || albumTitle == filepath.Base(albumPath)) {
			albumTitle = t.Album
		}
		if t.Genre != "" && albumGenre == "" {
			albumGenre = t.Genre
		}
		if t.Year > 0 && albumYear == 0 {
			albumYear = t.Year
		}
	}

	// Cover detection (check existing album folder, then tmp folder)
	coverPath := ""
	hasCover := false
	if albumPath != "" {
		if fullPath, err := h.musicSandbox.Resolve(albumPath, true); err == nil {
			for _, c := range []string{"cover.jpg", "cover.png", "folder.jpg"} {
				if _, err := os.Stat(filepath.Join(fullPath, c)); err == nil {
					coverPath = filepath.Join(albumPath, c)
					hasCover = true
					break
				}
			}
		}
	}
	if !hasCover && jobID != "" && h.tmpSandbox != nil {
		jobDirRel := filepath.Join(jobID, "converted")
		if fullJobDir, err := h.tmpSandbox.Resolve(jobDirRel, false); err == nil {
			if _, err := os.Stat(filepath.Join(fullJobDir, "cover.jpg")); err == nil {
				coverPath = filepath.Join("tmp_downloads", jobID, "converted", "cover.jpg")
				hasCover = true
			}
		}
	}

	var jobIDs []string
	if jobID != "" {
		jobIDs = append(jobIDs, jobID)
	}

	resp := map[string]any{
		"album": map[string]any{
			"album_artist": albumArtist,
			"album":        albumTitle,
			"genre":        albumGenre,
			"year":         albumYear,
			"track_total":  len(tracks),
		},
		"tracks":           tracks,
		"source_dir":       albumPath,
		"cover_path":       coverPath,
		"has_cover":        hasCover,
		"job_ids":          jobIDs,
		"missing_count":    0,
		"backfilled_count": len(tracks),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
