package tageditor

import (
	"context"
	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
)

// RenamePattern выполняет пакетное безопасное переименование файлов по шаблону с проверкой песочницы.
func (s *Service) RenamePattern(ctx context.Context, req RenamePatternRequest) ([]RenamePatternResult, error) {
	if s.musicSandbox == nil {
		return nil, ErrSandboxViolation
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
		currentFull, err := s.musicSandbox.Resolve(itemCurrent, true)
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
		targetFull, err := s.musicSandbox.Resolve(filepath.Join(filepath.Dir(itemCurrent), itemNew), true)
		if err != nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetProposedRel,
				Status:      "error",
				Error:       err.Error(),
			})
			continue
		}

		targetRel, err := s.musicSandbox.ToRelative(targetFull)
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

		// 9. Обновляем пути в БД через репозиторий
		currentRel, err := s.musicSandbox.ToRelative(currentFull)
		if err != nil || currentRel == "" {
			currentRel = itemCurrent
		}
		if s.repo != nil {
			_ = s.repo.UpdateTrackFilePath(ctx, currentRel, targetRel)
		}

		results = append(results, RenamePatternResult{
			CurrentPath: item.CurrentPath,
			NewPath:     targetRel,
			Status:      "ok",
		})
	}

	return results, nil
}
