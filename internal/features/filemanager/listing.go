package filemanager

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/id3"
)

// ListFiles возвращает список файлов и директорий для навигатора медиатеки.
func (s *Service) ListFiles(ctx context.Context, relPath string, recursive bool) (*DirectoryListingResponse, error) {
	fullPath, isTmp, err := s.ResolvePath(relPath)
	if err != nil {
		return nil, err
	}

	st, err := safefs.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrFileNotFound
		}
		return nil, err
	}

	cleanRel := filepath.Clean(relPath)
	if cleanRel == "." {
		cleanRel = ""
	}

	if !st.IsDir() {
		// Одиночный файл
		item := s.buildFileItem(fullPath, cleanRel, st, isTmp)
		return &DirectoryListingResponse{
			CurrentPath:      cleanRel,
			Breadcrumbs:      []BreadcrumbItem{{Name: "Корень", Path: ""}},
			Items:            []FileItem{item},
			TotalDirectories: 0,
			TotalFiles:       1,
		}, nil
	}

	// Build breadcrumbs
	var breadcrumbs []BreadcrumbItem
	breadcrumbs = append(breadcrumbs, BreadcrumbItem{Name: "Корень", Path: ""})
	if cleanRel != "" {
		parts := strings.Split(cleanRel, string(filepath.Separator))
		accum := ""
		for _, p := range parts {
			if p == "" || p == "." {
				continue
			}
			accum = filepath.Join(accum, p)
			breadcrumbs = append(breadcrumbs, BreadcrumbItem{Name: p, Path: filepath.ToSlash(accum)})
		}
	}

	var parentPath *string
	if cleanRel != "" {
		parent := filepath.Dir(cleanRel)
		if parent == "." {
			parent = ""
		}
		parentPath = &parent
	}

	var metadataCache map[string]FileItem
	if s.repo != nil && !isTmp {
		metadataCache, _ = s.repo.GetTracksMetadataCache(ctx)
	}

	var items []FileItem
	var totalDirs, totalFiles int

	if recursive {
		const maxDepth = 20
		maxTracks := 100000

		err = safefs.WalkDir(fullPath, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}

			var relToSandbox string
			if isTmp && s.tmpSandbox != nil {
				relToSandbox, err = s.tmpSandbox.ToRelative(path)
			} else if s.musicSandbox != nil {
				relToSandbox, err = s.musicSandbox.ToRelative(path)
			}
			if err != nil {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			name := d.Name()
			if d.IsDir() {
				if path != fullPath {
					if strings.HasPrefix(name, ".") || name == "tmp" || name == "tmp_downloads" {
						return filepath.SkipDir
					}
					relFromStart, rErr := filepath.Rel(fullPath, path)
					if rErr == nil {
						parts := strings.Split(filepath.ToSlash(relFromStart), "/")
						if len(parts) > maxDepth {
							return filepath.SkipDir
						}
					}
				}
				return nil
			}

			if strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".mp3") {
				return nil
			}

			if len(items) >= maxTracks {
				return filepath.SkipAll
			}

			info, iErr := d.Info()
			if iErr != nil {
				return nil
			}

			cleanItemRel := filepath.ToSlash(relToSandbox)
			if metadataCache != nil {
				if cached, ok := metadataCache[cleanItemRel]; ok {
					cached.Name = name
					cached.Path = cleanItemRel
					cached.Type = "file"
					cached.IsDir = false
					sz := info.Size()
					cached.Size = &sz
					cached.Extension = strings.ToLower(filepath.Ext(name))
					cached.ModTime = info.ModTime().Format(time.RFC3339)
					tHash := TrackHash(cleanItemRel)
					cached.ID = &tHash
					cached.TrackHash = tHash
					sURL := "/api/tracks/" + tHash + "/stream"
					cached.StreamURL = &sURL
					items = append(items, cached)
					return nil
				}
			}

			items = append(items, s.buildFileItem(path, cleanItemRel, info, isTmp))
			return nil
		})
		if err != nil {
			return nil, err
		}

		totalFiles = len(items)
	} else {
		entries, rErr := safefs.ReadDir(fullPath)
		if rErr != nil {
			return nil, rErr
		}

		var dirTrackCounts map[string]int
		if metadataCache != nil {
			dirTrackCounts = make(map[string]int, len(metadataCache)/2)
			for p := range metadataCache {
				dir := filepath.Dir(p)
				for dir != "." && dir != "" && dir != "/" {
					dirTrackCounts[filepath.ToSlash(dir)]++
					dir = filepath.Dir(dir)
				}
			}
		}

		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") || name == "tmp" || name == "tmp_downloads" {
				continue
			}

			itemPath := filepath.Join(fullPath, name)
			var itemRel string
			if cleanRel == "" {
				itemRel = name
			} else {
				itemRel = filepath.ToSlash(filepath.Join(cleanRel, name))
			}

			if entry.IsDir() {
				totalDirs++
				var tCount int
				var countFound bool
				if dirTrackCounts != nil {
					if cnt, ok := dirTrackCounts[itemRel]; ok {
						tCount = cnt
						countFound = true
					}
				}
				if !countFound {
					subEntries, _ := safefs.ReadDir(itemPath)
					for _, sub := range subEntries {
						if !sub.IsDir() && strings.EqualFold(filepath.Ext(sub.Name()), ".mp3") {
							tCount++
						}
					}
				}
				items = append(items, FileItem{
					Name:         name,
					Path:         itemRel,
					Type:         "directory",
					IsDir:        true,
					TrackCount:   &tCount,
					LyricsStatus: "none",
				})
			} else {
				totalFiles++
				info, iErr := entry.Info()
				if iErr != nil {
					continue
				}

				if metadataCache != nil {
					if cached, ok := metadataCache[itemRel]; ok {
						cached.Name = name
						cached.Path = itemRel
						cached.Type = "file"
						cached.IsDir = false
						sz := info.Size()
						cached.Size = &sz
						cached.Extension = strings.ToLower(filepath.Ext(name))
						cached.ModTime = info.ModTime().Format(time.RFC3339)
						tHash := TrackHash(itemRel)
						cached.ID = &tHash
						cached.TrackHash = tHash
						sURL := "/api/tracks/" + tHash + "/stream"
						cached.StreamURL = &sURL
						items = append(items, cached)
						continue
					}
				}

				items = append(items, s.buildFileItem(itemPath, itemRel, info, isTmp))
			}
		}
	}

	return &DirectoryListingResponse{
		CurrentPath:      cleanRel,
		ParentPath:       parentPath,
		Breadcrumbs:      breadcrumbs,
		Items:            items,
		TotalDirectories: totalDirs,
		TotalFiles:       totalFiles,
	}, nil
}

