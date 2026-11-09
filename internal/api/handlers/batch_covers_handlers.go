package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"path/filepath"
	"strings"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/id3"
)

type BackfillCoverRequest struct {
	FolderPath string `json:"folder_path"`
	AlbumID    int64  `json:"album_id,omitempty"`
}

// BackfillAlbumCover находит обложку высокого разрешения в онлайн-каталогах (iTunes -> Deezer -> Yandex),
// сохраняет cover.jpg, создает поквартирные {TrackBaseName}.jpg, вшивает APIC в MP3 и синхронизирует SQLite.
func (h *FileHandler) BackfillAlbumCover(w http.ResponseWriter, r *http.Request) {
	var req BackfillCoverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json request"}`, http.StatusBadRequest)
		return
	}

	folderPath := strings.TrimSpace(req.FolderPath)
	if folderPath == "" && req.AlbumID > 0 {
		_ = h.database.Conn().QueryRowContext(r.Context(), "SELECT folder_path FROM albums WHERE id = ?", req.AlbumID).Scan(&folderPath)
	}

	if folderPath == "" {
		http.Error(w, `{"error":"missing folder_path or album_id"}`, http.StatusBadRequest)
		return
	}

	fullDir, err := h.musicSandbox.Resolve(folderPath, false)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"folder not found in library: %v"}`, err), http.StatusNotFound)
		return
	}

	// Получаем метаданные альбома
	var albumTitle, artistName string
	_ = h.database.Conn().QueryRowContext(r.Context(), `
		SELECT al.title, a.name
		FROM albums al
		JOIN artists a ON al.artist_id = a.id
		WHERE al.folder_path = ? OR al.folder_path = ?
		LIMIT 1;
	`, folderPath, filepath.Clean(folderPath)).Scan(&albumTitle, &artistName)

	if albumTitle == "" {
		albumTitle = filepath.Base(fullDir)
	}

	coverBytes, mime, cErr := h.fetchBestCoverOnline(r.Context(), artistName, albumTitle)
	if cErr != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to find cover online: %v"}`, cErr), http.StatusNotFound)
		return
	}

	tracksUpdated, sErr := h.applyCoverToAlbumDir(fullDir, folderPath, coverBytes, mime)
	if sErr != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to save cover: %v"}`, sErr), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":        true,
		"folder_path":    folderPath,
		"tracks_updated": tracksUpdated,
		"album":          albumTitle,
		"artist":         artistName,
	})
}

