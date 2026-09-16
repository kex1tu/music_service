package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"

	"music-manager/internal/id3"
	"music-manager/internal/services/downloader"
	"music-manager/internal/services/organizer"
)

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
	var albumID int64
	var albumTitle, artistName string
	var dbDeezerID string
	var expCount, presentCount, totTracks int
	_ = h.database.Conn().QueryRowContext(r.Context(), `
		SELECT al.id, al.title, ar.name, COALESCE(al.deezer_id, ''),
		       COALESCE(al.expected_tracks_count, 0), COALESCE(al.present_tracks_count, 0), COALESCE(al.total_tracks, 0)
		FROM albums al
		JOIN artists ar ON al.artist_id = ar.id
		WHERE al.folder_path = ? OR al.folder_path = ? OR al.title = ?
		LIMIT 1
	`, req.Path, filepath.Clean(req.Path), filepath.Base(req.Path)).Scan(&albumID, &albumTitle, &artistName, &dbDeezerID, &expCount, &presentCount, &totTracks)

	if albumTitle == "" {
		albumTitle = filepath.Base(req.Path)
	}
	if artistName == "" {
		if parts := strings.Split(albumTitle, " - "); len(parts) >= 2 {
			albumTitle = strings.TrimSpace(parts[0])
			artistName = strings.TrimSpace(parts[1])
		}
	}

	deezerAlbumID := ""
	if req.DeezerAlbumID != nil && *req.DeezerAlbumID != "" {
		deezerAlbumID = *req.DeezerAlbumID
	} else if dbDeezerID != "" {
		deezerAlbumID = dbDeezerID
	}

	trackNumbers := req.TrackNumbers
	trackURLs := req.TrackURLs

	// 2. If track numbers not specified, load from missing_tracks table or calculate gaps
	if len(trackNumbers) == 0 && albumID > 0 {
		mRows, mErr := h.database.Conn().QueryContext(r.Context(), `
			SELECT track_number, COALESCE(deezer_url, '')
			FROM missing_tracks
			WHERE album_id = ?
			ORDER BY track_number
		`, albumID)
		if mErr == nil {
			for mRows.Next() {
				var tn int
				var dzURL string
				if err := mRows.Scan(&tn, &dzURL); err == nil {
					trackNumbers = append(trackNumbers, tn)
					if dzURL != "" {
						trackURLs = append(trackURLs, dzURL)
					}
				}
			}
			mRows.Close()
		}

		if len(trackNumbers) == 0 {
			targetExp := expCount
			if targetExp <= 0 && totTracks > 1 {
				targetExp = totTracks
			}
			if targetExp > presentCount {
				tRows, tErr := h.database.Conn().QueryContext(r.Context(), "SELECT track_number FROM tracks WHERE album_id = ?", albumID)
				if tErr == nil {
					presentNums := make(map[int]bool)
					for tRows.Next() {
						var tn int
						if err := tRows.Scan(&tn); err == nil {
							presentNums[tn] = true
						}
					}
					tRows.Close()
					for n := 1; n <= targetExp; n++ {
						if !presentNums[n] {
							trackNumbers = append(trackNumbers, n)
						}
					}
				}
			}
		}
	}

	job, err := h.downloader.CreateBackfillJob(
		req.Path,
		albumTitle,
		artistName,
		deezerAlbumID,
		trackURLs,
		trackNumbers,
	)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode([]*downloader.DownloadJob{job})
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
