package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/id3"
)

var (
	errTrackNotFound = errors.New("track not found")
)

// resolveTrack находит физический путь к треку по числовому ID, хэшу или прямому относительному пути.
// Гарантирует строгую изоляцию через PathSandbox (инвариант #2).
func (h *FileHandler) resolveTrack(idOrHash string) (fullPath string, relPath string, track *db.TrackEntity, err error) {
	idOrHash = strings.TrimSpace(idOrHash)
	if idOrHash == "" {
		return "", "", nil, errTrackNotFound
	}

	// 1. Проверяем быстрый in-memory кэш
	if val, ok := h.trackCache.Load(idOrHash); ok {
		relPath = val.(string)
	}

	// 2. Если в кэше нет — запрашиваем SQLite
	if relPath == "" && h.database != nil {
		t, dbErr := h.database.GetTrackByIDOrHash(context.Background(), idOrHash)
		if dbErr == nil && t != nil {
			track = t
			relPath = t.FilePath
			// Кэшируем по строковому ID и хэшу
			h.trackCache.Store(idOrHash, relPath)
			h.trackCache.Store(strconv.FormatInt(t.ID, 10), relPath)
			if t.FileHash != "" {
				h.trackCache.Store(t.FileHash, relPath)
			}
		}
	}

	// 3. Если в базе нет — возможно idOrHash является прямым относительным путем к файлу в MUSIC_DIR
	if relPath == "" {
		if testFull, testErr := h.musicSandbox.Resolve(idOrHash, false); testErr == nil && fileExists(testFull) {
			relPath = idOrHash
		}
	}

	if relPath == "" {
		return "", "", nil, errTrackNotFound
	}

	// 4. Обязательная валидация через PathSandbox (оборона в глубину)
	fullPath, err = h.musicSandbox.Resolve(relPath, false)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) {
			return "", "", nil, err // Передаем ErrPathTraversal для выдачи 403 Forbidden
		}
		// Пробуем tmpSandbox если путь указывает на временные файлы
		if h.tmpSandbox != nil {
			cleanRel := strings.TrimPrefix(relPath, "tmp_downloads/")
			cleanRel = strings.TrimPrefix(cleanRel, "tmp/")
			if p, err2 := h.tmpSandbox.Resolve(cleanRel, false); err2 == nil && fileExists(p) {
				fullPath = p
				err = nil
			}
		}
	}

	if err != nil || fullPath == "" || !fileExists(fullPath) {
		return "", "", nil, errTrackNotFound
	}

	return fullPath, relPath, track, nil
}

// StreamTrack отдает аудиопоток с полной поддержкой RFC 7233 HTTP Range (206 Partial Content, перемотка, ETag, sendfile Zero-Copy).
func (h *FileHandler) StreamTrack(w http.ResponseWriter, r *http.Request) {
	trackID := chi.URLParam(r, "id")
	if trackID == "" {
		trackID = r.URL.Query().Get("id")
	}
	if trackID == "" {
		trackID = r.URL.Query().Get("path")
	}

	fullPath, _, _, err := h.resolveTrack(trackID)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) {
			http.Error(w, `{"error":"access denied"}`, http.StatusForbidden)
			return
		}
		http.Error(w, `{"error":"track not found"}`, http.StatusNotFound)
		return
	}

	// Определение MIME-типа
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
	case ".aac":
		mimeType = "audio/aac"
	case ".wav":
		mimeType = "audio/wav"
	}

	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "private, max-age=3600")

	// http.ServeFile нативно реализует RFC 7233 Range-запросы и использует sendfile(2) в Linux
	http.ServeFile(w, r, fullPath)
}

