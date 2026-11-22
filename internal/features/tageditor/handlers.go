package tageditor

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"music-manager/internal/core/auth"
	"music-manager/internal/core/httputil"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
)

// GetTags обрабатывает GET /tags, GET /tags/, GET /api/tags
// Возвращает ID3v2.4 метаданные для одного файла или для всей папки (альбома).
func (m *Module) GetTags(w http.ResponseWriter, r *http.Request) {
	relPath := strings.TrimSpace(httputil.QueryString(r, "path", ""))
	if relPath == "" {
		httputil.RespondError(w, http.StatusBadRequest, "missing path query param")
		return
	}

	asAlbum := httputil.QueryBool(r, "as_album", false)

	result, err := m.service.GetTags(r.Context(), relPath, asAlbum)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) || errors.Is(err, ErrSandboxViolation) {
			httputil.RespondError(w, http.StatusForbidden, "access denied: invalid path")
		} else if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrFileNotFound) {
			httputil.RespondError(w, http.StatusNotFound, "file not found")
		} else {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	httputil.RespondJSON(w, http.StatusOK, result)
}

// SaveTags обрабатывает POST /tags, POST /tags/, POST /api/tags
// Записывает теги ID3v2.4 UTF-8 в один трек (множественные значения разделяются null-байтами \x00).
func (m *Module) SaveTags(w http.ResponseWriter, r *http.Request) {
	var tags id3.TrackTags
	if err := httputil.DecodeJSON(r, &tags); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json")
		return
	}

	if strings.TrimSpace(tags.Path) == "" {
		httputil.RespondError(w, http.StatusBadRequest, "track path required")
		return
	}

	if err := m.service.SaveTags(r.Context(), tags); err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) || errors.Is(err, ErrSandboxViolation) {
			httputil.RespondError(w, http.StatusForbidden, "access denied: invalid path")
		} else if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrFileNotFound) {
			httputil.RespondError(w, http.StatusNotFound, "file not found")
		} else {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	httputil.RespondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"track":   tags,
	})
}

// BatchSaveTags обрабатывает POST/PUT /tags/batch, POST /puddletag/batch, POST/PUT /api/tags/batch
// Применяет каскадное распространение метаданных альбома, записывает ID3v2.4 теги,
// перемещает файлы через organizer и обновляет БД.
func (m *Module) BatchSaveTags(w http.ResponseWriter, r *http.Request) {
	var req BatchSaveRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json")
		return
	}

	res, err := m.service.BatchSaveTags(r.Context(), req)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) || errors.Is(err, ErrSandboxViolation) {
			httputil.RespondError(w, http.StatusForbidden, "access denied: invalid path")
		} else {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	httputil.RespondJSON(w, http.StatusOK, res)
}

// GetCover обрабатывает GET /tags/cover, GET /api/tags/cover
// Отдает встроенную APIC обложку из MP3 либо файл обложки из каталога.
func (m *Module) GetCover(w http.ResponseWriter, r *http.Request) {
	relPath := strings.TrimSpace(httputil.QueryString(r, "path", ""))
	if relPath == "" {
		httputil.RespondError(w, http.StatusBadRequest, "missing path parameter")
		return
	}

	cover, err := m.service.GetCover(r.Context(), relPath)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) || errors.Is(err, ErrSandboxViolation) {
			httputil.RespondError(w, http.StatusForbidden, "access denied")
		} else if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrCoverNotFound) || errors.Is(err, ErrFileNotFound) {
			httputil.RespondError(w, http.StatusNotFound, "cover art not found")
		} else {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	w.Header().Set("Content-Type", cover.MimeType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(cover.Data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(cover.Data)
}

// SearchCovers обрабатывает GET /tags/covers/search, GET /api/tags/covers/search
// Ищет качественные обложки релизов через каталог Deezer.
func (m *Module) SearchCovers(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(httputil.QueryString(r, "query", ""))
	if query == "" {
		httputil.RespondJSON(w, http.StatusOK, []CoverSearchResultItem{})
		return
	}

	limit := httputil.QueryInt(r, "limit", 12)
	if limit <= 0 || limit > 50 {
		limit = 12
	}

	items, err := m.service.SearchCovers(r.Context(), query, limit)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, "failed to search covers")
		return
	}

	if items == nil {
		items = []CoverSearchResultItem{}
	}
	httputil.RespondJSON(w, http.StatusOK, items)
}

