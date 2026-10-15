package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

// FixTagIssue исправляет теги одного трека и обновляет SQLite индекс.
func (h *FileHandler) FixTagIssue(w http.ResponseWriter, r *http.Request) {
	var req FixTagIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	if req.Path == "" {
		http.Error(w, `{"error":"missing path"}`, http.StatusBadRequest)
		return
	}

	fullPath, err := h.musicSandbox.Resolve(req.Path, false)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) {
			http.Error(w, `{"error":"access denied"}`, http.StatusForbidden)
		} else {
			http.Error(w, `{"error":"file not found in sandbox"}`, http.StatusNotFound)
		}
		return
	}

	tags, err := id3.ReadTags(fullPath, req.Path)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to read tags: %v"}`, err), http.StatusInternalServerError)
		return
	}

	if strings.TrimSpace(req.Title) != "" {
		tags.Title = strings.TrimSpace(req.Title)
	}
	if strings.TrimSpace(req.Artist) != "" {
		tags.Artist = strings.TrimSpace(req.Artist)
	}
	if strings.TrimSpace(req.Album) != "" {
		tags.Album = strings.TrimSpace(req.Album)
	}
	if req.Year > 0 {
		tags.Year = req.Year
	}

	// Сверка и дообогащение соавторов через FeatEnricher (Yandex + YouTube Music)
	if h.downloader != nil && h.downloader.Resolver() != nil && h.downloader.Resolver().Enricher() != nil {
		currentArts := id3.ParseMultiValue(tags.Artist)
		enriched := h.downloader.Resolver().Enricher().EnrichTrackArtists(r.Context(), tags.Title, currentArts)
		if len(enriched) > len(currentArts) {
			tags.Artist = id3.FormatMultiValue(enriched)
		}
	}

	if err := id3.WriteTags(fullPath, *tags, nil); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to write tags: %v"}`, err), http.StatusInternalServerError)
		return
	}

	if h.scanner != nil {
		_ = h.scanner.IndexFile(fullPath, req.Path)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"path":    req.Path,
		"tags":    tags,
	})
}