// TrackMetadataResponse представляет структурированные метаданные трека.
type TrackMetadataResponse struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	DurationSec int    `json:"duration_sec"`
	BitrateKbps int    `json:"bitrate_kbps"`
	SampleRate  int    `json:"sample_rate"`
	TrackNumber int    `json:"track_number,omitempty"`
	Year        int    `json:"year,omitempty"`
	Genre       string `json:"genre,omitempty"`
	FileSize    int64  `json:"file_size,omitempty"`
	Codec       string `json:"codec"`
	HasCover    bool   `json:"has_cover"`
	StreamURL   string `json:"stream_url"`
	CoverURL    string `json:"cover_url"`
	FilePath    string `json:"file_path,omitempty"`
}

// GetTrackMetadata возвращает метаданные трека без задержки чтения аудиопотока.
func (h *FileHandler) GetTrackMetadata(w http.ResponseWriter, r *http.Request) {
	trackID := chi.URLParam(r, "id")
	if trackID == "" {
		trackID = r.URL.Query().Get("id")
	}

	fullPath, relPath, track, err := h.resolveTrack(trackID)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) {
			http.Error(w, `{"error":"access denied"}`, http.StatusForbidden)
			return
		}
		http.Error(w, `{"error":"track not found"}`, http.StatusNotFound)
		return
	}

	codec := strings.TrimPrefix(strings.ToLower(filepath.Ext(fullPath)), ".")
	if codec == "" {
		codec = "mp3"
	}

	resp := TrackMetadataResponse{
		ID:         trackID,
		Codec:      codec,
		SampleRate: 44100,
		StreamURL:  fmt.Sprintf("/api/tracks/%s/stream", trackID),
		CoverURL:   fmt.Sprintf("/api/tracks/%s/cover", trackID),
		FilePath:   relPath,
	}

	if track != nil {
		resp.Title = track.Title
		resp.Artist = track.Artist
		resp.Album = track.Album
		resp.DurationSec = int(track.Duration)
		resp.BitrateKbps = track.Bitrate
		resp.TrackNumber = track.TrackNumber
		resp.FileSize = track.FileSize
		resp.HasCover = track.HasCover || track.HasEmbeddedCover
	} else {
		// Читаем теги на лету через pure Go id3 движок
		if tags, err := id3.ReadTags(fullPath, relPath); err == nil {
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
		if fi, err := os.Stat(fullPath); err == nil {
			resp.FileSize = fi.Size()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// GetTrackCover отдает обложку трека (встроенный APIC или файл cover.jpg) с кэшированием.
func (h *FileHandler) GetTrackCover(w http.ResponseWriter, r *http.Request) {
	trackID := chi.URLParam(r, "id")
	if trackID == "" {
		trackID = r.URL.Query().Get("id")
	}

	fullPath, _, _, err := h.resolveTrack(trackID)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) {
			http.Error(w, `{"error":"access denied"}`, http.StatusForbidden)
			return
		}
		http.Error(w, `{"error":"track not found"}`, http.StatusNotFound)
		return
	}

	st, err := os.Stat(fullPath)
	if err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}

	// 1. Проверяем встроенную обложку APIC в MP3
	if strings.EqualFold(filepath.Ext(fullPath), ".mp3") {
		cover, err := id3.ReadCover(fullPath)
		if err == nil && cover != nil && len(cover.Data) > 0 {
			w.Header().Set("Content-Type", cover.MimeType)
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = w.Write(cover.Data)
			return
		}
	}

	// 2. Ищем индивидуальную обложку трека {TrackBaseName}.jpg/.png или cover.jpg в каталоге
	var candidates []string
	if !st.IsDir() {
		trackBase := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))
		candidates = append(candidates,
			trackBase+".jpg", trackBase+".png", trackBase+".jpeg", trackBase+".webp",
		)
	}
	dir := fullPath
	if !st.IsDir() {
		dir = filepath.Dir(fullPath)
	}
	for _, cand := range []string{"cover.jpg", "cover.png", "folder.jpg", "folder.png", "cover.jpeg"} {
		candidates = append(candidates, filepath.Join(dir, cand))
	}

	for _, candPath := range candidates {
		if data, err := os.ReadFile(candPath); err == nil {
			mime := "image/jpeg"
			if strings.HasSuffix(candPath, ".png") {
				mime = "image/png"
			} else if strings.HasSuffix(candPath, ".webp") {
				mime = "image/webp"
			}
			w.Header().Set("Content-Type", mime)
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = w.Write(data)
			return
		}
	}

	http.Error(w, `{"error":"cover not found"}`, http.StatusNotFound)
}

