package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
	"music-manager/internal/services/scanner"
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
	scanner      *scanner.Scanner
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
	scanner *scanner.Scanner,
) *FileHandler {
	return &FileHandler{
		musicSandbox: musicSandbox,
		tmpSandbox:   tmpSandbox,
		database:     database,
		downloader:   downloader,
		organizer:    organizer,
		deezer:       deezer,
		lyrics:       lyrics,
		scanner:      scanner,
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

		// Синхронизация БД при удалении директории
		cleanDir := filepath.Clean(filepath.ToSlash(relPath))
		_, _ = h.database.Conn().Exec(`
			DELETE FROM audit_issues 
			WHERE file_path = ? OR file_path LIKE ? || '/%' 
			   OR entity_id IN (SELECT id FROM tracks WHERE file_path = ? OR file_path LIKE ? || '/%');
		`, cleanDir, cleanDir, cleanDir, cleanDir)

		_, _ = h.database.Conn().Exec(`
			DELETE FROM track_genres 
			WHERE track_id IN (SELECT id FROM tracks WHERE file_path = ? OR file_path LIKE ? || '/%');
		`, cleanDir, cleanDir)

		_, _ = h.database.Conn().Exec(`
			DELETE FROM tracks WHERE file_path = ? OR file_path LIKE ? || '/%';
		`, cleanDir, cleanDir)

		_, _ = h.database.Conn().Exec(`
			DELETE FROM albums 
			WHERE folder_path = ? OR folder_path LIKE ? || '/%' 
			   OR (SELECT COUNT(*) FROM tracks WHERE album_id = albums.id) = 0;
		`, cleanDir, cleanDir)

		_, _ = h.database.Conn().Exec(`
			DELETE FROM artists WHERE (SELECT COUNT(*) FROM albums WHERE artist_id = artists.id) = 0;
		`)
		_, _ = h.database.Conn().Exec(`
			DELETE FROM missing_tracks WHERE album_id NOT IN (SELECT id FROM albums);
		`)
	} else {
		ext := strings.ToLower(filepath.Ext(fullPath))

		if err := os.Remove(fullPath); err != nil {
			http.Error(w, `{"error":"failed to delete file"}`, http.StatusInternalServerError)
			return
		}

		if ext == ".mp3" {
			// Удаляем сопутствующие тексты и обложки трека
			base := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))
			_ = os.Remove(base + ".lrc")
			_ = os.Remove(base + ".txt")
			_ = os.Remove(base + ".jpg")
			_ = os.Remove(base + ".jpeg")
			_ = os.Remove(base + ".png")
			_ = os.Remove(base + ".webp")

			// Синхронизация БД при удалении MP3
			var trackID, albumID int64
			err := h.database.Conn().QueryRow("SELECT id, album_id FROM tracks WHERE file_path = ?", relPath).Scan(&trackID, &albumID)
			if err == nil {
				_, _ = h.database.Conn().Exec("DELETE FROM audit_issues WHERE (entity_type = 'track' AND entity_id = ?) OR file_path = ?", trackID, relPath)
				_, _ = h.database.Conn().Exec("DELETE FROM track_genres WHERE track_id = ?", trackID)
				_, _ = h.database.Conn().Exec("DELETE FROM tracks WHERE id = ?", trackID)

				if albumID > 0 {
					var count int
					_ = h.database.Conn().QueryRow("SELECT COUNT(*) FROM tracks WHERE album_id = ?", albumID).Scan(&count)
					if count == 0 {
						_, _ = h.database.Conn().Exec("DELETE FROM albums WHERE id = ?", albumID)
						_, _ = h.database.Conn().Exec("DELETE FROM missing_tracks WHERE album_id = ?", albumID)
					} else {
						_, _ = h.database.Conn().Exec(`
							UPDATE albums SET
								present_tracks_count = ?,
								updated_at = CURRENT_TIMESTAMP
							WHERE id = ?
						`, count, albumID)
					}
				}

				_, _ = h.database.Conn().Exec("DELETE FROM artists WHERE (SELECT COUNT(*) FROM albums WHERE artist_id = artists.id) = 0")
				_, _ = h.database.Conn().Exec("DELETE FROM missing_tracks WHERE album_id NOT IN (SELECT id FROM albums)")
			}
		} else if ext == ".lrc" || ext == ".txt" {
			// Синхронизация БД при удалении текста песни
			audioRel := strings.TrimSuffix(relPath, ext) + ".mp3"
			audioFull := strings.TrimSuffix(fullPath, ext) + ".mp3"
			lrcFull := strings.TrimSuffix(fullPath, ext) + ".lrc"
			txtFull := strings.TrimSuffix(fullPath, ext) + ".txt"

			if !fileExists(lrcFull) && !fileExists(txtFull) {
				_, _ = h.database.Conn().Exec("UPDATE tracks SET lyrics_status = 'none' WHERE file_path = ?", audioRel)
				var trackID int64
				if err := h.database.Conn().QueryRow("SELECT id FROM tracks WHERE file_path = ?", audioRel).Scan(&trackID); err == nil {
					_, _ = h.database.Conn().Exec(`
						INSERT INTO audit_issues (issue_type, entity_type, entity_id, file_path, details)
						VALUES ('missing_lyrics', 'track', ?, ?, 'Отсутствует текст песни (.lrc/.txt)')
					`, trackID, audioRel)
				}
			}
			_ = audioFull
		} else if ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
			// Синхронизация БД при удалении обложки
			albumDir := filepath.Dir(relPath)
			if albumDir == "." {
				albumDir = ""
			}
			fullAlbumDir := filepath.Dir(fullPath)
			hasCover := fileExists(filepath.Join(fullAlbumDir, "cover.jpg")) ||
				fileExists(filepath.Join(fullAlbumDir, "cover.png")) ||
				fileExists(filepath.Join(fullAlbumDir, "cover.jpeg")) ||
				fileExists(filepath.Join(fullAlbumDir, "folder.jpg")) ||
				fileExists(filepath.Join(fullAlbumDir, "folder.png"))

			if !hasCover {
				_, _ = h.database.Conn().Exec("UPDATE albums SET has_cover = 0, cover_path = NULL WHERE folder_path = ?", albumDir)
				_, _ = h.database.Conn().Exec("UPDATE tracks SET has_embedded_cover = 0 WHERE album_id IN (SELECT id FROM albums WHERE folder_path = ?)", albumDir)
			}
		}
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

	album := strings.TrimSpace(tags.Album)
	if album == "" || strings.EqualFold(album, "single") || strings.EqualFold(album, "singles") {
		if strings.TrimSpace(tags.Title) != "" {
			album = strings.TrimSpace(tags.Title)
		}
	}
	artist := strings.TrimSpace(tags.AlbumArtist)
	if artist == "" {
		artist = strings.TrimSpace(tags.Artist)
	}
	if artist == "" {
		artist = "Unknown Artist"
	}
	_, targetRel := h.organizer.ComputeTargetPath(artist, album, tags.TrackNumber, tags.Title, filepath.Base(fullPath))
	targetFull, err := h.musicSandbox.Resolve(targetRel, true)
	if err != nil {
		http.Error(w, `{"error":"failed to resolve target"}`, http.StatusBadRequest)
		return
	}

	if fullPath != targetFull {
		oldDir := filepath.Dir(fullPath)

		actualDest, err := organizer.MoveTrackWithCompanions(fullPath, targetFull, req.Overwrite)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed to move file: %v"}`, err), http.StatusInternalServerError)
			return
		}
		actualRel, _ := h.musicSandbox.ToRelative(actualDest)
		_ = h.database.UpdateTrackPath(r.Context(), req.Path, actualRel)
		_, _ = h.database.Conn().ExecContext(r.Context(), "DELETE FROM audit_issues WHERE file_path = ? AND issue_type = 'invalid_path'", req.Path)
		targetRel = actualRel

		// Очищаем старую папку и её пустых родителей (например, папку артиста)
		organizer.CleanEmptyParents(oldDir, h.musicSandbox.Root())
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
	oldDirs := make(map[string]bool)

	for _, p := range paths {
		fullPath, err := h.musicSandbox.Resolve(p.FilePath, false)
		if err != nil {
			continue
		}
		targetFull, err := h.musicSandbox.Resolve(p.SuggestedPath, true)
		if err != nil {
			continue
		}

		oldDir := filepath.Dir(fullPath)

		if actualDest, err := organizer.MoveTrackWithCompanions(fullPath, targetFull, false); err == nil {
			actualRel, _ := h.musicSandbox.ToRelative(actualDest)
			_ = h.database.UpdateTrackPath(r.Context(), p.FilePath, actualRel)
			_, _ = h.database.Conn().ExecContext(r.Context(), "DELETE FROM audit_issues WHERE file_path = ? AND issue_type = 'invalid_path'", p.FilePath)
			fixed++
			oldDirs[oldDir] = true
		} else {
			errs = append(errs, fmt.Sprintf("%s: %v", p.FilePath, err))
		}
	}

	// Удаляем все опустевшие старые папки
	root := h.musicSandbox.Root()
	for dir := range oldDirs {
		organizer.CleanEmptyParents(dir, root)
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
	rows, err := h.database.Conn().QueryContext(r.Context(), `
		SELECT al.id, al.folder_path, al.title, a.name, al.year, al.has_cover, al.cover_path,
		       al.present_tracks_count, al.expected_tracks_count, al.total_tracks, al.deezer_id
		FROM albums al
		JOIN artists a ON al.artist_id = a.id
		WHERE al.is_complete = 0 OR (al.expected_tracks_count > al.present_tracks_count) OR (al.total_tracks > 1 AND al.present_tracks_count < al.total_tracks)
		ORDER BY al.title
		LIMIT 100;
	`)
	if err != nil {
		http.Error(w, `{"error":"database query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type incompleteAlbumItem struct {
		Path                string  `json:"path"`
		Album               string  `json:"album"`
		Artist              string  `json:"artist"`
		Year                *int    `json:"year"`
		HasCover            bool    `json:"has_cover"`
		CoverURL            *string `json:"cover_url"`
		PresentCount        int     `json:"present_count"`
		ExpectedCount       int     `json:"expected_count"`
		MissingTrackNumbers []int   `json:"missing_track_numbers"`
		IsCatalogMatched    bool    `json:"is_catalog_matched"`
		DeezerAlbumID       *string `json:"deezer_album_id"`
	}

	type albumRow struct {
		id            int64
		item          incompleteAlbumItem
		totalTracks   int
	}

	var albumRows []albumRow
	for rows.Next() {
		var a albumRow
		var year, expTracks, totTracks sqlInt
		var deezerID, coverPath sqlStr
		if err := rows.Scan(
			&a.id, &a.item.Path, &a.item.Album, &a.item.Artist, &year, &a.item.HasCover, &coverPath,
			&a.item.PresentCount, &expTracks, &totTracks, &deezerID,
		); err == nil {
			if year.Valid {
				y := int(year.Int64)
				a.item.Year = &y
			}
			exp := int(expTracks.Int64)
			if exp <= 0 && totTracks.Valid {
				exp = int(totTracks.Int64)
			}
			a.item.ExpectedCount = exp
			a.totalTracks = int(totTracks.Int64)
			a.item.MissingTrackNumbers = []int{}

			if deezerID.Valid && deezerID.String != "" {
				s := deezerID.String
				a.item.DeezerAlbumID = &s
				a.item.IsCatalogMatched = true
			}
			albumRows = append(albumRows, a)
		}
	}

	var items []incompleteAlbumItem
	for _, a := range albumRows {
		it := a.item
		// Query missing track numbers from missing_tracks table
		mRows, mErr := h.database.Conn().QueryContext(r.Context(), "SELECT track_number FROM missing_tracks WHERE album_id = ? ORDER BY track_number", a.id)
		if mErr == nil {
			for mRows.Next() {
				var tn int
				if err := mRows.Scan(&tn); err == nil {
					it.MissingTrackNumbers = append(it.MissingTrackNumbers, tn)
				}
			}
			mRows.Close()
		}

		// Fallback: if missing_tracks table empty but expected > present, compute gaps
		if len(it.MissingTrackNumbers) == 0 && it.ExpectedCount > it.PresentCount {
			tRows, tErr := h.database.Conn().QueryContext(r.Context(), "SELECT track_number FROM tracks WHERE album_id = ?", a.id)
			if tErr == nil {
				presentNums := make(map[int]bool)
				for tRows.Next() {
					var tn int
					if err := tRows.Scan(&tn); err == nil {
						presentNums[tn] = true
					}
				}
				tRows.Close()

				for n := 1; n <= it.ExpectedCount; n++ {
					if !presentNums[n] {
						it.MissingTrackNumbers = append(it.MissingTrackNumbers, n)
					}
				}
			}
		}

		items = append(items, it)
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

	if req.Path == "" && (req.DeezerAlbumID == nil || *req.DeezerAlbumID == "") {
		http.Error(w, `{"error":"path or deezer_album_id required"}`, http.StatusBadRequest)
		return
	}

	// 1. Look up album metadata from database
	var albumTitle, artistName string
	var dbDeezerID string
	_ = h.database.Conn().QueryRowContext(r.Context(), `
		SELECT al.title, ar.name, COALESCE(al.deezer_id, '')
		FROM albums al
		JOIN artists ar ON al.artist_id = ar.id
		WHERE al.folder_path = ? OR al.folder_path = ?
		LIMIT 1
	`, req.Path, filepath.Clean(req.Path)).Scan(&albumTitle, &artistName, &dbDeezerID)

	deezerAlbumID := ""
	if req.DeezerAlbumID != nil && *req.DeezerAlbumID != "" {
		deezerAlbumID = *req.DeezerAlbumID
	} else if dbDeezerID != "" {
		deezerAlbumID = dbDeezerID
	}

	if albumTitle == "" {
		albumTitle = filepath.Base(req.Path)
	}

	job, err := h.downloader.CreateBackfillJob(
		req.Path,
		albumTitle,
		artistName,
		deezerAlbumID,
		req.TrackURLs,
		req.TrackNumbers,
	)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode([]*downloader.DownloadJob{job})
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
	if h.scanner != nil {
		_, _ = h.scanner.ScanAll(r.Context())
	}
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

		// Обновляем связи в SQLite для каждого канонического жанра
		_, _ = h.database.Conn().Exec("DELETE FROM track_genres WHERE track_id = ?", t.trackID)
		for _, cg := range id3.ParseGenres(canon) {
			var canonID int64
			err = h.database.Conn().QueryRow("SELECT id FROM genres WHERE name = ?", cg).Scan(&canonID)
			if err == sql.ErrNoRows {
				res, insErr := h.database.Conn().Exec("INSERT INTO genres (name, is_valid_en, canonical_en_name) VALUES (?, 1, ?)", cg, cg)
				if insErr == nil {
					canonID, _ = res.LastInsertId()
				}
			}
			if canonID > 0 {
				_, _ = h.database.Conn().Exec("INSERT OR IGNORE INTO track_genres (track_id, genre_id) VALUES (?, ?)", t.trackID, canonID)
			}
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
	// 1. Получаем все неканонические треки
	tracks, err := h.database.GetInvalidPaths(r.Context())
	if err != nil {
		http.Error(w, `{"error":"failed to query tracks"}`, http.StatusInternalServerError)
		return
	}

	moved := 0
	oldDirs := make(map[string]bool)

	for _, tr := range tracks {
		if tr.SuggestedPath != tr.FilePath {
			srcFull, err := h.musicSandbox.Resolve(tr.FilePath, false)
			if err != nil {
				continue
			}
			dstFull, err := h.musicSandbox.Resolve(tr.SuggestedPath, true)
			if err != nil {
				continue
			}

			oldDir := filepath.Dir(srcFull)

			if actualDest, err := organizer.MoveTrackWithCompanions(srcFull, dstFull, false); err == nil {
				actualRel, _ := h.musicSandbox.ToRelative(actualDest)
				moved++
				_ = h.database.UpdateTrackPath(r.Context(), tr.FilePath, actualRel)
				_, _ = h.database.Conn().ExecContext(r.Context(), "DELETE FROM audit_issues WHERE file_path = ? AND issue_type = 'invalid_path'", tr.FilePath)
				oldDirs[oldDir] = true
			}
		}
	}

	// 2. Рекурсивно очищаем все опустевшие каталоги (папки исполнителей и старые альбомы)
	root := h.musicSandbox.Root()
	for dir := range oldDirs {
		organizer.CleanEmptyParents(dir, root)
	}

	// Дополнительный проход по всем подпапкам медиатеки на случай пустых папок, оставшихся ранее
	cleanAllEmptyDirs(root)

	// 3. Запускаем полное пересканирование медиатеки для обновления дерева в БД
	if h.scanner != nil {
		_, _ = h.scanner.ScanAll(r.Context())
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

// cleanAllEmptyDirs рекурсивно обходит дерево директорий снизу вверх и удаляет пустые папки
func cleanAllEmptyDirs(root string) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || !d.IsDir() || path == root {
			return nil
		}
		// Пропускаем служебные папки
		name := d.Name()
		if strings.HasPrefix(name, ".") || name == "tmp" || name == "tmp_downloads" {
			return filepath.SkipDir
		}
		return nil
	})

	// Обход в обратном порядке (от листьев к корню), чтобы удалить вложенные пустые папки
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d != nil && d.IsDir() && path != root {
			name := d.Name()
			if !strings.HasPrefix(name, ".") && name != "tmp" && name != "tmp_downloads" {
				dirs = append(dirs, path)
			}
		}
		return nil
	})

	for i := len(dirs) - 1; i >= 0; i-- {
		organizer.RemoveIfEmpty(dirs[i])
	}
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

var fuzzyPunctuationRegex = regexp.MustCompile(`[^\p{L}\p{N}]+`)
var fuzzyBracketsRegex = regexp.MustCompile(`[\(\[\{].*?[\)\]\}]`)

func cleanFuzzyString(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = fuzzyBracketsRegex.ReplaceAllString(s, "")
	s = fuzzyPunctuationRegex.ReplaceAllString(s, "")
	return s
}

func (h *FileHandler) CatalogMatch(w http.ResponseWriter, r *http.Request) {
	albumPath := r.URL.Query().Get("path")
	deezerID := r.URL.Query().Get("deezer_album_id")

	if albumPath == "" {
		http.Error(w, `{"error":"missing path query param"}`, http.StatusBadRequest)
		return
	}

	// 1. Query database for album metadata
	var albumID int64
	var dbArtist, dbAlbum, dbDeezerID string
	var dbYear int
	_ = h.database.Conn().QueryRowContext(r.Context(), `
		SELECT al.id, ar.name, al.title, COALESCE(al.year, 0), COALESCE(al.deezer_id, '')
		FROM albums al
		JOIN artists ar ON al.artist_id = ar.id
		WHERE al.folder_path = ? OR al.folder_path = ?
		LIMIT 1
	`, albumPath, filepath.Clean(albumPath)).Scan(&albumID, &dbArtist, &dbAlbum, &dbYear, &dbDeezerID)

	if deezerID == "" && dbDeezerID != "" {
		deezerID = dbDeezerID
	}

	var matchedAlbum *resolver.DeezerAlbum
	var err error

	if deezerID != "" {
		matchedAlbum, err = h.deezer.GetAlbum(r.Context(), deezerID)
	}

	if matchedAlbum == nil {
		// Try searching by artist and album
		if dbArtist != "" && dbAlbum != "" {
			q := fmt.Sprintf(`artist:"%s" album:"%s"`, dbArtist, dbAlbum)
			albums, searchErr := h.deezer.SearchAlbums(r.Context(), q)
			if searchErr == nil && len(albums) > 0 {
				matchedAlbum, err = h.deezer.GetAlbum(r.Context(), strconv.FormatInt(albums[0].ID, 10))
			}
		}
	}

	if matchedAlbum == nil && dbAlbum != "" {
		albums, searchErr := h.deezer.SearchAlbums(r.Context(), dbAlbum)
		if searchErr == nil && len(albums) > 0 {
			matchedAlbum, err = h.deezer.GetAlbum(r.Context(), strconv.FormatInt(albums[0].ID, 10))
		}
	}

	if matchedAlbum == nil {
		// Fallback: search by folder base name
		folderName := filepath.Base(albumPath)
		// If folder has format "Album - Artist", split it
		if parts := strings.Split(folderName, " - "); len(parts) >= 2 {
			q := fmt.Sprintf(`artist:"%s" album:"%s"`, strings.TrimSpace(parts[1]), strings.TrimSpace(parts[0]))
			albums, searchErr := h.deezer.SearchAlbums(r.Context(), q)
			if searchErr == nil && len(albums) > 0 {
				matchedAlbum, err = h.deezer.GetAlbum(r.Context(), strconv.FormatInt(albums[0].ID, 10))
			}
		}
		if matchedAlbum == nil {
			albums, searchErr := h.deezer.SearchAlbums(r.Context(), folderName)
			if searchErr == nil && len(albums) > 0 {
				matchedAlbum, err = h.deezer.GetAlbum(r.Context(), strconv.FormatInt(albums[0].ID, 10))
			}
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

	// 2. Read local tracks in album directory
	type localTrack struct {
		TrackNumber int
		Title       string
		Path        string
		Duration    float64
	}
	var localTracks []localTrack
	if albumID > 0 {
		tRows, tErr := h.database.Conn().QueryContext(r.Context(), "SELECT track_number, title, file_path, duration FROM tracks WHERE album_id = ?", albumID)
		if tErr == nil {
			for tRows.Next() {
				var lt localTrack
				if err := tRows.Scan(&lt.TrackNumber, &lt.Title, &lt.Path, &lt.Duration); err == nil {
					localTracks = append(localTracks, lt)
				}
			}
			tRows.Close()
		}
	}

	if len(localTracks) == 0 {
		if fullPath, err := h.musicSandbox.Resolve(albumPath, false); err == nil {
			entries, _ := os.ReadDir(fullPath)
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp3") {
					itemRel := filepath.Join(albumPath, e.Name())
					itemFull := filepath.Join(fullPath, e.Name())
					if t, err := id3.ReadTags(itemFull, itemRel); err == nil {
						localTracks = append(localTracks, localTrack{
							TrackNumber: t.TrackNumber,
							Title:       t.Title,
							Path:        itemRel,
							Duration:    t.Duration,
						})
					}
				}
			}
		}
	}

	presentByNum := make(map[int]localTrack)
	presentByTitle := make(map[string]localTrack)
	for _, lt := range localTracks {
		if lt.TrackNumber > 0 {
			presentByNum[lt.TrackNumber] = lt
		}
		cl := cleanFuzzyString(lt.Title)
		if cl != "" {
			presentByTitle[cl] = lt
		}
	}

	cover := matchedAlbum.BestCoverURL()
	year := matchedAlbum.ReleaseYear()
	expectedCount := matchedAlbum.NbTracks

	var allTracks []map[string]any
	var missingTracks []map[string]any
	var missingNums []int
	presentCount := 0

	if matchedAlbum.Tracks != nil {
		for i, t := range matchedAlbum.Tracks.Data {
			pos := t.TrackPosition
			if pos == 0 {
				pos = i + 1
			}
			cleanT := cleanFuzzyString(t.Title)

			isPresent := false
			localPath := ""
			if lt, ok := presentByNum[pos]; ok {
				isPresent = true
				localPath = lt.Path
			} else if lt, ok := presentByTitle[cleanT]; ok {
				isPresent = true
				localPath = lt.Path
			}

			tArtist := t.Artist.Name
			if tArtist == "" {
				tArtist = matchedAlbum.Artist.Name
			}

			discNum := t.DiskNumber
			if discNum <= 0 {
				discNum = 1
			}

			trackInfo := map[string]any{
				"track_number": pos,
				"disc_number":  discNum,
				"title":        id3.CleanTitle(t.Title, ""),
				"artist":       tArtist,
				"duration":     t.Duration,
				"deezer_id":    strconv.FormatInt(t.ID, 10),
				"deezer_url":   fmt.Sprintf("https://www.deezer.com/track/%d", t.ID),
				"is_present":   isPresent,
				"local_path":   localPath,
			}
			allTracks = append(allTracks, trackInfo)
			if isPresent {
				presentCount++
			} else {
				missingTracks = append(missingTracks, trackInfo)
				missingNums = append(missingNums, pos)
			}
		}
	}

	// 3. Update database with verification results
	isComplete := 1
	if len(missingTracks) > 0 || (expectedCount > 1 && presentCount < expectedCount) {
		isComplete = 0
	}

	if albumID > 0 {
		_, _ = h.database.Conn().ExecContext(r.Context(), `
			UPDATE albums SET
				expected_tracks_count = ?,
				total_tracks = ?,
				present_tracks_count = ?,
				deezer_id = ?,
				is_complete = ?,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, expectedCount, expectedCount, presentCount, strconv.FormatInt(matchedAlbum.ID, 10), isComplete, albumID)

		// Repopulate missing_tracks table
		_, _ = h.database.Conn().ExecContext(r.Context(), "DELETE FROM missing_tracks WHERE album_id = ?", albumID)
		for _, mt := range missingTracks {
			tNum, _ := mt["track_number"].(int)
			dNum, _ := mt["disc_number"].(int)
			if dNum <= 0 {
				dNum = 1
			}
			tit, _ := mt["title"].(string)
			art, _ := mt["artist"].(string)
			dur, _ := mt["duration"].(int)
			dzID, _ := mt["deezer_id"].(string)
			dzURL, _ := mt["deezer_url"].(string)

			_, _ = h.database.Conn().ExecContext(r.Context(), `
				INSERT INTO missing_tracks (
					album_id, title, artist, track_number, disc_number,
					expected_duration, deezer_id, deezer_url, status, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'missing', CURRENT_TIMESTAMP)
				ON CONFLICT(album_id, track_number, disc_number) DO UPDATE SET
					title = excluded.title,
					artist = excluded.artist,
					deezer_id = excluded.deezer_id,
					deezer_url = excluded.deezer_url,
					updated_at = CURRENT_TIMESTAMP
			`, albumID, tit, art, tNum, dNum, dur, dzID, dzURL)
		}
	}

	resp := map[string]any{
		"path":                  albumPath,
		"album":                 matchedAlbum.Title,
		"artist":                matchedAlbum.Artist.Name,
		"year":                  year,
		"has_cover":             cover != "",
		"cover_url":             cover,
		"present_count":         presentCount,
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
