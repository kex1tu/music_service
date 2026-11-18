package audit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/id3"
)

func (s *Service) GetMissingCovers(ctx context.Context, limit int) ([]MissingCoverItem, error) {
	return s.repo.GetAlbumsMissingCovers(ctx, limit)
}

func (s *Service) BackfillAlbumCover(ctx context.Context, folderPath string, albumID int64) (*BackfillCoverResponse, error) {
	folderPath = strings.TrimSpace(folderPath)
	if folderPath == "" && albumID > 0 {
		meta, _ := s.repo.GetAlbumMetadataByPath(ctx, strconv.FormatInt(albumID, 10))
		if meta != nil {
			folderPath = meta.FolderPath
		}
	}

	if folderPath == "" {
		return nil, fmt.Errorf("%w: missing folder_path or album_id", ErrInvalidRequest)
	}

	fullDir, err := s.musicSandbox.Resolve(folderPath, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFolderNotFound, err)
	}

	meta, _ := s.repo.GetAlbumMetadataByPath(ctx, folderPath)
	albumTitle := ""
	artistName := ""
	if meta != nil {
		albumTitle = meta.Title
		artistName = meta.Artist
	}
	if albumTitle == "" {
		albumTitle = filepath.Base(fullDir)
	}

	coverBytes, mime, cErr := s.fetchBestCoverOnline(ctx, artistName, albumTitle)
	if cErr != nil {
		return nil, fmt.Errorf("failed to find cover online: %w", cErr)
	}

	tracksUpdated, sErr := s.applyCoverToAlbumDir(fullDir, folderPath, coverBytes, mime)
	if sErr != nil {
		return nil, fmt.Errorf("failed to save cover: %w", sErr)
	}

	return &BackfillCoverResponse{
		Success:       true,
		FolderPath:    folderPath,
		TracksUpdated: tracksUpdated,
		Album:         albumTitle,
		Artist:        artistName,
	}, nil
}

func (s *Service) StartBatchCovers(ctx context.Context) (BatchProgress, error) {
	s.coversMu.Lock()
	if s.coversState.IsRunning {
		state := s.coversState
		s.coversMu.Unlock()
		return state, ErrBatchAlreadyRunning
	}

	missing, err := s.repo.GetAlbumsMissingCovers(ctx, 1000)
	if err != nil {
		s.coversMu.Unlock()
		return BatchProgress{}, err
	}

	if len(missing) == 0 {
		s.coversMu.Unlock()
		return BatchProgress{IsRunning: false}, nil
	}

	workerCtx, cancel := context.WithCancel(s.ctx)
	s.coversCancel = cancel
	s.coversState = BatchProgress{
		IsRunning: true,
		Total:     len(missing),
		Current:   0,
		Found:     0,
		Failed:    0,
	}
	s.coversMu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.coversMu.Lock()
			s.coversState.IsRunning = false
			s.coversState.CurrentAlbum = ""
			s.coversMu.Unlock()
		}()

		for i, a := range missing {
			select {
			case <-workerCtx.Done():
				return
			default:
			}

			s.coversMu.Lock()
			s.coversState.Current = i + 1
			s.coversState.CurrentAlbum = fmt.Sprintf("%s - %s", a.Artist, a.Title)
			s.coversMu.Unlock()

			fullDir, rErr := s.musicSandbox.Resolve(a.Path, false)
			if rErr != nil {
				s.coversMu.Lock()
				s.coversState.Failed++
				s.coversMu.Unlock()
				continue
			}

			coverBytes, mime, cErr := s.fetchBestCoverOnline(workerCtx, a.Artist, a.Title)
			if cErr != nil || len(coverBytes) == 0 {
				s.coversMu.Lock()
				s.coversState.Failed++
				s.coversMu.Unlock()
				time.Sleep(300 * time.Millisecond)
				continue
			}

			if _, err := s.applyCoverToAlbumDir(fullDir, a.Path, coverBytes, mime); err == nil {
				s.coversMu.Lock()
				s.coversState.Found++
				s.coversMu.Unlock()
			} else {
				s.coversMu.Lock()
				s.coversState.Failed++
				s.coversMu.Unlock()
			}

			time.Sleep(350 * time.Millisecond)
		}
	}()

	s.coversMu.RLock()
	defer s.coversMu.RUnlock()
	return s.coversState, nil
}

func (s *Service) GetBatchCoversProgress() BatchProgress {
	s.coversMu.RLock()
	defer s.coversMu.RUnlock()
	return s.coversState
}

func (s *Service) CancelBatchCovers() bool {
	s.coversMu.Lock()
	defer s.coversMu.Unlock()
	if s.coversCancel != nil && s.coversState.IsRunning {
		s.coversCancel()
		s.coversState.IsRunning = false
		return true
	}
	return false
}