// LibraryTrackItem представляет трек в выдаче каталога.
type LibraryTrackItem struct {
	ID          string `json:"id"`
	NumericID   int64  `json:"numeric_id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	DurationSec int    `json:"duration_sec"`
	BitrateKbps int    `json:"bitrate_kbps"`
	Path        string `json:"path"`
	HasCover    bool   `json:"has_cover"`
	StreamURL   string `json:"stream_url"`
	CoverURL    string `json:"cover_url"`
}

// ListLibraryTracks отдает список треков медиатеки с пагинацией и поиском.
func (h *FileHandler) ListLibraryTracks(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("query"))
	if q == "" {
		q = strings.TrimSpace(r.URL.Query().Get("q"))
	}

	limit := 50
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
			if l > 200 {
				limit = 200
			} else {
				limit = l
			}
		}
	}

	offset := 0
	if oStr := r.URL.Query().Get("offset"); oStr != "" {
		if o, err := strconv.Atoi(oStr); err == nil && o >= 0 {
			offset = o
		}
	}

	var tracks []LibraryTrackItem
	if h.database != nil {
		var rows *sql.Rows
		var err error
		if q != "" {
			term := "%" + q + "%"
			query := `
			SELECT t.id, t.title, ar.name AS artist, al.title AS album, t.duration, t.bitrate, t.file_path,
			       COALESCE(t.file_hash, ''), al.has_cover, t.has_embedded_cover
			FROM tracks t
			JOIN albums al ON t.album_id = al.id
			JOIN artists ar ON t.artist_id = ar.id
			WHERE t.title LIKE ? OR t.clean_title LIKE ? OR ar.name LIKE ? OR al.title LIKE ?
			ORDER BY t.id DESC
			LIMIT ? OFFSET ?;
			`
			rows, err = h.database.Conn().QueryContext(r.Context(), query, term, term, term, term, limit, offset)
		} else {
			query := `
			SELECT t.id, t.title, ar.name AS artist, al.title AS album, t.duration, t.bitrate, t.file_path,
			       COALESCE(t.file_hash, ''), al.has_cover, t.has_embedded_cover
			FROM tracks t
			JOIN albums al ON t.album_id = al.id
			JOIN artists ar ON t.artist_id = ar.id
			ORDER BY t.id DESC
			LIMIT ? OFFSET ?;
			`
			rows, err = h.database.Conn().QueryContext(r.Context(), query, limit, offset)
		}

		if err == nil && rows != nil {
			defer rows.Close()
			for rows.Next() {
				var item LibraryTrackItem
				var dur float64
				var fileHash string
				var albumHasCover, trackHasEmbeddedCover bool

				if scanErr := rows.Scan(
					&item.NumericID, &item.Title, &item.Artist, &item.Album,
					&dur, &item.BitrateKbps, &item.Path, &fileHash,
					&albumHasCover, &trackHasEmbeddedCover,
				); scanErr == nil {
					if fileHash == "" {
						fileHash = db.TrackHash(item.Path)
					}
					item.ID = fileHash
					item.DurationSec = int(dur)
					item.HasCover = albumHasCover || trackHasEmbeddedCover
					item.StreamURL = fmt.Sprintf("/api/tracks/%s/stream", item.ID)
					item.CoverURL = fmt.Sprintf("/api/tracks/%s/cover", item.ID)

					// Кэшируем для последующего мгновенного воспроизведения
					h.trackCache.Store(item.ID, item.Path)
					h.trackCache.Store(strconv.FormatInt(item.NumericID, 10), item.Path)

					tracks = append(tracks, item)
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tracks": tracks,
		"count":  len(tracks),
		"limit":  limit,
		"offset": offset,
	})
}
