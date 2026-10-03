package organizer

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func moveFile(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		_ = os.Chmod(dest, 0644)
		return nil
	}
	// Fallback to streaming copy and delete across different mount points (EXDEV)
	if err := copyFile(src, dest); err != nil {
		return err
	}
	_ = os.Chmod(dest, 0644)
	return os.Remove(src)
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	_ = os.Chmod(dest, 0644)
	return out.Sync()
}

// RemoveIfEmpty удаляет папку, если в ней нет файлов.
func RemoveIfEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
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

		entries, err := os.ReadDir(curr)
		if err != nil || len(entries) > 0 {
			// В директории еще остались файлы/папки — останавливаем подъем
			break
		}

		// Удаляем пустую директорию
		if err := os.Remove(curr); err != nil {
			break
		}

		curr = filepath.Dir(curr)
	}
}

func removeIfEmpty(dir string) {
	RemoveIfEmpty(dir)
}