// SearchDeezerAlbums обрабатывает GET /tags/search-deezer-albums, GET /tags/deezer/albums, GET /api/tags/deezer/albums
func (m *Module) SearchDeezerAlbums(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(httputil.QueryString(r, "query", ""))
	if query == "" {
		httputil.RespondError(w, http.StatusBadRequest, "missing query parameter")
		return
	}

	albums, err := m.service.SearchDeezerAlbums(r.Context(), query)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, "deezer search failed")
		return
	}

	httputil.RespondJSON(w, http.StatusOK, albums)
}

// GetDeezerAlbumDetails обрабатывает GET /tags/deezer-album-details, GET /tags/deezer/albums/{id}, GET /api/tags/deezer/albums/{id}
func (m *Module) GetDeezerAlbumDetails(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		id = httputil.QueryString(r, "id", "")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		httputil.RespondError(w, http.StatusBadRequest, "missing id parameter")
		return
	}

	album, err := m.service.GetDeezerAlbumDetails(r.Context(), id)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, "deezer request failed")
		return
	}

	httputil.RespondJSON(w, http.StatusOK, album)
}

// SearchDeezerTracks обрабатывает GET /tags/search-deezer-tracks, GET /tags/deezer/tracks, GET /api/tags/deezer/tracks
func (m *Module) SearchDeezerTracks(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(httputil.QueryString(r, "query", ""))
	if query == "" {
		httputil.RespondError(w, http.StatusBadRequest, "missing query parameter")
		return
	}

	tracks, err := m.service.SearchDeezerTracks(r.Context(), query)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, "deezer track search failed")
		return
	}

	httputil.RespondJSON(w, http.StatusOK, tracks)
}

// GetDeezerTrackDetails обрабатывает GET /tags/deezer-track-details, GET /tags/deezer/tracks/{id}, GET /api/tags/deezer/tracks/{id}
func (m *Module) GetDeezerTrackDetails(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		id = httputil.QueryString(r, "id", "")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		httputil.RespondError(w, http.StatusBadRequest, "missing id parameter")
		return
	}

	track, err := m.service.GetDeezerTrackDetails(r.Context(), id)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, "deezer request failed")
		return
	}

	httputil.RespondJSON(w, http.StatusOK, track)
}

// RecognizeAudio обрабатывает POST /tags/recognize-audio, POST /api/tags/recognize-audio
// Выполняет акустическое распознавание файла через songrec / Shazam с безопасным фолбэком.
func (m *Module) RecognizeAudio(w http.ResponseWriter, r *http.Request) {
	var req RecognizeAudioRequest
	if err := httputil.DecodeJSON(r, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		httputil.RespondJSON(w, http.StatusOK, RecognizeAudioResponse{Recognized: false})
		return
	}

	resp, err := m.service.RecognizeAudio(r.Context(), req.Path)
	if err != nil {
		httputil.RespondJSON(w, http.StatusOK, RecognizeAudioResponse{Recognized: false})
		return
	}

	httputil.RespondJSON(w, http.StatusOK, resp)
}

// SearchCatalog обрабатывает GET /api/catalog/search
// Запускает параллельный поиск по Deezer, MusicBrainz и Shazam.
func (m *Module) SearchCatalog(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(httputil.QueryString(r, "query", ""))
	audioPath := strings.TrimSpace(httputil.QueryString(r, "audio_path", ""))

	if query == "" && audioPath == "" {
		httputil.RespondError(w, http.StatusBadRequest, "missing query or audio_path parameter")
		return
	}

	resp, err := m.service.SearchCatalog(r.Context(), query, audioPath)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, resp)
}

// GetCatalogDeezerAlbum обрабатывает GET /api/catalog/deezer/album/{id}
func (m *Module) GetCatalogDeezerAlbum(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		id = httputil.QueryString(r, "id", "")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		httputil.RespondError(w, http.StatusBadRequest, "missing id parameter")
		return
	}

	details, err := m.service.GetCatalogDeezerAlbum(r.Context(), id)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, details)
}

