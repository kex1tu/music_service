package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/id3"
	"music-manager/internal/services/downloader"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
)

type lyricsBatchState struct {
	sync.Mutex
	isRunning    bool
	total        int
	current      int
	found        int
	failed       int
	currentTrack string
	cancel       context.CancelFunc
}

type FileHandler struct {
	musicSandbox *sandbox.PathSandbox
	tmpSandbox   *sandbox.PathSandbox
	database     *db.DB
	downloader   *downloader.Service
	organizer    *organizer.Service
	deezer       *resolver.DeezerClient
	lyrics       *lyrics.Service
	lyricsState  lyricsBatchState
}

func NewFileHandler(
	musicSandbox *sandbox.PathSandbox,
	tmpSandbox *sandbox.PathSandbox,
	database *db.DB,
	downloader *downloader.Service,
	organizer *organizer.Service,
	deezer *resolver.DeezerClient,
	lyrics *lyrics.Service,
) *FileHandler {
	return &FileHandler{
		musicSandbox: musicSandbox,
		tmpSandbox:   tmpSandbox,
		database:     database,
		downloader:   downloader,
		organizer:    organizer,
		deezer:       deezer,
		lyrics:       lyrics,
	}
}

type fileItem struct {
	Name         string  `json:"name"`
	Path         string  `json:"path"`
	Type         string  `json:"type"` // "directory" or "file"
	Size         *int64  `json:"size,omitempty"`
	Extension    *string `json:"extension,omitempty"`
	TrackCount   *int    `json:"track_count,omitempty"`
	TrackNumber  *int    `json:"track_number,omitempty"`
	Title        *string `json:"title,omitempty"`
	Artist       *string `json:"artist,omitempty"`
	Album        *string `json:"album,omitempty"`
	Duration     *int    `json:"duration,omitempty"`
	HasCover     bool    `json:"has_cover"`
	LyricsStatus string  `json:"lyrics_status"` // "none", "lrc", "txt"
}

type breadcrumbItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type directoryListingResponse struct {
	CurrentPath      string           `json:"current_path"`
	ParentPath       *string          `json:"parent_path"`
	Breadcrumbs      []breadcrumbItem `json:"breadcrumbs"`
	Items            []fileItem       `json:"items"`
	TotalDirectories int              `json:"total_directories"`
	TotalFiles       int              `json:"total_files"`
}

func (h *FileHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	fullPath, err := h.musicSandbox.Resolve(relPath, true)
	if err != nil {
		http.Error(w, `{"error":"invalid path"}`, http.StatusBadRequest)
		return
	}

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		http.Error(w, `{"error":"failed to read directory"}`, http.StatusInternalServerError)
		return
	}

	var items []fileItem
	var totalDirs, totalFiles int

	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "tmp" || name == "tmp_downloads" {
			continue
		}

		itemRel := filepath.Join(relPath, name)
		itemFull := filepath.Join(fullPath, name)

		if e.IsDir() {
			totalDirs++
			// Count mp3 files in directory
			subEntries, _ := os.ReadDir(itemFull)
			tCount := 0
			for _, sub := range subEntries {
				if !sub.IsDir() && strings.EqualFold(filepath.Ext(sub.Name()), ".mp3") {
					tCount++
				}
			}

			items = append(items, fileItem{
				Name:         name,
				Path:         itemRel,
				Type:         "directory",
				TrackCount:   &tCount,
				LyricsStatus: "none",
			})
		} else {
			totalFiles++
			info, _ := e.Info()
			var sz int64
			if info != nil {
				sz = info.Size()
			}
			ext := filepath.Ext(name)

			fItem := fileItem{
				Name:         name,
				Path:         itemRel,
				Type:         "file",
				Size:         &sz,
				Extension:    &ext,
				LyricsStatus: "none",
			}

			if strings.EqualFold(ext, ".mp3") {
				if tags, err := id3.ReadTags(itemFull, itemRel); err == nil {
					fItem.TrackNumber = &tags.TrackNumber
					fItem.Title = &tags.Title
					fItem.Artist = &tags.Artist
					fItem.Album = &tags.Album
					dur := int(tags.Duration)
					fItem.Duration = &dur
					fItem.HasCover = tags.HasCover
					fItem.LyricsStatus = tags.LyricsStatus
				}
			}

			items = append(items, fItem)
		}
	}

	// Build breadcrumbs
	var breadcrumbs []breadcrumbItem
	breadcrumbs = append(breadcrumbs, breadcrumbItem{Name: "Корень", Path: ""})
	if relPath != "" {
		parts := strings.Split(filepath.Clean(relPath), string(filepath.Separator))
		accum := ""
		for _, p := range parts {
			if p == "" || p == "." {
				continue
			}
			accum = filepath.Join(accum, p)
			breadcrumbs = append(breadcrumbs, breadcrumbItem{Name: p, Path: accum})
		}
	}

	var parentPath *string
	if relPath != "" {
		parent := filepath.Dir(relPath)
		if parent == "." {
			parent = ""
		}
		parentPath = &parent
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(directoryListingResponse{
		CurrentPath:      relPath,
		ParentPath:       parentPath,
		Breadcrumbs:      breadcrumbs,
		Items:            items,
		TotalDirectories: totalDirs,
		TotalFiles:       totalFiles,
	})
}

