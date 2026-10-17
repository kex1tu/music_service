package filemanager

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/httputil"
	"music-manager/internal/core/sandbox"
)

// ListFiles обрабатывает GET /files, GET /api/files (вывод содержимого папки или всей библиотеки).
func (m *Module) ListFiles(w http.ResponseWriter, r *http.Request) {
	relPath := httputil.QueryString(r, "path", "")
	if relPath == "" {
		relPath = httputil.QueryString(r, "dir", "")
	}
	recursive := httputil.QueryBool(r, "recursive", false)

	resp, err := m.service.ListFiles(r.Context(), relPath, recursive)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) || errors.Is(err, ErrSandboxViolation) {
			httputil.RespondError(w, http.StatusForbidden, "Доступ запрещен")
			return
		}
		if errors.Is(err, ErrFileNotFound) {
			httputil.RespondError(w, http.StatusNotFound, "Директория или файл не найдены")
			return
		}
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if resp == nil {
		resp = &DirectoryListingResponse{
			Breadcrumbs: []BreadcrumbItem{{Name: "Корень", Path: ""}},
			Items:       []FileItem{},
		}
	}
	httputil.RespondJSON(w, http.StatusOK, resp)
}

// DeleteFile обрабатывает DELETE /files, DELETE /api/files (удаление файла, директории или пакета файлов).
func (m *Module) DeleteFile(w http.ResponseWriter, r *http.Request) {
	relPath := httputil.QueryString(r, "path", "")
	if relPath == "" {
		relPath = httputil.QueryString(r, "file", "")
	}

	var req DeleteRequest
	if r.Body != nil && (relPath == "" || strings.Contains(r.Header.Get("Content-Type"), "json")) {
		_ = httputil.DecodeJSON(r, &req)
	}
	if relPath == "" && req.Path != "" {
		relPath = req.Path
	}

	// 1. Пакетное удаление путей
	if len(req.Paths) > 0 {
		res, err := m.service.DeleteBatch(r.Context(), req.Paths)
		if err != nil {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		httputil.RespondJSON(w, http.StatusOK, res)
		return
	}

	// 2. Одиночное удаление
	if strings.TrimSpace(relPath) == "" {
		httputil.RespondError(w, http.StatusBadRequest, "Параметр 'path' или 'paths' обязателен для удаления")
		return
	}

	res, err := m.service.DeleteFile(r.Context(), relPath)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) || errors.Is(err, ErrSandboxViolation) {
			httputil.RespondError(w, http.StatusForbidden, "Доступ запрещен (path traversal)")
			return
		}
		if errors.Is(err, ErrFileNotFound) {
			httputil.RespondError(w, http.StatusNotFound, "Удаляемый файл или каталог не найден")
			return
		}
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, res)
}

// RenameFile обрабатывает POST /files/rename, POST /api/files/rename.
func (m *Module) RenameFile(w http.ResponseWriter, r *http.Request) {
	var req RenameRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "Некорректный формат запроса: "+err.Error())
		return
	}

	if strings.TrimSpace(req.OldPath) == "" || strings.TrimSpace(req.NewPath) == "" {
		httputil.RespondError(w, http.StatusBadRequest, "Поля oldPath и newPath обязательны")
		return
	}

	res, err := m.service.RenameFile(r.Context(), req.OldPath, req.NewPath)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) || errors.Is(err, ErrSandboxViolation) {
			httputil.RespondError(w, http.StatusForbidden, "Доступ запрещен (path traversal)")
			return
		}
		if errors.Is(err, ErrFileNotFound) {
			httputil.RespondError(w, http.StatusNotFound, "Исходный файл не найден")
			return
		}
		if errors.Is(err, ErrTargetExists) {
			httputil.RespondError(w, http.StatusConflict, "Целевой файл уже существует")
			return
		}
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, res)
}

// StreamAudio обрабатывает GET /files/stream (потоковое воспроизведение по относительному пути).
func (m *Module) StreamAudio(w http.ResponseWriter, r *http.Request) {
	path := httputil.QueryString(r, "path", "")
	if path == "" {
		path = httputil.QueryString(r, "file", "")
	}
	if path == "" {
		httputil.RespondError(w, http.StatusBadRequest, "Параметр 'path' обязателен")
		return
	}

	m.service.StreamAudio(w, r, path)
}

