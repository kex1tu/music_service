package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
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

type coversBatchState struct {
	sync.Mutex
	isRunning    bool
	total        int
	current      int
	found        int
	failed       int
	currentAlbum string
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
	yandex       *resolver.YandexMusicClient
	ytmusic      *resolver.YouTubeMusicClient
	itunes       *resolver.ITunesClient
	httpClient   *http.Client
	lyricsState  lyricsBatchState
	coversState  coversBatchState
	trackCache   sync.Map // Кэш ID/хэш -> относительный путь
}

// FileHandlerParams содержит параметры для инициализации FileHandler.
type FileHandlerParams struct {
	MusicSandbox *sandbox.PathSandbox
	TmpSandbox   *sandbox.PathSandbox
	Database     *db.DB
	Downloader   *downloader.Service
	Organizer    *organizer.Service
	Deezer       *resolver.DeezerClient
	Lyrics       *lyrics.Service
	Scanner      *scanner.Scanner
	Yandex       *resolver.YandexMusicClient
	YTMusic      *resolver.YouTubeMusicClient
	ITunes       *resolver.ITunesClient
}

func NewFileHandler(p FileHandlerParams) *FileHandler {
	return &FileHandler{
		musicSandbox: p.MusicSandbox,
		tmpSandbox:   p.TmpSandbox,
		database:     p.Database,
		downloader:   p.Downloader,
		organizer:    p.Organizer,
		deezer:       p.Deezer,
		lyrics:       p.Lyrics,
		scanner:      p.Scanner,
		yandex:       p.Yandex,
		ytmusic:      p.YTMusic,
		itunes:       p.ITunes,
		httpClient:   &http.Client{Timeout: 15 * time.Second},
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
	ID           *string `json:"id,omitempty"`
	StreamURL    *string `json:"stream_url,omitempty"`
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

func (h *FileHandler) buildFileItem(itemFull, itemRel string, sz int64) fileItem {
	ext := filepath.Ext(itemRel)
	name := filepath.Base(itemRel)
	cleanRel := filepath.ToSlash(itemRel)

	fItem := fileItem{
		Name:         name,
		Path:         cleanRel,
		Type:         "file",
		Size:         &sz,
		Extension:    &ext,
		LyricsStatus: "none",
	}

	if strings.EqualFold(ext, ".mp3") {
		tHash := db.TrackHash(cleanRel)
		fItem.ID = &tHash
		streamURL := "/api/tracks/" + tHash + "/stream"
		fItem.StreamURL = &streamURL
		h.trackCache.Store(tHash, cleanRel)

		if tags, err := id3.ReadTags(itemFull, cleanRel); err == nil && tags != nil {
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

	return fItem
}

func (h *FileHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	recursive := r.URL.Query().Get("recursive") == "true"
	fullPath, err := h.musicSandbox.Resolve(relPath, true)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) {
			http.Error(w, `{"error":"access denied"}`, http.StatusForbidden)
		} else {
			http.Error(w, `{"error":"invalid path"}`, http.StatusBadRequest)
		}
		return
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

	if recursive {
		st, err := os.Stat(fullPath)
		if err != nil || !st.IsDir() {
			http.Error(w, `{"error":"failed to read directory"}`, http.StatusInternalServerError)
			return
		}

		items := make([]fileItem, 0)
		const maxTracks = 2000
		const maxDepth = 20

		_ = filepath.WalkDir(fullPath, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}

			// Safety check: avoid traversing outside sandbox
			relToSandbox, err := h.musicSandbox.ToRelative(path)
			if err != nil {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			name := d.Name()
			if d.IsDir() {
				if path != fullPath {
					if strings.HasPrefix(name, ".") || name == "tmp" || name == "tmp_downloads" {
						return filepath.SkipDir
					}
					// Depth limit relative to fullPath
					relFromStart, err := filepath.Rel(fullPath, path)
					if err == nil {
						parts := strings.Split(filepath.ToSlash(relFromStart), "/")
						if len(parts) > maxDepth {
							return filepath.SkipDir
						}
					}
				}
				return nil
			}

			// For files: skip hidden and non-mp3 files
			if strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".mp3") {
				return nil
			}

			// Track count limit
			if len(items) >= maxTracks {
				return filepath.SkipAll
			}

			info, _ := d.Info()
			var sz int64
			if info != nil {
				sz = info.Size()
			}

			items = append(items, h.buildFileItem(path, relToSandbox, sz))
			return nil
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(directoryListingResponse{
			CurrentPath:      relPath,
			ParentPath:       parentPath,
			Breadcrumbs:      breadcrumbs,
			Items:            items,
			TotalDirectories: 0,
			TotalFiles:       len(items),
		})
		return
	}

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		http.Error(w, `{"error":"failed to read directory"}`, http.StatusInternalServerError)
		return
	}

	items := make([]fileItem, 0)
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
			items = append(items, h.buildFileItem(itemFull, itemRel, sz))
		}
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
	if relPath == "" && r.Body != nil {
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			relPath = req.Path
		}
	}
	if relPath == "" {
		http.Error(w, `{"error":"missing path parameter"}`, http.StatusBadRequest)
		return
	}

	fullPath, err := h.musicSandbox.Resolve(relPath, false)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) {
			http.Error(w, `{"error":"access denied"}`, http.StatusForbidden)
		} else {
			http.Error(w, `{"error":"invalid path"}`, http.StatusBadRequest)
		}
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
