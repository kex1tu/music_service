package tageditor

import (
	"context"
	"errors"
	"fmt"
	"music-manager/internal/services/organizer"
	"os"
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

		rename := func() error {
			if currentFull != targetFull {
				if _, err := safefs.Lstat(targetFull); err == nil {
					return fmt.Errorf("target file already exists")
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			if err := safefs.Rename(currentFull, targetFull); err != nil {
				return fmt.Errorf("failed to rename file: %w", err)
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

			return nil
		}
		err = organizer.ApplyTrackChange(ctx, s.trackIndex, currentFull, targetFull, false, rename)
		if err == nil && s.trackIndex == nil && s.repo != nil {
			currentRel, _ := s.musicSandbox.ToRelative(currentFull)
			err = s.repo.UpdateTrackFilePath(ctx, currentRel, targetRel)
		}
		if err != nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetRel,
				Status:      "error",
				Error:       err.Error(),
			})
			continue
		}

		results = append(results, RenamePatternResult{
			CurrentPath: item.CurrentPath,
			NewPath:     targetRel,
			Status:      "ok",
		})
	}

	return results, nil
}
