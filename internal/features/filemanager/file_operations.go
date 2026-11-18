package filemanager

import (
	"context"
	"errors"
	"fmt"
	"music-manager/internal/core/sandbox"
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

	// The encoded form was already checked by ResolvePath. Prefer the exact
	// filesystem spelling if it exists, for example a directory named A%20B.
	if !isTmp && strings.Contains(trimmed, "%") {
		literal, literalErr := s.musicSandbox.Resolve(trimmed, false, sandbox.LiteralPath)
		if literalErr != nil {
			return nil, literalErr
		}
		if _, err := safefs.Lstat(literal); err == nil {
			fullPath = literal
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}

	st, err := safefs.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrFileNotFound
		}
		return nil, err
	}

	var deletedTracks int64
	ext := strings.ToLower(filepath.Ext(fullPath))
	remove := func() error {
		if st.IsDir() {
			return safefs.RemoveAll(fullPath)
		}
		if err := safefs.Remove(fullPath); err != nil {
			return err
		}
		if ext == ".mp3" && !isTmp {
			base := strings.TrimSuffix(relPath, filepath.Ext(relPath))
			for _, suffix := range []string{".lrc", ".txt", ".jpg"} {
				if companion, err := s.musicSandbox.Resolve(base+suffix, false, sandbox.LiteralPath); err == nil {
					_ = safefs.Remove(companion)
				}
			}
		}
		return nil
	}

	if !isTmp {
		relPath, err = s.musicSandbox.ToRelative(fullPath)
		if err != nil {
			return nil, err
		}
	}
	indexed := !isTmp && (st.IsDir() || ext == ".mp3")
	if indexed && s.libraryIndex != nil {
		deletedTracks, err = s.libraryIndex.DeletePath(ctx, fullPath, st.IsDir(), remove)
	} else if err = remove(); err == nil && !isTmp && s.repo != nil {
		switch {
		case st.IsDir():
			deletedTracks, err = s.repo.DeleteDirectoryRecords(ctx, relPath)
		case ext == ".mp3":
			deletedTracks, err = s.repo.DeleteTrackByPath(ctx, relPath)
		case ext == ".jpg" || ext == ".png":
			err = s.repo.UpdateAlbumCoverStatus(ctx, filepath.Dir(relPath), false)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("delete %q: %w", relPath, err)
	}

	response := &DeleteResponse{Success: true, DeletedTracks: int(deletedTracks), Path: relPath}
	if st.IsDir() {
		response.Message = "Каталог успешно удален"
	} else {
		s.cleanupEmptyParents(filepath.Dir(fullPath), isTmp)
		response.Message = "Файл успешно удален"
		response.DeletedPaths = []string{relPath}
	}
	return response, nil
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
	var renameErr error

	if s.organizer != nil {
		affectedFiles, renameErr = s.organizer.RenameFile(oldRel, newRel)
		if renameErr != nil && len(affectedFiles) == 0 {
			return nil, renameErr
		}
		if len(affectedFiles) > 0 && s.libraryIndex != nil {
			newRel = affectedFiles[0]
		}
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

	if s.libraryIndex == nil {
		if s.repo != nil {
			renameErr = s.repo.UpdateTrackPath(ctx, oldRel, newRel)
		}
		if renameErr == nil && s.scanner != nil {
			renameErr = s.scanner.IndexFile(ctx, newRel)
		}
	}

	s.trackCache.Delete(oldRel)
	s.trackCache.Store(newRel, newRel)

	res := &RenameResponse{
		Success:        renameErr == nil,
		OldPath:        oldRel,
		NewPath:        newRel,
		AffectedFiles:  affectedFiles,
		AffectedTracks: 1,
	}
	if renameErr != nil {
		res.Error = renameErr.Error()
	}
	return res, nil
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
