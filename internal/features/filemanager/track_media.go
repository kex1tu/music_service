package filemanager

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
)

// StreamAudio отдает аудиофайл через http.ServeContent с поддержкой RFC 7233 partial content.
func (s *Service) StreamAudio(w http.ResponseWriter, r *http.Request, relPath string) {
	fullPath, _, err := s.ResolvePath(relPath)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) {
			http.Error(w, `{"error":"access denied"}`, http.StatusForbidden)
			return
		}
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}

	file, err := safefs.Open(fullPath)
	if err != nil {
		http.Error(w, `{"error":"failed to open file"}`, http.StatusNotFound)
		return
	}
	defer file.Close()

	fi, err := file.Stat()
	if err != nil || fi.IsDir() {
		http.Error(w, `{"error":"not an audio file"}`, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), file)
}

// StreamTrack отдает поток с оптимизацией sendfile(2) и RFC 7233.
func (s *Service) StreamTrack(w http.ResponseWriter, r *http.Request, idOrHash string) {
	fullPath, _, _, _, err := s.ResolveTrack(r.Context(), idOrHash)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) {
			http.Error(w, `{"error":"access denied"}`, http.StatusForbidden)
			return
		}
		http.Error(w, `{"error":"track not found"}`, http.StatusNotFound)
		return
	}

	fi, err := safefs.Stat(fullPath)
	if err != nil || fi.IsDir() {
		http.Error(w, `{"error":"file unavailable"}`, http.StatusNotFound)
		return
	}

	ext := strings.ToLower(filepath.Ext(fullPath))
	mimeType := "audio/mpeg"
	switch ext {
	case ".flac":
		mimeType = "audio/flac"
	case ".ogg":
		mimeType = "audio/ogg"
	case ".opus":
		mimeType = "audio/opus"
	case ".m4a":
		mimeType = "audio/mp4"
	}

	etag := fmt.Sprintf(`"%x-%x"`, fi.ModTime().Unix(), fi.Size())
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", etag)

	safefs.ServeFile(w, r, fullPath)
}

// GetTrackMetadata возвращает метаданные трека без блокировок.
func (s *Service) GetTrackMetadata(ctx context.Context, idOrHash string) (*TrackMetadataResponse, error) {
	fullPath, relPath, track, _, err := s.ResolveTrack(ctx, idOrHash)
	if err != nil {
		return nil, err
	}

	if track != nil {
		return track, nil
	}

	// Читаем метаданные на лету из файла
	fi, _ := safefs.Stat(fullPath)
	codec := strings.TrimPrefix(strings.ToLower(filepath.Ext(fullPath)), ".")
	if codec == "" {
		codec = "mp3"
	}

	resp := &TrackMetadataResponse{
		ID:         idOrHash,
		Codec:      codec,
		SampleRate: 44100,
		StreamURL:  fmt.Sprintf("/api/tracks/%s/stream", idOrHash),
		CoverURL:   fmt.Sprintf("/api/tracks/%s/cover", idOrHash),
		FilePath:   relPath,
	}
	if fi != nil {
		resp.FileSize = fi.Size()
	}

	if tags, err := id3.ReadTags(fullPath, relPath); err == nil && tags != nil {
		resp.Title = tags.Title
		resp.Artist = tags.Artist
		resp.Album = tags.Album
		resp.DurationSec = int(tags.Duration)
		resp.BitrateKbps = tags.Bitrate
		resp.TrackNumber = tags.TrackNumber
		resp.Year = tags.Year
		resp.Genre = tags.Genre
		resp.HasCover = tags.HasCover
	} else {
		resp.Title = strings.TrimSuffix(filepath.Base(fullPath), filepath.Ext(fullPath))
	}

	return resp, nil
}

// GetTrackCover извлекает обложку трека (встроенный APIC или файл cover.jpg) с защитой песочницы.
func (s *Service) GetTrackCover(ctx context.Context, idOrHash string) ([]byte, string, error) {
	fullPath, _, _, isTmp, err := s.ResolveTrack(ctx, idOrHash)
	if err != nil {
		return nil, "", err
	}

	// 1. Попытка извлечь встроенный APIC фрейм
	if strings.EqualFold(filepath.Ext(fullPath), ".mp3") {
		cover, err := id3.ReadCover(fullPath)
		if err == nil && cover != nil && len(cover.Data) > 0 {
			mime := cover.MimeType
			if mime == "" {
				mime = "image/jpeg"
			}
			return cover.Data, mime, nil
		}
	}

	// 2. Поиск сопутствующего файла обложки в каталоге
	dir := filepath.Dir(fullPath)
	var candidates []string
	trackBase := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))
	candidates = append(candidates, trackBase+".jpg", trackBase+".png", trackBase+".jpeg")
	for _, name := range []string{"cover.jpg", "cover.png", "folder.jpg", "cover.jpeg"} {
		candidates = append(candidates, filepath.Join(dir, name))
	}

	var sb *sandbox.PathSandbox = s.musicSandbox
	if isTmp {
		sb = s.tmpSandbox
	}

	for _, candPath := range candidates {
		if sb != nil {
			relCand, rErr := sb.ToRelative(candPath)
			if rErr != nil {
				continue
			}
			resolvedPath, sErr := sb.Resolve(relCand, false)
			if sErr != nil {
				continue
			}
			candPath = resolvedPath
		}
		if data, rErr := safefs.ReadFile(candPath); rErr == nil && len(data) > 0 {
			mime := "image/jpeg"
			if strings.HasSuffix(candPath, ".png") {
				mime = "image/png"
			}
			return data, mime, nil
		}
	}

	return nil, "", ErrFileNotFound
}
