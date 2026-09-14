package scanner

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// WalkDirectory рекурсивно обходит rootDir и отправляет пути к файлам .mp3 в out.
// Игнорирует скрытые директории и скрытые файлы (начинающиеся с точки).
func WalkDirectory(rootDir string, out chan<- string) error {
	defer close(out)

	return filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Пропускаем ошибки доступа к отдельным директориям/файлам
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
				out <- path
			}
		}

		return nil
	})
}
