package tageditor

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
)

// SaveTags записывает теги в один файл с соблюдением стандарта ID3v2.4 и null-byte разделителей.
func (s *Service) SaveTags(ctx context.Context, tags TrackTags) error {
	fullPath, isTmp, err := s.ResolvePath(tags.Path)
	if err != nil {
		return err
	}

	dirLock := s.getDirLock(filepath.Dir(fullPath))
	dirLock.Lock()
	defer dirLock.Unlock()

	write := func() error {
		if err := id3.WriteTags(fullPath, tags, nil); err != nil {
			return fmt.Errorf("failed to write tags: %w", err)
		}
		return nil
	}
	if isTmp {
		return write()
	}
	if s.trackIndex != nil {
		return s.trackIndex.ChangeTrack(ctx, fullPath, fullPath, false, write)
	}
	if err := write(); err != nil {
		return err
	}
	if s.repo != nil {
		return s.repo.UpdateTrackTags(ctx, tags.Path, tags)
	}
	return nil
}

// BatchSaveTags выполняет согласованное сохранение метаданных и раскладку дорожек альбома.
func (s *Service) BatchSaveTags(ctx context.Context, req BatchSaveRequest) (*BatchSaveResponse, error) {
	if s.organizer == nil {
		return nil, errors.New("organizer service not configured")
	}

	// Нормализация одиночного трека (сингла): подтягивание полей альбома из трека
	if len(req.Tracks) == 1 {
		t := &req.Tracks[0]
		if strings.TrimSpace(req.Album.AlbumArtist) == "" && strings.TrimSpace(t.Artist) != "" {
			req.Album.AlbumArtist = strings.TrimSpace(t.Artist)
		}
		if strings.TrimSpace(req.Album.Album) == "" || strings.EqualFold(req.Album.Album, "single") || strings.EqualFold(req.Album.Album, "singles") {
			if strings.TrimSpace(t.Title) != "" {
				req.Album.Album = strings.TrimSpace(t.Title)
			}
		}
		if (req.Album.Year == nil || *req.Album.Year == 0) && t.Year > 0 {
			yr := t.Year
			req.Album.Year = &yr
		}
		if strings.TrimSpace(req.Album.Genre) == "" && strings.TrimSpace(t.Genre) != "" {
			req.Album.Genre = strings.TrimSpace(t.Genre)
		}
	}

	// Каскадное распространение полей альбома на треки, если они пустые
	for i := range req.Tracks {
		if strings.TrimSpace(req.Tracks[i].Album) == "" {
			if strings.TrimSpace(req.Album.Album) != "" && !strings.EqualFold(req.Album.Album, "singles") && !strings.EqualFold(req.Album.Album, "single") {
				req.Tracks[i].Album = strings.TrimSpace(req.Album.Album)
			} else if strings.TrimSpace(req.Tracks[i].Title) != "" {
				req.Tracks[i].Album = strings.TrimSpace(req.Tracks[i].Title)
			}
		} else if strings.EqualFold(req.Tracks[i].Album, "single") || strings.EqualFold(req.Tracks[i].Album, "singles") {
			if strings.TrimSpace(req.Tracks[i].Title) != "" {
				req.Tracks[i].Album = strings.TrimSpace(req.Tracks[i].Title)
			}
		}
		if strings.TrimSpace(req.Tracks[i].AlbumArtist) == "" {
			req.Tracks[i].AlbumArtist = strings.TrimSpace(req.Album.AlbumArtist)
		}
		if req.Album.Year != nil && *req.Album.Year > 0 && req.Tracks[i].Year == 0 {
			req.Tracks[i].Year = *req.Album.Year
		}
		if strings.TrimSpace(req.Album.Genre) != "" && strings.TrimSpace(req.Tracks[i].Genre) == "" {
			req.Tracks[i].Genre = strings.TrimSpace(req.Album.Genre)
		}
	}

	// Если CoverBase64 пустой, но передан CoverURL (онлайн-обложка из мультисервисной галереи), скачиваем на сервере
	coverB64 := req.CoverBase64
	if strings.TrimSpace(coverB64) == "" && strings.TrimSpace(req.CoverURL) != "" && strings.HasPrefix(strings.ToLower(req.CoverURL), "http") {
		if data, err := s.downloadImage(ctx, req.CoverURL); err == nil && len(data) > 0 {
			coverB64 = base64.StdEncoding.EncodeToString(data)
		}
	}

	move := s.organizer.MoveAlbum
	if contextual, ok := s.organizer.(interface {
		MoveAlbumContext(context.Context, string, string, string, []id3.TrackTags, string) (*organizer.OrganizerResult, error)
	}); ok {
		move = func(dir, album, artist string, tracks []id3.TrackTags, cover string) (*organizer.OrganizerResult, error) {
			return contextual.MoveAlbumContext(ctx, dir, album, artist, tracks, cover)
		}
	}
	var res *organizer.OrganizerResult
	var err error
	if req.TargetAlbumPath != "" {
		if contextual, ok := s.organizer.(interface {
			MoveAlbumIntoContext(context.Context, string, string, string, []id3.TrackTags, string) (*organizer.OrganizerResult, error)
		}); ok {
			res, err = contextual.MoveAlbumIntoContext(ctx, req.TargetAlbumPath, req.Album.Album, req.Album.AlbumArtist, req.Tracks, coverB64)
		} else {
			res, err = s.organizer.MoveAlbumInto(req.TargetAlbumPath, req.Album.Album, req.Album.AlbumArtist, req.Tracks, coverB64)
		}
	} else {
		res, err = move(req.SourceDir, req.Album.Album, req.Album.AlbumArtist, req.Tracks, coverB64)
	}
	if err != nil {
		return nil, fmt.Errorf("organizer move album failed: %w", err)
	}

	response := &BatchSaveResponse{
		Success:        res.Complete(),
		TrackResults:   res.TrackResults,
		DestinationDir: res.DestinationDir,
		Tracks:         res.Tracks,
	}
	if s.trackIndex == nil && s.repo != nil && len(res.Tracks) > 0 {
		if err := s.repo.BatchUpdateTrackTags(ctx, res.Tracks); err != nil {
			response.Success = false
			response.Error = err.Error()
		}
	}
	return response, nil
}