func (h *FileHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, `{"error":"missing path parameter"}`, http.StatusBadRequest)
		return
	}

	fullPath, err := h.musicSandbox.Resolve(relPath, false)
	if err != nil {
		http.Error(w, `{"error":"invalid path"}`, http.StatusBadRequest)
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}

	isDir := info.IsDir()
	if isDir {
		if err := os.RemoveAll(fullPath); err != nil {
			http.Error(w, `{"error":"failed to delete directory"}`, http.StatusInternalServerError)
			return
		}
	} else {
		if err := os.Remove(fullPath); err != nil {
			http.Error(w, `{"error":"failed to delete file"}`, http.StatusInternalServerError)
			return
		}
		// Also delete lyrics if exist
		base := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))
		_ = os.Remove(base + ".lrc")
		_ = os.Remove(base + ".txt")

		// Remove from database
		_, _ = h.database.Conn().Exec("DELETE FROM tracks WHERE file_path = ?", relPath)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":      true,
		"deleted_path": relPath,
		"is_directory": isDir,
	})
}

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
	q := r.URL.Query().Get("q")
	if q == "" {
		q = r.URL.Query().Get("query")
	}

	artists, albums, tracks, err := h.database.SearchLibrary(r.Context(), q, 50)
	if err != nil {
		http.Error(w, `{"error":"search failed"}`, http.StatusInternalServerError)
		return
	}

	total := len(artists) + len(albums) + len(tracks)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"query":         q,
		"artists":       artists,
		"albums":        albums,
		"tracks":        tracks,
		"total_matches": total,
	})
}

func (h *FileHandler) AuditSummary(w http.ResponseWriter, r *http.Request) {
	var totalTracks, totalAlbums int
	_ = h.database.Conn().QueryRow("SELECT COUNT(*) FROM tracks").Scan(&totalTracks)
	_ = h.database.Conn().QueryRow("SELECT COUNT(*) FROM albums").Scan(&totalAlbums)

	var missingLyrics, missingCovers int
	_ = h.database.Conn().QueryRow("SELECT COUNT(*) FROM tracks WHERE lyrics_status = 'none'").Scan(&missingLyrics)
	_ = h.database.Conn().QueryRow("SELECT COUNT(*) FROM tracks WHERE has_embedded_cover = 0").Scan(&missingCovers)

	var incompleteAlbums int
	_ = h.database.Conn().QueryRow("SELECT COUNT(*) FROM albums WHERE is_complete = 0").Scan(&incompleteAlbums)

	invalidPaths, _ := h.database.GetInvalidPaths(r.Context())

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total_tracks":            totalTracks,
		"total_albums":            totalAlbums,
		"missing_lyrics_count":    missingLyrics,
		"missing_covers_count":    missingCovers,
		"duplicates_count":        0,
		"incomplete_albums_count": incompleteAlbums,
		"invalid_paths_count":     len(invalidPaths),
		"non_en_genres_count":     0,
	})
}