// GetCatalogMusicBrainzRelease обрабатывает GET /api/catalog/musicbrainz/release/{id}
func (m *Module) GetCatalogMusicBrainzRelease(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		id = httputil.QueryString(r, "id", "")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		httputil.RespondError(w, http.StatusBadRequest, "missing id parameter")
		return
	}

	release, err := m.service.GetCatalogMusicBrainzRelease(r.Context(), id)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, release)
}

// GetCatalogShazamTrack обрабатывает GET /api/catalog/shazam/track/{id}
func (m *Module) GetCatalogShazamTrack(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		id = httputil.QueryString(r, "id", "")
	}
	audioPath := httputil.QueryString(r, "audio_path", "")
	query := httputil.QueryString(r, "query", "")

	track, err := m.service.GetCatalogShazamTrack(r.Context(), id, query, audioPath)
	if err != nil {
		switch {
		case errors.Is(err, ErrSandboxViolation):
			httputil.RespondError(w, http.StatusForbidden, "access denied: invalid path")
		case errors.Is(err, ErrFileNotFound):
			httputil.RespondError(w, http.StatusNotFound, "track not found")
		default:
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	httputil.RespondJSON(w, http.StatusOK, track)
}

// MusicBrainzLogin обрабатывает GET /api/auth/musicbrainz/login
// Инициирует авторизацию пользователя через MusicBrainz OAuth 2.0.
func (m *Module) MusicBrainzLogin(w http.ResponseWriter, r *http.Request) {
	status := m.service.GetMusicBrainzStatus()
	if !status.Configured {
		httputil.RespondError(w, http.StatusBadRequest,
			"MusicBrainz OAuth not configured. Specify MUSICBRAINZ_CLIENT_ID and MUSICBRAINZ_CLIENT_SECRET in .env")
		return
	}

	state := uuid.New().String()
	auth.RegisterOAuthState(state)
	http.SetCookie(w, &http.Cookie{
		Name:     "mb_oauth_state",
		Value:    state,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	redirectURI := m.resolveRedirectURI(r)
	authURL, err := m.service.GetMusicBrainzAuthURL(state, redirectURI)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if httputil.QueryString(r, "format", "") == "json" || strings.Contains(r.Header.Get("Accept"), "application/json") {
		httputil.RespondJSON(w, http.StatusOK, map[string]any{
			"url":          authURL,
			"redirect_uri": redirectURI,
		})
		return
	}

	http.Redirect(w, r, authURL, http.StatusFound)
}

// MusicBrainzCallback обрабатывает GET /api/auth/musicbrainz/callback
// Публичный callback эндпоинт для перенаправления с musicbrainz.org.
func (m *Module) MusicBrainzCallback(w http.ResponseWriter, r *http.Request) {
	if !auth.ConsumeOAuthState(w, r) {
		http.Error(w, "invalid or expired OAuth state", http.StatusForbidden)
		return
	}

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		errDesc := r.URL.Query().Get("error_description")
		if errDesc == "" {
			errDesc = errParam
		}
		renderOAuthErrorPage(w, fmt.Sprintf("Authorization cancelled or denied: %s", html.EscapeString(errDesc)))
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		renderOAuthErrorPage(w, "Missing required authorization code in callback")
		return
	}

	redirectURI := m.resolveRedirectURI(r)
	username, err := m.service.HandleMusicBrainzCallback(r.Context(), code, redirectURI)
	if err != nil {
		renderOAuthErrorPage(w, fmt.Sprintf("Token exchange failed: %s", html.EscapeString(err.Error())))
		return
	}

	// Сброс state cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "mb_oauth_state",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	renderOAuthSuccessPage(w, username)
}

// MusicBrainzStatus обрабатывает GET /api/auth/musicbrainz/status
func (m *Module) MusicBrainzStatus(w http.ResponseWriter, r *http.Request) {
	status := m.service.GetMusicBrainzStatus()
	httputil.RespondJSON(w, http.StatusOK, status)
}

// MusicBrainzDisconnect обрабатывает POST /api/auth/musicbrainz/disconnect
func (m *Module) MusicBrainzDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := m.service.DisconnectMusicBrainz(r.Context()); err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, map[string]any{"success": true})
}

