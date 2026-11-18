package organizer

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"music-manager/internal/core/safefs"
)

var (
	albumLocksMu sync.Mutex
	albumLocks   = make(map[string]*sync.Mutex)
)

func getAlbumLock(albumDir string) *sync.Mutex {
	albumLocksMu.Lock()
	defer albumLocksMu.Unlock()
	lock, ok := albumLocks[albumDir]
	if !ok {
		lock = &sync.Mutex{}
		albumLocks[albumDir] = lock
	}
	return lock
}

func fileExists(path string) bool {
	st, err := safefs.Stat(path)
	return err == nil && !st.IsDir()
}

func moveFile(src, dest string) error {
	if err := safefs.Rename(src, dest); err == nil {
		_ = safefs.Chmod(dest, 0644)
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}

	in, err := safefs.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := safefs.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if err = errors.Join(err, out.Close()); err == nil {
		err = safefs.Remove(src)
	}
	if err != nil {
		return errors.Join(err, safefs.Remove(dest))
	}
	return nil
}

func copyFile(src, dest string) error {
	in, err := safefs.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := safefs.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	_ = safefs.Chmod(dest, 0644)
	return out.Sync()
}

// RemoveIfEmpty удаляет папку, если в ней нет файлов.
func RemoveIfEmpty(dir string) {
	entries, err := safefs.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		_ = safefs.Remove(dir)
	}
}

// CleanEmptyParents рекурсивно удаляет пустые директории вверх по дереву до rootDir (не включая сам rootDir).
func CleanEmptyParents(startDir, rootDir string) {
	cleanStart := filepath.Clean(startDir)
	cleanRoot := filepath.Clean(rootDir)

	curr := cleanStart
	for curr != "" && curr != "." && curr != "/" && curr != cleanRoot {
		// Проверяем, что curr находится внутри rootDir
		rel, err := filepath.Rel(cleanRoot, curr)
		if err != nil || strings.HasPrefix(rel, "..") {
			break
		}

		entries, err := safefs.ReadDir(curr)
		if err != nil || len(entries) > 0 {
			// В директории еще остались файлы/папки — останавливаем подъем
			break
		}

		// Удаляем пустую директорию
		if err := safefs.Remove(curr); err != nil {
			break
		}

		curr = filepath.Dir(curr)
	}
}