func (s *Service) fetchBestCoverOnline(ctx context.Context, artist, album string) ([]byte, string, error) {
	client := s.httpClient
	cleanArt := strings.TrimSpace(artist)
	cleanAlb := strings.TrimSpace(album)
	query := fmt.Sprintf("%s %s", cleanArt, cleanAlb)

	// 1. Try iTunes Search API (1400x1400 HQ art)
	if s.itunes != nil {
		tracks, err := s.itunes.SearchTracks(ctx, query)
		if err == nil && len(tracks) > 0 {
			for _, tr := range tracks {
				if tr.CoverURL != "" {
					hqArt := strings.Replace(tr.CoverURL, "100x100bb", "1400x1400bb", 1)
					if data, mime, err := downloadImageWithMime(ctx, client, hqArt); err == nil && len(data) > 0 {
						return data, mime, nil
					}
				}
			}
		}
	}

	// 2. Try Deezer API (1000x1000 cover_xl)
	if s.deezer != nil {
		albums, err := s.deezer.SearchAlbums(ctx, query)
		if err == nil && len(albums) > 0 {
			for _, alb := range albums {
				bestURL := alb.BestCoverURL()
				if bestURL != "" {
					if data, mime, err := downloadImageWithMime(ctx, client, bestURL); err == nil && len(data) > 0 {
						return data, mime, nil
					}
				}
			}
		}
	}

	return nil, "", fmt.Errorf("cover not found online for %q", query)
}

func (s *Service) applyCoverToAlbumDir(fullDir, folderRelPath string, coverBytes []byte, mime string) (int, error) {
	if len(coverBytes) == 0 {
		return 0, fmt.Errorf("empty cover bytes")
	}

	ext := ".jpg"
	if mime == "image/png" || bytes.HasPrefix(coverBytes, []byte("\x89PNG")) {
		ext = ".png"
		mime = "image/png"
	} else {
		mime = "image/jpeg"
	}

	coverPath := filepath.Join(fullDir, "cover"+ext)
	if err := safefs.WriteFile(coverPath, coverBytes, 0644); err != nil {
		return 0, fmt.Errorf("failed to write album cover: %w", err)
	}
	_ = safefs.Chmod(coverPath, 0644)

	entries, err := safefs.ReadDir(fullDir)
	if err != nil {
		return 0, err
	}

	coverInfo := &id3.CoverInfo{
		MimeType: mime,
		Data:     coverBytes,
	}

	tracksUpdated := 0
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".mp3") {
			continue
		}

		baseName := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		trackCoverPath := filepath.Join(fullDir, baseName+ext)
		_ = safefs.WriteFile(trackCoverPath, coverBytes, 0644)
		_ = safefs.Chmod(trackCoverPath, 0644)

		mp3Path := filepath.Join(fullDir, e.Name())
		relTrack, _ := s.musicSandbox.ToRelative(mp3Path)

		tags, rErr := id3.ReadTags(mp3Path, relTrack)
		if rErr == nil && tags != nil {
			if wErr := id3.WriteTags(mp3Path, *tags, coverInfo); wErr == nil {
				tracksUpdated++
				if s.scanner != nil {
					_ = s.scanner.IndexFile(mp3Path, relTrack)
				}
			}
		}
	}

	relCover, _ := s.musicSandbox.ToRelative(coverPath)
	_ = s.repo.UpdateAlbumCover(context.Background(), folderRelPath, relCover, mime)

	return tracksUpdated, nil
}

func downloadImageWithMime(ctx context.Context, client *http.Client, urlStr string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, "", err
	}
	if req.URL.User != nil {
		return nil, "", fmt.Errorf("cover URL must not contain user credentials")
	}

	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}

	if resp.ContentLength > int64(id3.MaxCoverDownloadBytes) {
		return nil, "", fmt.Errorf("cover Content-Length %d exceeds limit of %d bytes", resp.ContentLength, id3.MaxCoverDownloadBytes)
	}

	limitReader := io.LimitReader(resp.Body, int64(id3.MaxCoverDownloadBytes)+1)
	data, err := io.ReadAll(limitReader)
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > int64(id3.MaxCoverDownloadBytes) {
		return nil, "", fmt.Errorf("cover size exceeds limit of %d bytes", id3.MaxCoverDownloadBytes)
	}
	if len(data) == 0 {
		return nil, "", errors.New("empty cover body")
	}

	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	if strings.Contains(mime, "png") || bytes.HasPrefix(data, []byte("\x89PNG")) {
		mime = "image/png"
	} else {
		mime = "image/jpeg"
	}

	return data, mime, nil
}
