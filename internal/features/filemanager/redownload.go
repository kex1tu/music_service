package filemanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

// Redownload stages a fresh download while preserving the existing library files.
func (s *Service) Redownload(ctx context.Context, req RedownloadRequest) (*RedownloadResponse, error) {
	if s.redownloadQueue != nil {
		return s.prepareRedownload(ctx, req)
	}
	relPath := strings.TrimSpace(req.Path)
	if relPath == "" || relPath == "." {
		return nil, ErrInvalidPath
	}

	// 1. Проверяем безопасность пути через MusicSandbox
	fullPath, err := s.musicSandbox.Resolve(relPath, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSandboxViolation, err)
	}

	// 2. Определяем тип (трек или директория)
	isDir := false
	if req.Type == "album" || req.Type == "directory" {
		isDir = true
	} else if req.Type == "track" || req.Type == "file" {
		isDir = false
	} else {
		// Автоопределение по файловой системе
		if fi, statErr := safefs.Stat(fullPath); statErr == nil {
			isDir = fi.IsDir()
		} else {
			ext := strings.ToLower(filepath.Ext(relPath))
			if ext == ".mp3" || ext == ".flac" || ext == ".m4a" || ext == ".ogg" {
				isDir = false
			} else {
				isDir = true
			}
		}
	}

	var sourceURL string
	resType := "track"

	if isDir {
		resType = "album"
		// Ищем метаданные альбома в SQLite
		var artist, album, deezerID string
		if s.repo != nil {
			artist, album, deezerID, _ = s.repo.GetAlbumDetailsForRedownload(ctx, relPath)
		}
		// Если в БД не найдено, пробуем прочитать теги аудиофайла внутри каталога
		if artist == "" && album == "" {
			_ = safefs.WalkDir(fullPath, func(path string, d os.DirEntry, wErr error) error {
				if wErr != nil || d.IsDir() {
					return nil
				}
				ext := strings.ToLower(filepath.Ext(d.Name()))
				if ext == ".mp3" || ext == ".flac" {
					if tags, tErr := id3.ReadTags(path, ""); tErr == nil {
						artist = tags.Artist
						if artist == "" {
							artist = tags.AlbumArtist
						}
						album = tags.Album
						return filepath.SkipAll
					}
				}
				return nil
			})
		}
		if album == "" {
			album = filepath.Base(relPath)
		}

		if deezerID != "" {
			sourceURL = fmt.Sprintf("https://www.deezer.com/album/%s", deezerID)
		} else if s.sourceFinder != nil {
			u, fErr := s.sourceFinder.FindAlbumURL(ctx, artist, album)
			if fErr == nil && u != "" {
				sourceURL = u
			}
		}

		if sourceURL == "" {
			return nil, fmt.Errorf("не удалось определить источник для альбома %q (%s)", album, artist)
		}
	} else {
		resType = "track"
		// Ищем метаданные трека
		var artist, title, album, deezerID, pinnedURL string
		var duration int
		var albumDeezerID string

		if s.repo != nil {
			artist, title, album, deezerID, pinnedURL, duration, albumDeezerID, _ = s.repo.GetTrackDetailsForRedownload(ctx, relPath)
		}

		// Если в БД нет данных, пробуем прочитать из файла на диске
		if title == "" {
			if tags, tErr := id3.ReadTags(fullPath, relPath); tErr == nil {
				title = tags.Title
				if artist == "" {
					artist = tags.Artist
				}
				if album == "" {
					album = tags.Album
				}
				duration = int(tags.Duration)
			}
		}
		if title == "" {
			base := filepath.Base(relPath)
			title = strings.TrimSuffix(base, filepath.Ext(base))
		}

		// Приоритеты разрешения источника
		if pinnedURL != "" && resolver.IsAllowedURL(pinnedURL) {
			sourceURL = pinnedURL
		} else if deezerID != "" {
			sourceURL = fmt.Sprintf("https://www.deezer.com/track/%s", deezerID)
		} else if s.sourceFinder != nil {
			u, fErr := s.sourceFinder.FindTrackURL(ctx, artist, title, album, duration)
			if fErr == nil && u != "" {
				sourceURL = u
			}
		} else if albumDeezerID != "" {
			sourceURL = fmt.Sprintf("https://www.deezer.com/album/%s", albumDeezerID)
		}

		if sourceURL == "" {
			return nil, fmt.Errorf("не удалось определить источник для трека %q (%s)", title, artist)
		}
	}

	// Existing files remain available until the staged download is accepted.
	deleted := false

	// 4. Ставим задачу в очередь загрузчика
	jobID := ""
	if s.jobCreator != nil {
		id, cErr := s.jobCreator.CreateJob(ctx, sourceURL, "auto")
		if cErr != nil {
			return nil, fmt.Errorf("ошибка постановки задачи в очередь загрузчика: %w", cErr)
		}
		jobID = id
	} else {
		return nil, fmt.Errorf("очередь загрузчика недоступна")
	}

	msg := "Трек успешно поставлен в очередь повторной загрузки"
	if resType == "album" {
		msg = "Альбом успешно поставлен в очередь повторной загрузки"
	}

	return &RedownloadResponse{
		Success:   true,
		Message:   msg,
		JobID:     jobID,
		SourceURL: sourceURL,
		Deleted:   deleted,
		Type:      resType,
	}, nil
}