// FixSafeTagIssues выполняет быстрое и детерминированное локальное исправление дефектов категории safe_autofix
// (клиповый мусор, номер трека в названии, топик-каналы, литеральные слеши Navidrome) без внешних сетевых вызовов.
func (h *FileHandler) FixSafeTagIssues(w http.ResponseWriter, r *http.Request) {
	rows, err := h.database.Conn().QueryContext(r.Context(), `
		SELECT t.id, t.file_path, t.title, a.name, al.title, COALESCE(al.year, 0), t.track_number, t.duration
		FROM tracks t
		JOIN artists a ON t.artist_id = a.id
		JOIN albums al ON t.album_id = al.id;
	`)
	if err != nil {
		http.Error(w, `{"error":"database query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var toFix []TagIssueItem
	for rows.Next() {
		var id int64
		var filePath, title, artist, album string
		var year, trackNumber int
		var duration float64

		if err := rows.Scan(&id, &filePath, &title, &artist, &album, &year, &trackNumber, &duration); err != nil {
			continue
		}

		item := analyzeTrackIssues(id, filePath, title, artist, album, year, trackNumber, duration)
		if item == nil {
			continue
		}

		// Исправляем только треки с дефектами категории safe_autofix
		hasSafeIssue := false
		for _, iss := range item.Issues {
			if iss == "dirty_title" || iss == "track_num_in_title" || iss == "literal_backslash_artist" || iss == "label_as_artist" || iss == "missing_album_artist" {
				hasSafeIssue = true
				break
			}
		}
		if hasSafeIssue {
			toFix = append(toFix, *item)
		}
	}

	fixedCount := 0
	var errs []string

	for _, item := range toFix {
		fullPath, err := h.musicSandbox.Resolve(item.FilePath, false)
		if err != nil {
			continue
		}

		tags, err := id3.ReadTags(fullPath, item.FilePath)
		if err != nil {
			continue
		}

		// Применяем строго безопасные исправления
		if item.Proposed.Title != "" {
			tags.Title = item.Proposed.Title
		}
		if item.Proposed.Artist != "" {
			tags.Artist = item.Proposed.Artist
		}
		if item.Proposed.AlbumArtist != "" && (strings.TrimSpace(tags.AlbumArtist) == "" || strings.EqualFold(tags.AlbumArtist, "unknown artist") || strings.EqualFold(tags.Album, tags.Title) || strings.EqualFold(item.Proposed.Album, item.Proposed.Title)) {
			tags.AlbumArtist = item.Proposed.AlbumArtist
		}

		if err := id3.WriteTags(fullPath, *tags, nil); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", item.FilePath, err))
			continue
		}

		if h.scanner != nil {
			_ = h.scanner.IndexFile(fullPath, item.FilePath)
		}
		fixedCount++
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":     true,
		"fixed_count": fixedCount,
		"total_found": len(toFix),
		"errors":      errs,
	})
}

// FixSelectedTagIssues применяет предложенные исправления только к выбранным пользователем трекам.
func (h *FileHandler) FixSelectedTagIssues(w http.ResponseWriter, r *http.Request) {
	var req FixSelectedTagsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	fixedCount := 0
	var errs []string

	for _, item := range req.Items {
		if item.Path == "" {
			continue
		}
		fullPath, err := h.musicSandbox.Resolve(item.Path, false)
		if err != nil {
			continue
		}

		tags, err := id3.ReadTags(fullPath, item.Path)
		if err != nil {
			continue
		}

		if strings.TrimSpace(item.Title) != "" {
			tags.Title = strings.TrimSpace(item.Title)
		}
		if strings.TrimSpace(item.Artist) != "" {
			tags.Artist = strings.TrimSpace(item.Artist)
			if strings.TrimSpace(tags.AlbumArtist) == "" || strings.EqualFold(tags.AlbumArtist, "unknown artist") {
				if strings.EqualFold(tags.Album, tags.Title) {
					tags.AlbumArtist = tags.Artist
				} else {
					tags.AlbumArtist = resolver.ExtractPrimaryArtist(tags.Artist)
				}
			}
		}
		if strings.TrimSpace(item.Album) != "" {
			tags.Album = strings.TrimSpace(item.Album)
		}
		if item.Year > 0 {
			tags.Year = item.Year
		}

		if err := id3.WriteTags(fullPath, *tags, nil); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", item.Path, err))
			continue
		}

		if h.scanner != nil {
			_ = h.scanner.IndexFile(fullPath, item.Path)
		}
		fixedCount++
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":     true,
		"fixed_count": fixedCount,
		"total_found": len(req.Items),
		"errors":      errs,
	})
}

// FixAllTagIssues выполняет пакетное исправление всех явных дефектов тегов в медиатеке.
func (h *FileHandler) FixAllTagIssues(w http.ResponseWriter, r *http.Request) {
	rows, err := h.database.Conn().QueryContext(r.Context(), `
		SELECT t.id, t.file_path, t.title, a.name, al.title, COALESCE(al.year, 0), t.track_number, t.duration
		FROM tracks t
		JOIN artists a ON t.artist_id = a.id
		JOIN albums al ON t.album_id = al.id;
	`)
	if err != nil {
		http.Error(w, `{"error":"database query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var toFix []TagIssueItem
	for rows.Next() {
		var id int64
		var filePath, title, artist, album string
		var year, trackNumber int
		var duration float64

		if err := rows.Scan(&id, &filePath, &title, &artist, &album, &year, &trackNumber, &duration); err != nil {
			continue
		}

		item := analyzeTrackIssues(id, filePath, title, artist, album, year, trackNumber, duration)
		if item == nil {
			continue
		}

		// Исправляем дефекты, для которых вычислено безопасное или проверенное решение
		hasFixableIssue := false
		for _, iss := range item.Issues {
			if iss == "dirty_title" || iss == "track_num_in_title" || iss == "literal_backslash_artist" ||
				iss == "unparsed_feat" || iss == "artist_in_title" || iss == "label_as_artist" {
				hasFixableIssue = true
				break
			}
		}
		if hasFixableIssue {
			toFix = append(toFix, *item)
		}
	}

	fixedCount := 0
	var errs []string

	for _, item := range toFix {
		fullPath, err := h.musicSandbox.Resolve(item.FilePath, false)
		if err != nil {
			continue
		}

		tags, err := id3.ReadTags(fullPath, item.FilePath)
		if err != nil {
			continue
		}

		if item.Proposed.Title != "" {
			tags.Title = item.Proposed.Title
		}
		if item.Proposed.Artist != "" {
			tags.Artist = item.Proposed.Artist
		}
		if item.Proposed.AlbumArtist != "" && (strings.TrimSpace(tags.AlbumArtist) == "" || strings.EqualFold(tags.AlbumArtist, "unknown artist") || strings.EqualFold(tags.Album, tags.Title) || strings.EqualFold(item.Proposed.Album, item.Proposed.Title)) {
			tags.AlbumArtist = item.Proposed.AlbumArtist
		}
		if item.Proposed.Album != "" && (tags.Album == "" || strings.EqualFold(tags.Album, "unknown album") || strings.EqualFold(tags.Album, "single") || strings.EqualFold(tags.Album, "singles") || strings.HasPrefix(strings.ToLower(tags.Album), "single - ") || strings.HasPrefix(strings.ToLower(tags.Album), "singles - ")) {
			tags.Album = item.Proposed.Album
		}
		if item.Proposed.Year > 0 && tags.Year == 0 {
			tags.Year = item.Proposed.Year
		}

		if err := id3.WriteTags(fullPath, *tags, nil); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", item.FilePath, err))
			continue
		}

		if h.scanner != nil {
			_ = h.scanner.IndexFile(fullPath, item.FilePath)
		}
		fixedCount++
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":     true,
		"fixed_count": fixedCount,
		"total_found": len(toFix),
		"errors":      errs,
	})
}
