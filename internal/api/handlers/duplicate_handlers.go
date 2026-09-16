package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"music-manager/internal/id3"
)

type duplicateTrackItem struct {
	Path       string `json:"path"`
	Filename   string `json:"filename"`
	SizeBytes  *int64 `json:"size_bytes,omitempty"`
	Duration   *int   `json:"duration,omitempty"`
	ModifiedAt string `json:"modified_at"`
}

type duplicateGroupItem struct {
	Key      string               `json:"key"`
	Title    string               `json:"title"`
	Artist   string               `json:"artist"`
	AlbumDir string               `json:"album_dir"`
	Album    string               `json:"album"`
	Tracks   []duplicateTrackItem `json:"tracks"`
}

func (h *FileHandler) Duplicates(w http.ResponseWriter, r *http.Request) {
	type trackRow struct {
		Path        string
		Title       string
		Artist      string
		Album       string
		Duration    int
		Size        int64
		TrackNumber int
	}

	rows, err := h.database.Conn().QueryContext(r.Context(), `
		SELECT t.file_path, t.title, COALESCE(ar.name, ''), COALESCE(al.title, ''), t.duration, t.file_size, t.track_number 
		FROM tracks t
		LEFT JOIN artists ar ON t.artist_id = ar.id
		LEFT JOIN albums al ON t.album_id = al.id
		ORDER BY t.file_path
	`)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"groups":                []duplicateGroupItem{},
			"total_duplicate_files": 0,
		})
		return
	}
	defer rows.Close()

	groupsMap := make(map[string][]trackRow)
	for rows.Next() {
		var tr trackRow
		var dur, sz, trk sqlInt
		var tit, art, alb sqlStr
		if err := rows.Scan(&tr.Path, &tit, &art, &alb, &dur, &sz, &trk); err == nil {
			tr.Title = tit.String
			tr.Artist = art.String
			tr.Album = alb.String
			tr.Duration = int(dur.Int64)
			tr.Size = sz.Int64
			tr.TrackNumber = int(trk.Int64)

			albumDir := filepath.Dir(tr.Path)
			cleanTit := strings.ToLower(strings.TrimSpace(id3.CleanTitle(tr.Title, "")))
			key := fmt.Sprintf("%s::%s", albumDir, cleanTit)
			groupsMap[key] = append(groupsMap[key], tr)
		}
	}

	var groups []duplicateGroupItem
	totalDupes := 0

	for key, trList := range groupsMap {
		if len(trList) < 2 {
			continue
		}
		first := trList[0]
		albumDir := filepath.Dir(first.Path)

		var tracks []duplicateTrackItem
		for _, t := range trList {
			sz := t.Size
			d := t.Duration
			tracks = append(tracks, duplicateTrackItem{
				Path:       t.Path,
				Filename:   filepath.Base(t.Path),
				SizeBytes:  &sz,
				Duration:   &d,
				ModifiedAt: "now",
			})
		}
		totalDupes += len(tracks) - 1
		groups = append(groups, duplicateGroupItem{
			Key:      key,
			Title:    first.Title,
			Artist:   first.Artist,
			AlbumDir: albumDir,
			Album:    first.Album,
			Tracks:   tracks,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"groups":                groups,
		"total_duplicate_files": totalDupes,
	})
}

type fixDuplicatesReq struct {
	GroupKey      string   `json:"group_key"`
	AlbumPath     string   `json:"album_path"`
	PathsToDelete []string `json:"paths_to_delete"`
}

func (h *FileHandler) FixDuplicates(w http.ResponseWriter, r *http.Request) {
	var req fixDuplicatesReq
	_ = json.NewDecoder(r.Body).Decode(&req)

	var deleted []string
	var errs []string

	for _, p := range req.PathsToDelete {
		fullPath, err := h.musicSandbox.Resolve(p, false)
		if err != nil {
			errs = append(errs, fmt.Sprintf("invalid path %s: %v", p, err))
			continue
		}
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("failed to remove %s: %v", p, err))
		} else {
			deleted = append(deleted, p)
			// Remove companion .lrc/.txt
			baseNoExt := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))
			_ = os.Remove(baseNoExt + ".lrc")
			_ = os.Remove(baseNoExt + ".txt")
			_ = h.database.DeleteTrack(r.Context(), p)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":       len(errs) == 0,
		"deleted_count": len(deleted),
		"deleted_files": deleted,
		"errors":        errs,
	})
}