// BatchCoversBackfill запускает фоновый процесс дозагрузки обложек для всех альбомов без обложек.
func (h *FileHandler) BatchCoversBackfill(w http.ResponseWriter, r *http.Request) {
	h.coversState.Lock()
	if h.coversState.isRunning {
		resp := map[string]any{
			"is_running":    true,
			"total":         h.coversState.total,
			"current":       h.coversState.current,
			"found":         h.coversState.found,
			"failed":        h.coversState.failed,
			"current_album": h.coversState.currentAlbum,
		}
		h.coversState.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	type albumToBackfill struct {
		folderPath string
		title      string
		artist     string
	}

	rows, err := h.database.Conn().QueryContext(r.Context(), `
		SELECT al.folder_path, al.title, a.name
		FROM albums al
		JOIN artists a ON al.artist_id = a.id
		WHERE al.has_cover = 0
		ORDER BY al.id;
	`)
	if err != nil {
		h.coversState.Unlock()
		http.Error(w, `{"error":"failed to query albums"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var albums []albumToBackfill
	for rows.Next() {
		var a albumToBackfill
		if err := rows.Scan(&a.folderPath, &a.title, &a.artist); err == nil {
			albums = append(albums, a)
		}
	}

	if len(albums) == 0 {
		h.coversState.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"is_running":    false,
			"total":         0,
			"current":       0,
			"found":         0,
			"failed":        0,
			"current_album": "",
		})
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	h.coversState.isRunning = true
	h.coversState.total = len(albums)
	h.coversState.current = 0
	h.coversState.found = 0
	h.coversState.failed = 0
	h.coversState.currentAlbum = ""
	h.coversState.cancel = cancel
	h.coversState.Unlock()

	go func() {
		defer func() {
			h.coversState.Lock()
			h.coversState.isRunning = false
			h.coversState.currentAlbum = ""
			if h.coversState.cancel != nil {
				h.coversState.cancel()
				h.coversState.cancel = nil
			}
			h.coversState.Unlock()
		}()

		for i, a := range albums {
			select {
			case <-ctx.Done():
				return
			default:
			}

			h.coversState.Lock()
			h.coversState.current = i + 1
			h.coversState.currentAlbum = fmt.Sprintf("%s - %s", a.artist, a.title)
			h.coversState.Unlock()

			fullDir, rErr := h.musicSandbox.Resolve(a.folderPath, false)
			if rErr != nil {
				h.coversState.Lock()
				h.coversState.failed++
				h.coversState.Unlock()
				continue
			}

			coverBytes, mime, cErr := h.fetchBestCoverOnline(ctx, a.artist, a.title)
			if cErr != nil || len(coverBytes) == 0 {
				h.coversState.Lock()
				h.coversState.failed++
				h.coversState.Unlock()
				time.Sleep(300 * time.Millisecond) // защита от троттлинга
				continue
			}

			if _, err := h.applyCoverToAlbumDir(fullDir, a.folderPath, coverBytes, mime); err == nil {
				h.coversState.Lock()
				h.coversState.found++
				h.coversState.Unlock()
			} else {
				h.coversState.Lock()
				h.coversState.failed++
				h.coversState.Unlock()
			}

			time.Sleep(350 * time.Millisecond) // вежливая пауза между сетевыми запросами
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"is_running":    true,
		"total":         len(albums),
		"current":       0,
		"found":         0,
		"failed":        0,
		"current_album": "Запуск процесса дозагрузки обложек...",
	})
}

// CancelCoversBatch отменяет фоновый процесс дозагрузки обложек.
func (h *FileHandler) CancelCoversBatch(w http.ResponseWriter, r *http.Request) {
	h.coversState.Lock()
	if h.coversState.cancel != nil {
		h.coversState.cancel()
		h.coversState.cancel = nil
	}
	h.coversState.isRunning = false
	h.coversState.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
}

// BatchCoversProgress возвращает текущий статус фонового процесса дозагрузки обложек.
func (h *FileHandler) BatchCoversProgress(w http.ResponseWriter, r *http.Request) {
	h.coversState.Lock()
	resp := map[string]any{
		"is_running":    h.coversState.isRunning,
		"total":         h.coversState.total,
		"current":       h.coversState.current,
		"found":         h.coversState.found,
		"failed":        h.coversState.failed,
		"current_album": h.coversState.currentAlbum,
	}
	h.coversState.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// NormalizeNavidrome запускает процесс нормализации медиатеки под требования Navidrome
// (выставление прав 0644/0755, удаление конфликтных TXXX и конвертация '\\' в ID3v2.4 null-байты '\x00').
func (h *FileHandler) NormalizeNavidrome(w http.ResponseWriter, r *http.Request) {
	if h.organizer == nil {
		http.Error(w, `{"error":"organizer service is not initialized"}`, http.StatusInternalServerError)
		return
	}

	report, err := h.organizer.NormalizeNavidromeLibrary(r.Context(), func(relPath string) {
		if h.scanner != nil {
			if full, err := h.musicSandbox.Resolve(relPath, false); err == nil {
				_ = h.scanner.IndexFile(full, relPath)
			}
		}
	})
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"normalization failed: %v"}`, err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"report":  report,
	})
}

// fetchBestCoverOnline ищет обложку высокого разрешения в онлайн-каталогах.
func (h *FileHandler) fetchBestCoverOnline(ctx context.Context, artist, album string) ([]byte, string, error) {
	client := h.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	cleanArt := strings.TrimSpace(artist)
	cleanAlb := strings.TrimSpace(album)
	query := fmt.Sprintf("%s %s", cleanArt, cleanAlb)

	// 1. Попытка через iTunes Search API (1400x1400 HQ art)
	if h.itunes != nil {
		tracks, err := h.itunes.SearchTracks(ctx, query)
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

	// 2. Попытка через Deezer API (1000x1000 cover_xl)
	if h.deezer != nil {
		albums, err := h.deezer.SearchAlbums(ctx, query)
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

// applyCoverToAlbumDir сохраняет cover.jpg, создает трековые обложки и вшивает APIC в MP3.
func (h *FileHandler) applyCoverToAlbumDir(fullDir, folderRelPath string, coverBytes []byte, mime string) (int, error) {
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

	// 1. Сохраняем cover.jpg в каталоге альбома с правами 0644
	coverPath := filepath.Join(fullDir, "cover"+ext)
	if err := safefs.WriteFile(coverPath, coverBytes, 0644); err != nil {
		return 0, fmt.Errorf("failed to write album cover: %w", err)
	}
	_ = safefs.Chmod(coverPath, 0644)

	// 2. Обходим треки альбома: сохраняем {TrackBase}.jpg и вшиваем APIC
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
		relTrack, _ := h.musicSandbox.ToRelative(mp3Path)

		tags, rErr := id3.ReadTags(mp3Path, relTrack)
		if rErr == nil && tags != nil {
			if wErr := id3.WriteTags(mp3Path, *tags, coverInfo); wErr == nil {
				tracksUpdated++
				if h.scanner != nil {
					_ = h.scanner.IndexFile(mp3Path, relTrack)
				}
			}
		}
	}

	// 3. Обновляем статус альбома в SQLite
	relCover, _ := h.musicSandbox.ToRelative(coverPath)
	_, _ = h.database.Conn().Exec(`
		UPDATE albums
		SET has_cover = 1, cover_path = ?, cover_mime = ?, updated_at = CURRENT_TIMESTAMP
		WHERE folder_path = ? OR folder_path = ?;
	`, relCover, mime, folderRelPath, filepath.Clean(folderRelPath))

	_, _ = h.database.Conn().Exec(`
		UPDATE tracks
		SET has_embedded_cover = 1
		WHERE album_id IN (
			SELECT id FROM albums WHERE folder_path = ? OR folder_path = ?
		);
	`, folderRelPath, filepath.Clean(folderRelPath))

	log.Printf("[CoverBackfill] Successfully backfilled cover for %s (%d tracks updated)", folderRelPath, tracksUpdated)
	return tracksUpdated, nil
}

func downloadImageWithMime(ctx context.Context, client *http.Client, urlStr string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
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
