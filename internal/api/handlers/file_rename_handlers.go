package handlers

import (
	"encoding/json"
	"net/http"

	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
	"music-manager/internal/db"
)

// RenamePatternItem представляет один элемент для переименования.
type RenamePatternItem struct {
	CurrentPath string `json:"current_path"` // относительный путь в MUSIC_DIR
	NewName     string `json:"new_name"`     // новое имя файла (например "01 - Artist - Title.mp3") или относительный путь
}

// RenamePatternRequest содержит список элементов и флаг dry_run.
type RenamePatternRequest struct {
	Items  []RenamePatternItem `json:"items"`
	DryRun bool                `json:"dry_run"`
}

// RenamePatternResult результат операции переименования для одного элемента.
type RenamePatternResult struct {
	CurrentPath string `json:"current_path"`
	NewPath     string `json:"new_path"`
	Status      string `json:"status"` // "ok" | "conflict" | "error"
	Error       string `json:"error,omitempty"`
}

// RenamePattern выполняет пакетное безопасное переименование файлов по шаблону с проверкой песочницы.
func (h *FileHandler) RenamePattern(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		http.Error(w, `{"error":"missing request body"}`, http.StatusBadRequest)
		return
	}

	var req RenamePatternRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	results := make([]RenamePatternResult, 0, len(req.Items))
	claimedTargets := make(map[string]bool)

	for _, item := range req.Items {
		itemCurrent := strings.TrimSpace(item.CurrentPath)
		itemNew := strings.TrimSpace(item.NewName)

		if itemCurrent == "" || itemNew == "" {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     "",
				Status:      "error",
				Error:       "current_path and new_name cannot be empty",
			})
			continue
		}

		targetProposedRel := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(itemCurrent), itemNew)))

		// 1. Разрешаем текущий путь внутри песочницы
		currentFull, err := h.musicSandbox.Resolve(itemCurrent, true)
		if err != nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetProposedRel,
				Status:      "error",
				Error:       err.Error(),
			})
			continue
		}

		// Проверяем существование исходного файла
		stCurrent, err := safefs.Stat(currentFull)
		if err != nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetProposedRel,
				Status:      "error",
				Error:       "source file does not exist",
			})
			continue
		}
		if stCurrent.IsDir() {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetProposedRel,
				Status:      "error",
				Error:       "source path is a directory",
			})
			continue
		}

		// 2. Валидируем и разрешаем целевой путь внутри песочницы
		targetFull, err := h.musicSandbox.Resolve(filepath.Join(filepath.Dir(itemCurrent), itemNew), true)
		if err != nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetProposedRel,
				Status:      "error",
				Error:       err.Error(),
			})
			continue
		}

		targetRel, err := h.musicSandbox.ToRelative(targetFull)
		if err != nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetProposedRel,
				Status:      "error",
				Error:       err.Error(),
			})
			continue
		}

		// 3. Если путь не изменился
		if currentFull == targetFull {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetRel,
				Status:      "ok",
			})
			continue
		}

		// 4. Проверка коллизий
		if _, err := safefs.Stat(targetFull); err == nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetRel,
				Status:      "conflict",
				Error:       "target file already exists",
			})
			continue
		}

		if claimedTargets[targetFull] {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetRel,
				Status:      "conflict",
				Error:       "target file collision within batch",
			})
			continue
		}
		claimedTargets[targetFull] = true

		// 5. В режиме dry_run возвращаем статус "ok" без изменения файловой системы
		if req.DryRun {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetRel,
				Status:      "ok",
			})
			continue
		}

		// 6. Убеждаемся, что целевая директория существует
		targetDir := filepath.Dir(targetFull)
		if err := safefs.MkdirAll(targetDir, 0755); err != nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetRel,
				Status:      "error",
				Error:       "failed to create destination directory: " + err.Error(),
			})
			continue
		}

		// 7. Выполняем переименование основного файла
		if err := safefs.Rename(currentFull, targetFull); err != nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetRel,
				Status:      "error",
				Error:       "failed to rename file: " + err.Error(),
			})
			continue
		}

		// 8. Переименовываем сопутствующие файлы текстов (.lrc, .txt), если они существуют
		currentBase := strings.TrimSuffix(currentFull, filepath.Ext(currentFull))
		targetBase := strings.TrimSuffix(targetFull, filepath.Ext(targetFull))
		for _, lyricsExt := range []string{".lrc", ".txt"} {
			curLyr := currentBase + lyricsExt
			tarLyr := targetBase + lyricsExt
			if _, err := safefs.Stat(curLyr); err == nil {
				_ = safefs.Rename(curLyr, tarLyr)
			}
		}

		// 9. Обновляем кэш треков и запись в БД
		currentRel, err := h.musicSandbox.ToRelative(currentFull)
		if err != nil || currentRel == "" {
			currentRel = itemCurrent
		}
		oldHash := db.TrackHash(currentRel)
		newHash := db.TrackHash(targetRel)
		h.trackCache.Delete(oldHash)
		h.trackCache.Store(newHash, targetRel)

		if h.database != nil && h.database.Conn() != nil {
			_, _ = h.database.Conn().Exec("UPDATE tracks SET file_path = ?, updated_at = CURRENT_TIMESTAMP WHERE file_path = ?", targetRel, currentRel)
			_, _ = h.database.Conn().Exec("UPDATE audit_issues SET file_path = ? WHERE file_path = ?", targetRel, currentRel)
		}

		results = append(results, RenamePatternResult{
			CurrentPath: item.CurrentPath,
			NewPath:     targetRel,
			Status:      "ok",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}