// RenamePattern обрабатывает POST /files/rename-pattern, POST /puddletag/rename-pattern, POST /api/tags/rename-pattern
// Выполняет пакетное переименование файлов по шаблону с перемещением сопутствующих .lrc и .txt файлов.
func (m *Module) RenamePattern(w http.ResponseWriter, r *http.Request) {
	var req RenamePatternRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	results, err := m.service.RenamePattern(r.Context(), req)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) || errors.Is(err, ErrSandboxViolation) {
			httputil.RespondError(w, http.StatusForbidden, "access denied")
		} else {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	httputil.RespondJSON(w, http.StatusOK, results)
}

// resolveRedirectURI определяет URL обратного вызова для OAuth.
func (m *Module) resolveRedirectURI(r *http.Request) string {
	if configured := m.service.ConfiguredRedirectURI(); configured != "" {
		return configured
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/api/auth/musicbrainz/callback", scheme, r.Host)
}

func renderOAuthErrorPage(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	htmlTmpl := `<!DOCTYPE html>
<html lang="ru">
<head>
  <meta charset="utf-8">
  <title>MusicBrainz OAuth — Ошибка</title>
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #0f172a; color: #f8fafc; display: flex; align-items: center; justify-content: center; min-height: 100vh; margin: 0; padding: 20px; box-sizing: border-box; }
    .card { background: #1e293b; border: 1px solid #334155; border-radius: 16px; padding: 32px; max-width: 480px; width: 100%; text-align: center; box-shadow: 0 20px 25px -5px rgba(0,0,0,0.5); }
    h2 { color: #f87171; margin-top: 0; font-size: 20px; }
    p { color: #94a3b8; font-size: 14px; line-height: 1.5; margin: 16px 0 24px; word-break: break-word; }
    .btn { display: inline-block; background: #38bdf8; color: #0f172a; padding: 10px 24px; border-radius: 10px; font-weight: 600; text-decoration: none; font-size: 14px; }
  </style>
</head>
<body>
  <div class="card">
    <div style="font-size: 40px; margin-bottom: 12px;">⚠️</div>
    <h2>Ошибка подключения MusicBrainz</h2>
    <p>` + errMsg + `</p>
    <a href="/" class="btn">Вернуться в приложение</a>
  </div>
</body>
</html>`
	_, _ = w.Write([]byte(htmlTmpl))
}

func renderOAuthSuccessPage(w http.ResponseWriter, username string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	userText := ""
	if username != "" {
		userText = fmt.Sprintf(" (аккаунт <b>%s</b>)", html.EscapeString(username))
	}
	htmlTmpl := `<!DOCTYPE html>
<html lang="ru">
<head>
  <meta charset="utf-8">
  <title>MusicBrainz OAuth — Успешно</title>
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <script>
    if (window.opener) {
      window.opener.postMessage({ type: 'mb_oauth_success', username: USERNAME_JSON }, '*');
      setTimeout(function() { window.close(); }, 1200);
    } else {
      setTimeout(function() { window.location.href = '/?mb_connected=1'; }, 1500);
    }
  </script>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #0f172a; color: #f8fafc; display: flex; align-items: center; justify-content: center; min-height: 100vh; margin: 0; padding: 20px; box-sizing: border-box; }
    .card { background: #1e293b; border: 1px solid #334155; border-radius: 16px; padding: 32px; max-width: 480px; width: 100%; text-align: center; box-shadow: 0 20px 25px -5px rgba(0,0,0,0.5); }
    h2 { color: #34d399; margin-top: 0; font-size: 20px; }
    p { color: #94a3b8; font-size: 14px; line-height: 1.5; margin: 16px 0 24px; }
    .btn { display: inline-block; background: #38bdf8; color: #0f172a; padding: 10px 24px; border-radius: 10px; font-weight: 600; text-decoration: none; font-size: 14px; }
  </style>
</head>
<body>
  <div class="card">
    <div style="font-size: 40px; margin-bottom: 12px;">🎉</div>
    <h2>MusicBrainz успешно подключен!</h2>
    <p>Аккаунт MusicBrainz авторизован` + userText + `. Лимиты запросов повышены.<br>Окно закроется автоматически...</p>
    <a href="/?mb_connected=1" class="btn">Вернуться в приложение</a>
  </div>
</body>
</html>`
	userJSON, _ := json.Marshal(username)
	htmlTmpl = strings.ReplaceAll(htmlTmpl, "USERNAME_JSON", string(userJSON))
	_, _ = w.Write([]byte(htmlTmpl))
}