func (h *FileHandler) FixAllDuplicates(w http.ResponseWriter, r *http.Request) {
	type trackRow struct {
		Path        string
		Title       string
		Artist      string
		Album       string
		Duration    int
		Size        int64
		TrackNumber int
		Bitrate     int
	}

	rows, err := h.database.Conn().QueryContext(r.Context(), `
		SELECT t.file_path, t.title, COALESCE(ar.name, ''), COALESCE(al.title, ''), t.duration, t.file_size, t.track_number, COALESCE(t.bitrate, 0)
		FROM tracks t
		LEFT JOIN artists ar ON t.artist_id = ar.id
		LEFT JOIN albums al ON t.album_id = al.id
		ORDER BY t.file_path
	`)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":       false,
			"deleted_count": 0,
			"deleted_files": []string{},
			"errors":        []string{err.Error()},
		})
		return
	}
	defer rows.Close()

	groupsMap := make(map[string][]trackRow)
	for rows.Next() {
		var tr trackRow
		var dur, sz, trk, br sqlInt
		var tit, art, alb sqlStr
		if err := rows.Scan(&tr.Path, &tit, &art, &alb, &dur, &sz, &trk, &br); err == nil {
			tr.Title = tit.String
			tr.Artist = art.String
			tr.Album = alb.String
			tr.Duration = int(dur.Int64)
			tr.Size = sz.Int64
			tr.TrackNumber = int(trk.Int64)
			tr.Bitrate = int(br.Int64)

			albumDir := filepath.Dir(tr.Path)
			cleanTit := strings.ToLower(strings.TrimSpace(id3.CleanTitle(tr.Title, "")))
			key := fmt.Sprintf("%s::%s", albumDir, cleanTit)
			groupsMap[key] = append(groupsMap[key], tr)
		}
	}

	var deleted []string
	var errs []string

	for _, trList := range groupsMap {
		if len(trList) < 2 {
			continue
		}

		// Sort to pick canonical track at index 0
		sort.SliceStable(trList, func(i, j int) bool {
			aName := strings.ToLower(filepath.Base(trList[i].Path))
			bName := strings.ToLower(filepath.Base(trList[j].Path))

			aIsDup := strings.Contains(aName, " (1)") || strings.Contains(aName, " (2)") ||
				strings.Contains(aName, "_dup") || strings.Contains(aName, " copy") || strings.Contains(aName, " - копия")
			bIsDup := strings.Contains(bName, " (1)") || strings.Contains(bName, " (2)") ||
				strings.Contains(bName, "_dup") || strings.Contains(bName, " copy") || strings.Contains(bName, " - копия")

			// Non-duplicate naming is preferred
			if aIsDup != bIsDup {
				return !aIsDup
			}

			// Higher bitrate preferred
			if trList[i].Bitrate != trList[j].Bitrate {
				return trList[i].Bitrate > trList[j].Bitrate
			}

			// Larger size preferred
			if trList[i].Size != trList[j].Size {
				return trList[i].Size > trList[j].Size
			}

			// Shorter filename preferred
			return len(aName) < len(bName)
		})

		// Keep index 0, delete indices 1..len-1
		for i := 1; i < len(trList); i++ {
			p := trList[i].Path
			fullPath, err := h.musicSandbox.Resolve(p, false)
			if err != nil {
				errs = append(errs, fmt.Sprintf("invalid path %s: %v", p, err))
				continue
			}

			if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Sprintf("failed to remove %s: %v", p, err))
			} else {
				deleted = append(deleted, p)
				baseNoExt := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))
				_ = os.Remove(baseNoExt + ".lrc")
				_ = os.Remove(baseNoExt + ".txt")
				_ = h.database.DeleteTrack(r.Context(), p)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":       len(errs) == 0,
		"deleted_count": len(deleted),
		"deleted_files": deleted,
		"errors":        errs,
	})
}