// StreamTrack обрабатывает GET /api/tracks/{id}/stream и xHTTP чанки /api/library/stream/{id}/chunks.
func (m *Module) StreamTrack(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		id = httputil.QueryString(r, "id", "")
	}
	if id == "" {
		id = httputil.QueryString(r, "path", "")
	}

	if id == "" {
		httputil.RespondError(w, http.StatusBadRequest, "Параметр 'id' обязателен")
		return
	}

	m.service.StreamTrack(w, r, id)
}

// GetTrackMetadata обрабатывает GET /api/tracks/{id}/metadata.
func (m *Module) GetTrackMetadata(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		id = httputil.QueryString(r, "id", "")
	}

	if id == "" {
		httputil.RespondError(w, http.StatusBadRequest, "Параметр 'id' обязателен")
		return
	}

	meta, err := m.service.GetTrackMetadata(r.Context(), id)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) || errors.Is(err, ErrSandboxViolation) {
			httputil.RespondError(w, http.StatusForbidden, "Доступ запрещен")
			return
		}
		if errors.Is(err, ErrTrackNotFound) || errors.Is(err, ErrFileNotFound) {
			httputil.RespondError(w, http.StatusNotFound, "Трек не найден")
			return
		}
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, meta)
}

// GetTrackCover обрабатывает GET /api/tracks/{id}/cover.
func (m *Module) GetTrackCover(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		id = httputil.QueryString(r, "id", "")
	}

	if id == "" {
		httputil.RespondError(w, http.StatusBadRequest, "Параметр 'id' обязателен")
		return
	}

	data, mimeType, err := m.service.GetTrackCover(r.Context(), id)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) || errors.Is(err, ErrSandboxViolation) {
			httputil.RespondError(w, http.StatusForbidden, "Доступ запрещен")
			return
		}
		httputil.RespondError(w, http.StatusNotFound, "Обложка не найдена")
		return
	}

	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// ListLibraryTracks обрабатывает GET /api/library/tracks.
func (m *Module) ListLibraryTracks(w http.ResponseWriter, r *http.Request) {
	q := httputil.QueryString(r, "q", "")
	if q == "" {
		q = httputil.QueryString(r, "query", "")
	}
	limit := httputil.QueryInt(r, "limit", 50)
	offset := httputil.QueryInt(r, "offset", 0)

	tracks, total, err := m.service.ListLibraryTracks(r.Context(), q, limit, offset)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, "Ошибка получения списка треков: "+err.Error())
		return
	}

	if tracks == nil {
		tracks = []LibraryTrackItem{}
	}

	resp := LibraryTracksResponse{
		Tracks: tracks,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	}

	httputil.RespondJSON(w, http.StatusOK, resp)
}

// SearchLibrary обрабатывает GET /files/search, GET /api/files/search.
func (m *Module) SearchLibrary(w http.ResponseWriter, r *http.Request) {
	q := httputil.QueryString(r, "q", "")
	if q == "" {
		q = httputil.QueryString(r, "query", "")
	}
	limit := httputil.QueryInt(r, "limit", 50)

	if strings.TrimSpace(q) == "" {
		httputil.RespondJSON(w, http.StatusOK, SearchLibraryResult{
			Tracks:  []SearchTrackItem{},
			Albums:  []SearchAlbumItem{},
			Artists: []SearchArtistItem{},
		})
		return
	}

	res, err := m.service.SearchLibrary(r.Context(), q, limit)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, "Ошибка поиска: "+err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, res)
}

// ReorganizeLibrary обрабатывает POST /files/reorganize.
func (m *Module) ReorganizeLibrary(w http.ResponseWriter, r *http.Request) {
	dryRun := httputil.QueryBool(r, "dry_run", false)

	res, err := m.service.ReorganizeLibrary(r.Context(), dryRun)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, "Ошибка реорганизации медиатеки: "+err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, res)
}

// Redownload обрабатывает POST /files/redownload, POST /api/files/redownload (повторное скачивание трека или альбома).
func (m *Module) Redownload(w http.ResponseWriter, r *http.Request) {
	var req RedownloadRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "Некорректный JSON: "+err.Error())
		return
	}

	if strings.TrimSpace(req.Path) == "" {
		httputil.RespondError(w, http.StatusBadRequest, "Поле 'path' обязательно")
		return
	}

	res, err := m.service.Redownload(r.Context(), req)
	if err != nil {
		if errors.Is(err, ErrSandboxViolation) || errors.Is(err, sandbox.ErrPathTraversal) {
			httputil.RespondError(w, http.StatusForbidden, "Доступ запрещен (path traversal)")
			return
		}
		if errors.Is(err, ErrInvalidPath) {
			httputil.RespondError(w, http.StatusBadRequest, err.Error())
			return
		}
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, res)
}

