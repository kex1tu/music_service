package organizer

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"music-manager/internal/id3"
)

// NavidromeFixReport содержит статистику выполнения нормализации библиотеки.
type NavidromeFixReport struct {
	DirectoriesFixed int `json:"directories_fixed"`
	FilesPermsFixed  int `json:"files_perms_fixed"`
	TagsNormalized   int `json:"tags_normalized"`
	TotalScanned     int `json:"total_scanned"`
}

// FixLibraryPermissions рекурсивно обходит медиатеку и гарантирует права 0755 для папок и 0644 для файлов.
func FixLibraryPermissions(musicRootDir string) (dirsFixed, filesFixed int, err error) {
	err = filepath.WalkDir(musicRootDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // пропускаем недоступные единичные элементы без падения
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}

		if d.IsDir() {
			if info.Mode().Perm() != 0755 {
				if err := os.Chmod(path, 0755); err == nil {
					dirsFixed++
				}
			}
		} else {
			if info.Mode().Perm() != 0644 {
				if err := os.Chmod(path, 0644); err == nil {
					filesFixed++
				}
			}
		}
		return nil
	})
	return dirsFixed, filesFixed, err
}

// NormalizeNavidromeLibrary проверяет всю медиатеку на наличие литеральных '\\' в артистах,
// устаревших фреймов TXXX и некорректных прав доступа, и нормализует их под стандарт Navidrome ID3v2.4.
func (s *Service) NormalizeNavidromeLibrary(ctx context.Context, onTrackNormalized func(relPath string)) (*NavidromeFixReport, error) {
	report := &NavidromeFixReport{}
	musicRoot := s.musicSandbox.Root()

	err := filepath.WalkDir(musicRoot, func(fullPath string, d fs.DirEntry, walkErr error) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if walkErr != nil {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		report.TotalScanned++

		// 1. Проверка и нормализация прав
		if d.IsDir() {
			if info.Mode().Perm() != 0755 {
				if err := os.Chmod(fullPath, 0755); err == nil {
					report.DirectoriesFixed++
				}
			}
			return nil
		}

		if info.Mode().Perm() != 0644 {
			if err := os.Chmod(fullPath, 0644); err == nil {
				report.FilesPermsFixed++
			}
		}

		// 2. Обработка только MP3-файлов
		if !strings.EqualFold(filepath.Ext(fullPath), ".mp3") {
			return nil
		}

		relPath, relErr := s.musicSandbox.ToRelative(fullPath)
		if relErr != nil {
			return nil
		}

		// Считываем сырые фреймы для проверки TXXX
		rawFrames, _ := id3.ReadRawFrames(fullPath)
		hasConflictingTXXX := false
		for _, f := range rawFrames {
			if f.ID == "TXXX" {
				desc := strings.ToLower(strings.TrimSpace(id3.ExtractTXXXDesc(f.Data)))
				if desc == "artists" || desc == "albumartists" || desc == "album artists" || desc == "artist" {
					hasConflictingTXXX = true
					break
				}
			}
		}

		tags, tErr := id3.ReadTags(fullPath, relPath)
		if tErr != nil || tags == nil {
			return nil
		}

		hasLiteralBackslash := strings.Contains(tags.Artist, "\\") || strings.Contains(tags.AlbumArtist, "\\")

		// Если есть слеши или конфликтные TXXX — переписываем теги через WriteTags
		if hasLiteralBackslash || hasConflictingTXXX {
			// WriteTags автоматически кодирует TPE1/TPE2 в ID3v2.4 \x00 и отфильтровывает TXXX:ARTISTS
			if wErr := id3.WriteTags(fullPath, *tags, nil); wErr == nil {
				report.TagsNormalized++
				if onTrackNormalized != nil {
					onTrackNormalized(relPath)
				}
			}
		}

		return nil
	})

	return report, err
}
