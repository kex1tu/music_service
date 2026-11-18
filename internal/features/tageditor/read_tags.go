package tageditor

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"music-manager/internal/core/safefs"
	"music-manager/internal/id3"
)

// GetTags считывает теги для одного аудиофайла либо собирает сводку по всей директории (альбому).
func (s *Service) GetTags(ctx context.Context, relPath string, asAlbum ...bool) (*GetTagsResponse, error) {
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

	forceAlbum := len(asAlbum) > 0 && asAlbum[0]

	// Режим директории: сбор треков и метаданных релиза
	if st.IsDir() || forceAlbum {
		return s.readDirectoryTags(ctx, fullPath, relPath, isTmp)
	}

	// Режим одиночного файла
	return s.readSingleFileTags(ctx, fullPath, relPath)
}

func (s *Service) readSingleFileTags(ctx context.Context, fullPath, relPath string) (*GetTagsResponse, error) {
	tags, err := id3.ReadTags(fullPath, relPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read id3 tags: %w", err)
	}

	var year *int
	if tags.Year > 0 {
		y := tags.Year
		year = &y
	}

	var coverPath *string
	trackBase := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))
	trackRelBase := strings.TrimSuffix(relPath, filepath.Ext(relPath))

	for _, candExt := range []string{".jpg", ".png", ".jpeg", ".webp"} {
		if _, err := safefs.Stat(trackBase + candExt); err == nil {
			cRel := trackRelBase + candExt
			coverPath = &cRel
			break
		}
	}
	if coverPath == nil {
		dir := filepath.Dir(fullPath)
		for _, cand := range []string{"cover.jpg", "cover.png", "folder.jpg", "folder.png", "cover.jpeg"} {
			if _, err := safefs.Stat(filepath.Join(dir, cand)); err == nil {
				cRel := filepath.Join(filepath.Dir(relPath), cand)
				coverPath = &cRel
				break
			}
		}
	}
	hasCover := tags.HasCover || coverPath != nil

	albumTitle := tags.Album
	if albumTitle == "" {
		albumTitle = tags.Title
	}
	albumArtist := tags.AlbumArtist
	if albumArtist == "" {
		albumArtist = tags.Artist
	}

	return &GetTagsResponse{
		Album: AlbumSummaryDTO{
			AlbumArtist: albumArtist,
			Album:       albumTitle,
			Year:        year,
			Genre:       tags.Genre,
			DiscTotal:   tags.DiscTotal,
			TrackTotal:  tags.TrackTotal,
		},
		Tracks:       []TrackTags{*tags},
		SourceDir:    filepath.Dir(relPath),
		CoverPath:    coverPath,
		HasCover:     hasCover,
		Title:        tags.Title,
		Artist:       tags.Artist,
		AlbumArtist:  tags.AlbumArtist,
		Path:         tags.Path,
		TrackNumber:  tags.TrackNumber,
		TrackTotal:   tags.TrackTotal,
		DiscNumber:   tags.DiscNumber,
		DiscTotal:    tags.DiscTotal,
		Year:         tags.Year,
		Genre:        tags.Genre,
		Duration:     tags.Duration,
		Bitrate:      tags.Bitrate,
		LyricsStatus: tags.LyricsStatus,
	}, nil
}

func (s *Service) readDirectoryTags(ctx context.Context, fullPath, relPath string, isTmp bool) (*GetTagsResponse, error) {
	var tracks []TrackTags
	var coverPath *string
	hasCover := false

	// Проверка наличия файла обложки в каталоге
	for _, cand := range []string{"cover.jpg", "cover.png", "folder.jpg", "folder.png", "cover.jpeg"} {
		imgPath := filepath.Join(fullPath, cand)
		if _, err := safefs.Stat(imgPath); err == nil {
			cRel := filepath.Join(relPath, cand)
			coverPath = &cRel
			hasCover = true
			break
		}
	}

	err := safefs.WalkDir(fullPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "tmp" || name == "tmp_downloads" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".mp3") {
			return nil
		}

		var fileRel string
		if isTmp && s.tmpSandbox != nil {
			if r, err := filepath.Rel(s.tmpSandbox.Root(), path); err == nil {
				fileRel = "tmp_downloads/" + filepath.ToSlash(r)
			}
		} else if s.musicSandbox != nil {
			fileRel, _ = s.musicSandbox.ToRelative(path)
		}
		if fileRel == "" {
			fileRel = path
		}

		t, err := id3.ReadTags(path, fileRel)
		if err == nil && t != nil {
			if t.HasCover {
				hasCover = true
			}
			tracks = append(tracks, *t)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk directory tracks: %w", err)
	}

	// Сортировка треков: DiscNumber -> TrackNumber -> Path
	sort.SliceStable(tracks, func(i, j int) bool {
		if tracks[i].DiscNumber != tracks[j].DiscNumber {
			return tracks[i].DiscNumber < tracks[j].DiscNumber
		}
		if tracks[i].TrackNumber != tracks[j].TrackNumber {
			return tracks[i].TrackNumber < tracks[j].TrackNumber
		}
		return tracks[i].Path < tracks[j].Path
	})

	albumArtist := ""
	albumTitle := filepath.Base(relPath)
	if albumTitle == "." || albumTitle == "/" {
		albumTitle = ""
	}
	var year *int
	genre := ""
	discTotal := 1
	trackTotal := len(tracks)

	for _, t := range tracks {
		if albumTitle == "" || albumTitle == filepath.Base(relPath) {
			if t.Album != "" {
				albumTitle = t.Album
			}
		}
		if albumArtist == "" {
			if t.AlbumArtist != "" {
				albumArtist = t.AlbumArtist
			} else if t.Artist != "" {
				albumArtist = t.Artist
			}
		}
		if year == nil && t.Year > 0 {
			y := t.Year
			year = &y
		}
		if genre == "" && t.Genre != "" {
			genre = t.Genre
		}
		if t.DiscNumber > discTotal {
			discTotal = t.DiscNumber
		}
		if t.DiscTotal > discTotal {
			discTotal = t.DiscTotal
		}
		if t.TrackTotal > trackTotal {
			trackTotal = t.TrackTotal
		}
	}

	return &GetTagsResponse{
		Album: AlbumSummaryDTO{
			AlbumArtist: albumArtist,
			Album:       albumTitle,
			Year:        year,
			Genre:       genre,
			DiscTotal:   discTotal,
			TrackTotal:  trackTotal,
		},
		Tracks:    tracks,
		SourceDir: relPath,
		CoverPath: coverPath,
		HasCover:  hasCover,
	}, nil
}