func (h *FileHandler) InvalidPaths(w http.ResponseWriter, r *http.Request) {
	paths, err := h.database.GetInvalidPaths(r.Context())
	if err != nil {
		http.Error(w, `{"error":"failed to get invalid paths"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(paths)
}

type fixPathRequest struct {
	Path      string `json:"path"`
	Overwrite bool   `json:"overwrite"`
}

func (h *FileHandler) FixPath(w http.ResponseWriter, r *http.Request) {
	var req fixPathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	fullPath, err := h.musicSandbox.Resolve(req.Path, false)
	if err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}

	tags, err := id3.ReadTags(fullPath, req.Path)
	if err != nil {
		http.Error(w, `{"error":"failed to read tags"}`, http.StatusInternalServerError)
		return
	}

	_, targetRel := h.organizer.ComputeTargetPath(tags.Album, tags.TrackNumber, tags.Title, filepath.Base(fullPath))
	targetFull, err := h.musicSandbox.Resolve(targetRel, true)
	if err != nil {
		http.Error(w, `{"error":"failed to resolve target"}`, http.StatusBadRequest)
		return
	}

	if fullPath != targetFull {
		actualDest, err := organizer.MoveTrackWithCompanions(fullPath, targetFull, req.Overwrite)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed to move file: %v"}`, err), http.StatusInternalServerError)
			return
		}
		actualRel, _ := h.musicSandbox.ToRelative(actualDest)
		_ = h.database.UpdateTrackPath(r.Context(), req.Path, actualRel)
		targetRel = actualRel
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":     true,
		"source_path": req.Path,
		"target_path": targetRel,
		"message":     "Путь успешно исправлен на канонический",
	})
}

func (h *FileHandler) FixAllPaths(w http.ResponseWriter, r *http.Request) {
	paths, err := h.database.GetInvalidPaths(r.Context())
	if err != nil {
		http.Error(w, `{"error":"failed to get invalid paths"}`, http.StatusInternalServerError)
		return
	}

	fixed := 0
	var errs []string

	for _, p := range paths {
		fullPath, err := h.musicSandbox.Resolve(p.FilePath, false)
		if err != nil {
			continue
		}
		targetFull, err := h.musicSandbox.Resolve(p.SuggestedPath, true)
		if err != nil {
			continue
		}
		if actualDest, err := organizer.MoveTrackWithCompanions(fullPath, targetFull, false); err == nil {
			actualRel, _ := h.musicSandbox.ToRelative(actualDest)
			_ = h.database.UpdateTrackPath(r.Context(), p.FilePath, actualRel)
			fixed++
		} else {
			errs = append(errs, fmt.Sprintf("%s: %v", p.FilePath, err))
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":       true,
		"total_invalid": len(paths),
		"fixed_count":   fixed,
		"errors":        errs,
	})
}

