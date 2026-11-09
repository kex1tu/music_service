package handlers

import (
	"encoding/json"
	"net/http"

	"path/filepath"
	"sort"
	"strings"

	"music-manager/internal/core/safefs"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

func (h *FileHandler) BackfillEditorData(w http.ResponseWriter, r *http.Request) {
	albumPath := r.URL.Query().Get("path")
	jobID := r.URL.Query().Get("job_id")

	var localTracks []id3.TrackTags
	var backfillTracks []id3.TrackTags

	// 1. Read existing library tracks
	if albumPath != "" {
		if fullPath, err := h.musicSandbox.Resolve(albumPath, true); err == nil {
			entries, _ := safefs.ReadDir(fullPath)
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp3") {
					itemRel := filepath.Join(albumPath, e.Name())
					itemFull := filepath.Join(fullPath, e.Name())
					if t, err := id3.ReadTags(itemFull, itemRel); err == nil {
						localTracks = append(localTracks, *t)
					}
				}
			}
		}
	}

	// Determine canonical artist and album from existing library files or database
	canonicalArtist := ""
	canonicalAlbum := ""
	canonicalGenre := ""
	canonicalYear := 0

	for _, t := range localTracks {
		if canonicalArtist == "" {
			if t.AlbumArtist != "" {
				canonicalArtist = t.AlbumArtist
			} else if t.Artist != "" {
				canonicalArtist = t.Artist
			}
		}
		if canonicalAlbum == "" && t.Album != "" {
			canonicalAlbum = t.Album
		}
		if canonicalGenre == "" && t.Genre != "" {
			canonicalGenre = t.Genre
		}
		if canonicalYear == 0 && t.Year > 0 {
			canonicalYear = t.Year
		}
	}

	if canonicalArtist == "" || canonicalAlbum == "" {
		if albumPath != "" {
			var dbArtist, dbAlbum string
			var dbYear int
			_ = h.database.Conn().QueryRowContext(r.Context(), `
				SELECT ar.name, al.title, COALESCE(al.year, 0)
				FROM albums al
				JOIN artists ar ON al.artist_id = ar.id
				WHERE al.folder_path = ? OR al.folder_path = ? OR al.title = ?
				LIMIT 1
			`, albumPath, filepath.Clean(albumPath), filepath.Base(albumPath)).Scan(&dbArtist, &dbAlbum, &dbYear)

			if canonicalArtist == "" && dbArtist != "" {
				canonicalArtist = dbArtist
			}
			if canonicalAlbum == "" && dbAlbum != "" {
				canonicalAlbum = dbAlbum
			}
			if canonicalYear == 0 && dbYear > 0 {
				canonicalYear = dbYear
			}
		}
	}

	if canonicalAlbum == "" && albumPath != "" {
		canonicalAlbum = filepath.Base(albumPath)
	}
	if canonicalArtist == "" && albumPath != "" {
		if parts := strings.Split(canonicalAlbum, " - "); len(parts) >= 2 {
			canonicalAlbum = strings.TrimSpace(parts[0])
			canonicalArtist = strings.TrimSpace(parts[1])
		} else if parent := filepath.Base(filepath.Dir(albumPath)); parent != "" && parent != "." && parent != "/" {
			canonicalArtist = parent
		}
	}

	// 2. Read backfilled tracks from job tmp directory if jobID provided
	if jobID != "" && h.tmpSandbox != nil {
		jobDirRel := filepath.Join(jobID, "converted")
		if fullJobDir, err := h.tmpSandbox.Resolve(jobDirRel, false); err == nil {
			entries, _ := safefs.ReadDir(fullJobDir)
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp3") {
					itemRel := filepath.Join("tmp_downloads", jobID, "converted", e.Name())
					itemFull := filepath.Join(fullJobDir, e.Name())
					if t, err := id3.ReadTags(itemFull, itemRel); err == nil {
						// Normalize backfilled track artist & album to match canonical local release
						if canonicalArtist != "" {
							if t.Artist == "" || t.AlbumArtist == "" || resolver.AreArtistsEquivalent(t.Artist, canonicalArtist) || resolver.AreArtistsEquivalent(t.AlbumArtist, canonicalArtist) {
								t.Artist = canonicalArtist
								t.AlbumArtist = canonicalArtist
							}
						}
						if canonicalAlbum != "" {
							if t.Album == "" || resolver.CleanQueryText(t.Album) == resolver.CleanQueryText(canonicalAlbum) {
								t.Album = canonicalAlbum
							}
						}
						if canonicalGenre != "" && t.Genre == "" {
							t.Genre = canonicalGenre
						}
						if canonicalYear > 0 && t.Year == 0 {
							t.Year = canonicalYear
						}
						backfillTracks = append(backfillTracks, *t)
					}
				}
			}
		}
	}

	tracks := append(localTracks, backfillTracks...)

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

	albumArtist := canonicalArtist
	albumTitle := canonicalAlbum
	albumGenre := canonicalGenre
	albumYear := canonicalYear

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
				if _, err := safefs.Stat(filepath.Join(fullPath, c)); err == nil {
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
			if _, err := safefs.Stat(filepath.Join(fullJobDir, "cover.jpg")); err == nil {
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
