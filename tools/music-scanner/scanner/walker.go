package scanner

import (
	"io/fs"
	"path/filepath"
	"strings"

	"music-scanner/db"
)

// WalkDirectoryIncremental рекурсивно обходит rootDir и отправляет на обработку только
// новые или измененные файлы (сравнивая mtime и size со снимком existingMtimes).
// Также собирает множество всех существующих на диске relPaths для удаления удаленных файлов.
func WalkDirectoryIncremental(
	rootDir string,
	existingMtimes map[string]db.FileMtimeEntry,
	out chan<- string,
	existingRelPaths map[string]bool,
) error {
	defer close(out)

	return filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if !d.IsDir() {
			ext := strings.ToLower(filepath.Ext(name))
			if ext == ".mp3" {
				relPath, err := filepath.Rel(rootDir, path)
				if err != nil {
					relPath = path
				}
				relPath = filepath.ToSlash(relPath)
				existingRelPaths[relPath] = true

				info, err := d.Info()
				if err != nil {
					out <- path
					return nil
				}

				if entry, exists := existingMtimes[relPath]; exists {
					mtime := float64(info.ModTime().UnixNano()) / 1e9
					// Если размер и время изменения совпадают — файл не изменился
					diff := mtime - entry.Mtime
					if diff < 0 {
						diff = -diff
					}
					if entry.Size == info.Size() && diff < 0.001 {
						return nil // Пропускаем чтение тегов
					}
				}

				out <- path
			}
		}

		return nil
	})
}