func (h *FileHandler) MissingLyrics(w http.ResponseWriter, r *http.Request) {
	rows, err := h.database.Conn().Query(`
		SELECT t.file_path, t.title, a.name, al.title
		FROM tracks t
		JOIN artists a ON t.artist_id = a.id
		JOIN albums al ON t.album_id = al.id
		WHERE t.lyrics_status = 'none'
		LIMIT 100;
	`)
	if err != nil {
		http.Error(w, `{"error":"database query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type missingLyricsItem struct {
		Path      string `json:"path"`
		Title     string `json:"title"`
		Artist    string `json:"artist"`
		Album     string `json:"album"`
		Directory string `json:"directory"`
	}

	var items []missingLyricsItem
	for rows.Next() {
		var it missingLyricsItem
		if err := rows.Scan(&it.Path, &it.Title, &it.Artist, &it.Album); err == nil {
			it.Directory = filepath.Dir(it.Path)
			items = append(items, it)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (h *FileHandler) MissingCovers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.database.Conn().Query(`
		SELECT al.folder_path, al.title, a.name, COUNT(t.id)
		FROM albums al
		JOIN artists a ON al.artist_id = a.id
		LEFT JOIN tracks t ON t.album_id = al.id
		WHERE al.has_cover = 0
		GROUP BY al.id
		LIMIT 100;
	`)
	if err != nil {
		http.Error(w, `{"error":"database query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type missingCoverItem struct {
		Path       string `json:"path"`
		Title      string `json:"title"`
		Artist     string `json:"artist"`
		Album      string `json:"album"`
		IsAlbumDir bool   `json:"is_album_dir"`
		TrackCount int    `json:"track_count"`
	}

	var items []missingCoverItem
	for rows.Next() {
		var it missingCoverItem
		if err := rows.Scan(&it.Path, &it.Title, &it.Artist, &it.TrackCount); err == nil {
			it.Album = it.Title
			it.IsAlbumDir = true
			items = append(items, it)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (h *FileHandler) IncompleteAlbums(w http.ResponseWriter, r *http.Request) {
	rows, err := h.database.Conn().Query(`
		SELECT al.folder_path, al.title, a.name, al.year, al.has_cover, al.cover_path,
		       al.present_tracks_count, al.expected_tracks_count, al.deezer_id
		FROM albums al
		JOIN artists a ON al.artist_id = a.id
		WHERE al.is_complete = 0 OR (al.expected_tracks_count > al.present_tracks_count)
		LIMIT 50;
	`)
	if err != nil {
		http.Error(w, `{"error":"database query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type incompleteAlbumItem struct {
		Path             string  `json:"path"`
		Album            string  `json:"album"`
		Artist           string  `json:"artist"`
		Year             *int    `json:"year"`
		HasCover         bool    `json:"has_cover"`
		CoverURL         *string `json:"cover_url"`
		PresentCount     int     `json:"present_count"`
		ExpectedCount    int     `json:"expected_count"`
		IsCatalogMatched bool    `json:"is_catalog_matched"`
		DeezerAlbumID    *string `json:"deezer_album_id"`
	}

	var items []incompleteAlbumItem
	for rows.Next() {
		var it incompleteAlbumItem
		var year sqlInt
		var deezerID sqlStr
		var coverPath sqlStr
		if err := rows.Scan(
			&it.Path, &it.Album, &it.Artist, &year, &it.HasCover, &coverPath,
			&it.PresentCount, &it.ExpectedCount, &deezerID,
		); err == nil {
			if year.Valid {
				y := int(year.Int64)
				it.Year = &y
			}
			if deezerID.Valid && deezerID.String != "" {
				s := deezerID.String
				it.DeezerAlbumID = &s
				it.IsCatalogMatched = true
			}
			items = append(items, it)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"albums":                  items,
		"total_incomplete_albums": len(items),
		"total_missing_tracks":    0,
	})
}

type downloadMissingRequest struct {
	Path          string   `json:"path"`
	DeezerAlbumID *string  `json:"deezer_album_id"`
	TrackURLs     []string `json:"track_urls"`
	TrackNumbers  []int    `json:"track_numbers"`
}

func (h *FileHandler) DownloadMissing(w http.ResponseWriter, r *http.Request) {
	var req downloadMissingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	var createdJobs []*downloader.DownloadJob

	if len(req.TrackURLs) > 0 {
		for _, u := range req.TrackURLs {
			j, err := h.downloader.CreateJob(u, "deezer")
			if err == nil {
				createdJobs = append(createdJobs, j)
			}
		}
	} else if req.DeezerAlbumID != nil && *req.DeezerAlbumID != "" {
		albumURL := fmt.Sprintf("https://www.deezer.com/album/%s", *req.DeezerAlbumID)
		j, err := h.downloader.CreateJob(albumURL, "deezer")
		if err == nil {
			createdJobs = append(createdJobs, j)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(createdJobs)
}

type sqlInt struct {
	Int64 int64
	Valid bool
}

func (s *sqlInt) Scan(value any) error {
	if value == nil {
		s.Int64, s.Valid = 0, false
		return nil
	}
	if v, ok := value.(int64); ok {
		s.Int64, s.Valid = v, true
		return nil
	}
	s.Int64, s.Valid = 0, false
	return nil
}

type sqlStr struct {
	String string
	Valid  bool
}

func (s *sqlStr) Scan(value any) error {
	if value == nil {
		s.String, s.Valid = "", false
		return nil
	}
	if v, ok := value.(string); ok {
		s.String, s.Valid = v, true
		return nil
	}
	if b, ok := value.([]byte); ok {
		s.String, s.Valid = string(b), true
		return nil
	}
	s.String, s.Valid = "", false
	return nil
}

func (h *FileHandler) RefreshAudit(w http.ResponseWriter, r *http.Request) {
	h.AuditSummary(w, r)
}

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
	// Scan tracks in database grouped by release and clean title
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

type nonEnGenreItem struct {
	Path           string `json:"path"`
	CurrentGenre   string `json:"current_genre"`
	CanonicalGenre string `json:"canonical_genre"`
	IsAutoFixed    bool   `json:"is_auto_fixed"`
}

func (h *FileHandler) NonEnGenres(w http.ResponseWriter, r *http.Request) {
	rows, err := h.database.Conn().QueryContext(r.Context(), `
		SELECT t.file_path, g.name, g.canonical_en_name 
		FROM tracks t
		JOIN track_genres tg ON t.id = tg.track_id
		JOIN genres g ON tg.genre_id = g.id
		WHERE g.is_valid_en = 0
		ORDER BY t.file_path
	`)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]nonEnGenreItem{})
		return
	}
	defer rows.Close()

	var items []nonEnGenreItem
	for rows.Next() {
		var path, name string
		var canon sqlStr
		if err := rows.Scan(&path, &name, &canon); err == nil {
			items = append(items, nonEnGenreItem{
				Path:           path,
				CurrentGenre:   name,
				CanonicalGenre: canon.String,
				IsAutoFixed:    false,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (h *FileHandler) RetagGenres(w http.ResponseWriter, r *http.Request) {
	rows, err := h.database.Conn().QueryContext(r.Context(), `
		SELECT t.id, t.file_path, g.id, g.name, COALESCE(g.canonical_en_name, '')
		FROM tracks t
		JOIN track_genres tg ON t.id = tg.track_id
		JOIN genres g ON tg.genre_id = g.id
		WHERE g.is_valid_en = 0
		ORDER BY t.file_path
	`)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":       false,
			"updated_count": 0,
			"total_checked": 0,
			"items":         []nonEnGenreItem{},
			"errors":        []string{err.Error()},
		})
		return
	}
	defer rows.Close()

	type targetItem struct {
		trackID      int64
		filePath     string
		genreID      int64
		currentGenre string
		canonical    string
	}
	var targets []targetItem
	for rows.Next() {
		var it targetItem
		if err := rows.Scan(&it.trackID, &it.filePath, &it.genreID, &it.currentGenre, &it.canonical); err == nil {
			targets = append(targets, it)
		}
	}

	var updatedItems []nonEnGenreItem
	var errs []string
	updatedCount := 0

	for _, t := range targets {
		canon := t.canonical
		if canon == "" {
			normRes := id3.AnalyzeAndNormalizeGenre(t.currentGenre)
			canon = normRes.CanonicalGenre
		}
		if canon == "" {
			canon = t.currentGenre
		}

		fullPath, err := h.musicSandbox.Resolve(t.filePath, false)
		if err != nil {
			errs = append(errs, fmt.Sprintf("invalid path %s: %v", t.filePath, err))
			continue
		}

		currentTags, err := id3.ReadTags(fullPath, t.filePath)
		if err != nil {
			errs = append(errs, fmt.Sprintf("read tags error %s: %v", t.filePath, err))
			continue
		}

		currentTags.Genre = canon
		if err := id3.WriteTags(fullPath, *currentTags, nil); err != nil {
			errs = append(errs, fmt.Sprintf("write tags error %s: %v", t.filePath, err))
			continue
		}

		var canonID int64
		err = h.database.Conn().QueryRow("SELECT id FROM genres WHERE name = ?", canon).Scan(&canonID)
		if err == sql.ErrNoRows {
			res, insErr := h.database.Conn().Exec("INSERT INTO genres (name, is_valid_en, canonical_en_name) VALUES (?, 1, ?)", canon, canon)
			if insErr == nil {
				canonID, _ = res.LastInsertId()
			}
		}
		if canonID > 0 {
			_, _ = h.database.Conn().Exec("UPDATE track_genres SET genre_id = ? WHERE track_id = ? AND genre_id = ?", canonID, t.trackID, t.genreID)
		}
		_, _ = h.database.Conn().Exec("UPDATE audit_issues SET is_resolved = 1, is_auto_fixed = 1 WHERE entity_type = 'track' AND entity_id = ? AND issue_type = 'non_en_genre'", t.trackID)

		updatedCount++
		updatedItems = append(updatedItems, nonEnGenreItem{
			Path:           t.filePath,
			CurrentGenre:   t.currentGenre,
			CanonicalGenre: canon,
			IsAutoFixed:    true,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":       len(errs) == 0,
		"updated_count": updatedCount,
		"total_checked": len(targets),
		"items":         updatedItems,
		"errors":        errs,
	})
}

func (h *FileHandler) ReorganizeLibrary(w http.ResponseWriter, r *http.Request) {
	// Query all tracks and reorganize canonical paths
	tracks, err := h.database.GetInvalidPaths(r.Context())
	if err != nil {
		http.Error(w, `{"error":"failed to query tracks"}`, http.StatusInternalServerError)
		return
	}

	moved := 0
	for _, tr := range tracks {
		if tr.SuggestedPath != tr.FilePath {
			srcFull, err := h.musicSandbox.Resolve(tr.FilePath, false)
			if err != nil {
				continue
			}
			dstFull, err := h.musicSandbox.Resolve(tr.SuggestedPath, false)
			if err != nil {
				continue
			}
			if actualDest, err := organizer.MoveTrackWithCompanions(srcFull, dstFull, false); err == nil {
				actualRel, _ := h.musicSandbox.ToRelative(actualDest)
				moved++
				_ = h.database.UpdateTrackPath(r.Context(), tr.FilePath, actualRel)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":      true,
		"total_tracks": len(tracks),
		"moved_tracks": moved,
		"total_albums": 1,
		"errors":       []string{},
	})
}

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

func (h *FileHandler) CatalogMatch(w http.ResponseWriter, r *http.Request) {
	albumPath := r.URL.Query().Get("path")
	deezerID := r.URL.Query().Get("deezer_album_id")

	if albumPath == "" {
		http.Error(w, `{"error":"missing path query param"}`, http.StatusBadRequest)
		return
	}

	var matchedAlbum *resolver.DeezerAlbum
	var err error

	if deezerID != "" {
		matchedAlbum, err = h.deezer.GetAlbum(r.Context(), deezerID)
	} else {
		// Search by folder name
		folderName := filepath.Base(albumPath)
		albums, searchErr := h.deezer.SearchAlbums(r.Context(), folderName)
		if searchErr == nil && len(albums) > 0 {
			matchedAlbum, err = h.deezer.GetAlbum(r.Context(), strconv.FormatInt(albums[0].ID, 10))
		}
	}

	if err != nil || matchedAlbum == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"path":                  albumPath,
			"album":                 filepath.Base(albumPath),
			"present_count":         0,
			"expected_count":        0,
			"missing_track_numbers": []int{},
			"missing_tracks":        []any{},
			"all_tracks":            []any{},
			"is_catalog_matched":    false,
		})
		return
	}

	cover := matchedAlbum.BestCoverURL()
	year := matchedAlbum.ReleaseYear()
	expectedCount := matchedAlbum.NbTracks

	var allTracks []map[string]any
	var missingTracks []map[string]any
	var missingNums []int

	if matchedAlbum.Tracks != nil {
		for i, t := range matchedAlbum.Tracks.Data {
			pos := t.TrackPosition
			if pos == 0 {
				pos = i + 1
			}
			trackInfo := map[string]any{
				"track_number": pos,
				"disc_number":  1,
				"title":        id3.CleanTitle(t.Title, ""),
				"artist":       t.Artist.Name,
				"duration":     t.Duration,
				"deezer_id":    strconv.FormatInt(t.ID, 10),
				"deezer_url":   fmt.Sprintf("https://www.deezer.com/track/%d", t.ID),
				"is_present":   false,
			}
			allTracks = append(allTracks, trackInfo)
			missingTracks = append(missingTracks, trackInfo)
			missingNums = append(missingNums, pos)
		}
	}

	resp := map[string]any{
		"path":                  albumPath,
		"album":                 matchedAlbum.Title,
		"artist":                matchedAlbum.Artist.Name,
		"year":                  year,
		"has_cover":             cover != "",
		"cover_url":             cover,
		"present_count":         0,
		"expected_count":        expectedCount,
		"missing_track_numbers": missingNums,
		"missing_tracks":        missingTracks,
		"all_tracks":            allTracks,
		"is_catalog_matched":    true,
		"deezer_album_id":       strconv.FormatInt(matchedAlbum.ID, 10),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

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

	albumArtist := ""
	albumTitle := filepath.Base(albumPath)
	if len(tracks) > 0 {
		albumArtist = tracks[0].Artist
		if tracks[0].Album != "" {
			albumTitle = tracks[0].Album
		}
	}

	resp := map[string]any{
		"album": map[string]any{
			"album_artist": albumArtist,
			"album":        albumTitle,
		},
		"tracks":           tracks,
		"source_dir":       albumPath,
		"job_ids":          []string{jobID},
		"missing_count":    0,
		"backfilled_count": len(tracks),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
