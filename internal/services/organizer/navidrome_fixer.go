package organizer

import (
	"context"
	"io/fs"

	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

// NavidromeFixReport содержит статистику выполнения нормализации библиотеки.
type NavidromeFixReport struct {
	DirectoriesFixed int `json:"directories_fixed"`
	FilesPermsFixed  int `json:"files_perms_fixed"`
	TagsNormalized   int `json:"tags_normalized"`
	SinglesFixed     int `json:"singles_fixed"`
	TotalScanned     int `json:"total_scanned"`
}

// FixLibraryPermissions рекурсивно обходит медиатеку и гарантирует права 0755 для папок и 0644 для файлов.
func FixLibraryPermissions(musicRootDir string) (dirsFixed, filesFixed int, err error) {
	err = safefs.WalkDir(musicRootDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // пропускаем недоступные единичные элементы без падения
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}

		if d.IsDir() {
			if info.Mode().Perm() != 0755 {
				if err := safefs.Chmod(path, 0755); err == nil {
					dirsFixed++
				}
			}
		} else {
			if info.Mode().Perm() != 0644 {
				if err := safefs.Chmod(path, 0644); err == nil {
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

	err := safefs.WalkDir(musicRoot, func(fullPath string, d fs.DirEntry, walkErr error) error {
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
				if err := safefs.Chmod(fullPath, 0755); err == nil {
					report.DirectoriesFixed++
				}
			}
			return nil
		}

		if info.Mode().Perm() != 0644 {
			if err := safefs.Chmod(fullPath, 0644); err == nil {
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

		rawTitle := strings.TrimSpace(tags.Title)
		if rawTitle == "" {
			rawTitle = id3.CleanTitle("", filepath.Base(fullPath))
			if rawTitle != "" {
				tags.Title = rawTitle
			}
		}

		rawArtist := strings.TrimSpace(tags.Artist)
		rawAlbum := strings.TrimSpace(tags.Album)
		rawAlbumArtist := strings.TrimSpace(tags.AlbumArtist)
		lowerAlbum := strings.ToLower(rawAlbum)

		isSingle := rawAlbum == "" ||
			lowerAlbum == "single" ||
			lowerAlbum == "singles" ||
			lowerAlbum == "unknown album" ||
			lowerAlbum == "." ||
			strings.HasPrefix(lowerAlbum, "single - ") ||
			strings.HasPrefix(lowerAlbum, "singles - ") ||
			(rawTitle != "" && strings.EqualFold(rawAlbum, rawTitle))

		hasLiteralBackslash := strings.Contains(tags.Artist, "\\") || strings.Contains(tags.AlbumArtist, "\\")
		needsRewrite := hasLiteralBackslash || hasConflictingTXXX
		isSingleFixed := false

		// 3. Извлечение нераспарсенных соавторов из названия трека (feat. / ft. / with / при уч.)
		if rawTitle != "" {
			cleanTitle, feats := resolver.ExtractFeatFromText(rawTitle)
			if len(feats) > 0 && cleanTitle != "" {
				tags.Title = cleanTitle
				rawTitle = cleanTitle

				currentArts := id3.ParseMultiValue(tags.Artist)
				for _, f := range feats {
					fTrimmed := strings.TrimSpace(f)
					if fTrimmed != "" {
						found := false
						for _, a := range currentArts {
							if strings.EqualFold(a, fTrimmed) {
								found = true
								break
							}
						}
						if !found {
							currentArts = append(currentArts, fTrimmed)
						}
					}
				}
				tags.Artist = id3.FormatMultiValue(currentArts)
				rawArtist = tags.Artist
				needsRewrite = true
				if isSingle {
					isSingleFixed = true
				}
			}
		}

		// 4. Нормализация метаданных сингла: Album = Title, AlbumArtist = Artist
		if isSingle && rawTitle != "" {
			if tags.Album != rawTitle {
				tags.Album = rawTitle
				needsRewrite = true
				isSingleFixed = true
			}
			if rawArtist != "" && (rawAlbumArtist == "" || rawAlbumArtist != rawArtist || strings.EqualFold(rawAlbumArtist, "unknown artist")) {
				tags.AlbumArtist = rawArtist
				needsRewrite = true
				isSingleFixed = true
			}
		}

		// Если есть слеши, конфликтные TXXX или исправлены метаданные сингла — переписываем теги
		if needsRewrite {
			// WriteTags автоматически кодирует TPE1/TPE2 в ID3v2.4 \x00 и отфильтровывает TXXX:ARTISTS
			if wErr := id3.WriteTags(fullPath, *tags, nil); wErr == nil {
				report.TagsNormalized++
				if isSingleFixed {
					report.SinglesFixed++
				}
				if onTrackNormalized != nil {
					onTrackNormalized(relPath)
				}
			}
		}

		return nil
	})

	return report, err
}