func (s *Service) buildFileItem(absPath, relPath string, info os.FileInfo, isTmp bool) FileItem {
	sz := info.Size()
	ext := strings.ToLower(filepath.Ext(absPath))
	cleanRel := filepath.ToSlash(relPath)

	item := FileItem{
		Name:      info.Name(),
		Path:      cleanRel,
		Type:      "file",
		IsDir:     info.IsDir(),
		Size:      &sz,
		ModTime:   info.ModTime().Format(time.RFC3339),
		Extension: ext,
	}

	if item.IsDir {
		item.Type = "directory"
		return item
	}

	if ext != ".mp3" {
		item.LyricsStatus = "none"
		return item
	}

	tHash := TrackHash(cleanRel)
	item.ID = &tHash
	item.TrackHash = tHash
	sURL := "/api/tracks/" + tHash + "/stream"
	item.StreamURL = &sURL
	s.trackCache.Store(tHash, cleanRel)

	tags, err := id3.ReadTags(absPath, cleanRel)
	if err == nil && tags != nil {
		item.Title = tags.Title
		item.Artist = tags.Artist
		item.Album = tags.Album
		item.AlbumArtist = tags.AlbumArtist
		if tags.Year > 0 {
			y := tags.Year
			item.Year = &y
		}
		item.Genre = tags.Genre
		if tags.TrackNumber > 0 {
			tn := tags.TrackNumber
			item.TrackNumber = &tn
		}
		if tags.TrackTotal > 0 {
			tt := tags.TrackTotal
			item.TrackTotal = &tt
		}
		if tags.DiscNumber > 0 {
			dn := tags.DiscNumber
			item.DiscNumber = &dn
		}
		if tags.DiscTotal > 0 {
			dt := tags.DiscTotal
			item.DiscTotal = &dt
		}
		dur := int(tags.Duration)
		item.Duration = &dur
		item.Bitrate = tags.Bitrate
		item.HasCover = tags.HasCover
		lyr := tags.LyricsStatus
		if lyr == "synced" {
			lyr = "lrc"
		} else if lyr == "plain" {
			lyr = "txt"
		}
		item.LyricsStatus = lyr
	} else {
		item.Title = strings.TrimSuffix(info.Name(), ext)
		item.LyricsStatus = "none"
	}

	// Проверка наличия внешних текстов рядом с треком (.lrc или .txt)
	base := strings.TrimSuffix(absPath, ext)
	if fileExists(base + ".lrc") {
		item.HasLyrics = true
		item.LyricsStatus = "lrc"
	} else if fileExists(base + ".txt") {
		item.HasLyrics = true
		item.LyricsStatus = "txt"
	}

	return item
}

// ListLibraryTracks возвращает пагинированный список треков библиотеки.
func (s *Service) ListLibraryTracks(ctx context.Context, query string, limit, offset int) ([]LibraryTrackItem, int, error) {
	if s.repo == nil {
		return nil, 0, nil
	}
	return s.repo.ListLibraryTracks(ctx, query, limit, offset)
}

// SearchLibrary выполняет полнотекстовый поиск по медиатеке.
func (s *Service) SearchLibrary(ctx context.Context, q string, limit int) (*SearchLibraryResult, error) {
	if s.repo == nil {
		return nil, nil
	}
	return s.repo.SearchLibrary(ctx, q, limit)
}
