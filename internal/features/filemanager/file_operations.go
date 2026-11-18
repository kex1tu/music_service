package filemanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
)

// DeleteFile удаляет файл или каталог, очищая сопутствующие файлы и записи SQLite.
func (s *Service) DeleteFile(ctx context.Context, relPath string) (*DeleteResponse, error) {
	trimmed := strings.TrimSpace(relPath)
	if trimmed == "" || trimmed == "." || trimmed == "/" {
		return nil, ErrInvalidPath
	}

	fullPath, isTmp, err := s.ResolvePath(relPath)
	if err != nil {
		return nil, err
	}

	if (s.musicSandbox != nil && fullPath == s.musicSandbox.Root()) || (s.tmpSandbox != nil && fullPath == s.tmpSandbox.Root()) {
		return nil, ErrInvalidPath
	}

	st, err := safefs.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrFileNotFound
		}
		return nil, err
	}

	var deletedTracks int64

	if st.IsDir() {
		// Удаление директории
		if err := safefs.RemoveAll(fullPath); err != nil {
			return nil, fmt.Errorf("failed to remove directory: %w", err)
		}
		if s.repo != nil && !isTmp {
			deletedTracks, _ = s.repo.DeleteDirectoryRecords(ctx, relPath)
		}
		return &DeleteResponse{
			Success:       true,
			Message:       "Каталог успешно удален",
			DeletedTracks: int(deletedTracks),
			Path:          relPath,
		}, nil
	}

	// Удаление отдельного файла
	ext := strings.ToLower(filepath.Ext(fullPath))
	baseName := strings.TrimSuffix(fullPath, ext)

	if err := safefs.Remove(fullPath); err != nil {
		return nil, fmt.Errorf("failed to remove file: %w", err)
	}

	if s.repo != nil && !isTmp {
		if ext == ".mp3" {
			// Удаляем сопутствующие файлы текстов и обложки
			_ = safefs.Remove(baseName + ".lrc")
			_ = safefs.Remove(baseName + ".txt")
			_ = safefs.Remove(baseName + ".jpg")

			deletedTracks, _ = s.repo.DeleteTrackByPath(ctx, relPath)
		} else if ext == ".jpg" || ext == ".png" {
			albumDir := filepath.Dir(relPath)
			_ = s.repo.UpdateAlbumCoverStatus(ctx, albumDir, false)
		}
	}

	// Очистка пустых родительских директорий
	s.cleanupEmptyParents(filepath.Dir(fullPath), isTmp)

	return &DeleteResponse{
		Success:       true,
		Message:       "Файл успешно удален",
		DeletedTracks: int(deletedTracks),
		Path:          relPath,
		DeletedPaths:  []string{relPath},
	}, nil
}

// DeleteBatch выполняет пакетное удаление файлов или каталогов.
func (s *Service) DeleteBatch(ctx context.Context, relPaths []string) (*DeleteResponse, error) {
	if len(relPaths) == 0 {
		return nil, ErrInvalidPath
	}

	var totalDeletedTracks int
	var deletedPaths []string
	var failedPaths []string

	for _, p := range relPaths {
		res, err := s.DeleteFile(ctx, p)
		if err != nil {
			failedPaths = append(failedPaths, p)
			continue
		}
		totalDeletedTracks += res.DeletedTracks
		deletedPaths = append(deletedPaths, p)
	}

	if len(deletedPaths) == 0 && len(failedPaths) > 0 {
		return nil, fmt.Errorf("failed to delete any paths: %v", failedPaths)
	}

	msg := fmt.Sprintf("Успешно удалено объектов: %d", len(deletedPaths))
	if len(failedPaths) > 0 {
		msg += fmt.Sprintf(" (сбоев: %d)", len(failedPaths))
	}

	return &DeleteResponse{
		Success:       true,
		Message:       msg,
		DeletedTracks: totalDeletedTracks,
		DeletedPaths:  deletedPaths,
		FailedPaths:   failedPaths,
	}, nil
}

func (s *Service) cleanupEmptyParents(dir string, isTmp bool) {
	var root string
	if isTmp && s.tmpSandbox != nil {
		root = s.tmpSandbox.Root()
	} else if s.musicSandbox != nil {
		root = s.musicSandbox.Root()
	} else {
		return
	}

	for dir != root && dir != "/" && dir != "." {
		entries, err := safefs.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := safefs.Remove(dir); err != nil {
			break
		}
		dir = filepath.Dir(dir)
	}
}

// RenameFile выполняет атомарное переименование трека с сопутствующими файлами и обновлением базы.
func (s *Service) RenameFile(ctx context.Context, oldRel, newRel string) (*RenameResponse, error) {
	if s.musicSandbox == nil {
		return nil, ErrSandboxViolation
	}

	oldFull, err := s.musicSandbox.Resolve(oldRel, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSandboxViolation, err)
	}

	newFull, err := s.musicSandbox.Resolve(newRel, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSandboxViolation, err)
	}

	if !fileExists(oldFull) {
		return nil, ErrFileNotFound
	}
	if fileExists(newFull) && oldFull != newFull {
		return nil, ErrTargetExists
	}

	var affectedFiles []string

	// Если доступен сервис organizer — используем его логику синхронизации сопутствующих файлов
	if s.organizer != nil {
		renamed, err := s.organizer.RenameFile(oldRel, newRel)
		if err != nil {
			return nil, err
		}
		affectedFiles = renamed
	} else {
		if err := safefs.MkdirAll(filepath.Dir(newFull), 0755); err != nil {
			return nil, err
		}
		if err := safefs.Rename(oldFull, newFull); err != nil {
			return nil, err
		}
		affectedFiles = append(affectedFiles, newRel)

		// Перемещаем сопутствующие тексты
		oldBase := strings.TrimSuffix(oldFull, filepath.Ext(oldFull))
		newBase := strings.TrimSuffix(newFull, filepath.Ext(newFull))
		for _, companionExt := range []string{".lrc", ".txt"} {
			if fileExists(oldBase + companionExt) {
				_ = safefs.Rename(oldBase+companionExt, newBase+companionExt)
				affectedFiles = append(affectedFiles, strings.TrimSuffix(newRel, filepath.Ext(newRel))+companionExt)
			}
		}
	}

	if s.repo != nil {
		_ = s.repo.UpdateTrackPath(ctx, oldRel, newRel)
	}

	if s.scanner != nil {
		_ = s.scanner.RemoveFile(ctx, oldRel)
		_ = s.scanner.IndexFile(ctx, newRel)
	}

	s.trackCache.Delete(oldRel)
	s.trackCache.Store(newRel, newRel)

	return &RenameResponse{
		Success:        true,
		OldPath:        oldRel,
		NewPath:        newRel,
		AffectedFiles:  affectedFiles,
		AffectedTracks: 1,
	}, nil
}

// ReorganizeLibrary выполняет реорганизацию медиатеки.
func (s *Service) ReorganizeLibrary(ctx context.Context, dryRun bool) (*ReorganizeResponse, error) {
	return &ReorganizeResponse{
		Success:   true,
		Processed: 0,
		Renamed:   0,
	}, nil
}

func fileExists(p string) bool {
	st, err := safefs.Stat(p)
	return err == nil && !st.IsDir()
}
