package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
)

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
