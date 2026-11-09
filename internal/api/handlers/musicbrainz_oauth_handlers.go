package handlers

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"music-manager/internal/core/auth"
)

// MusicBrainzLogin инициирует OAuth 2.0 авторизацию пользователя на MusicBrainz.
// Перенаправляет браузер на официальную страницу авторизации MusicBrainz с необходимыми правами.
func (h *TagHandler) MusicBrainzLogin(w http.ResponseWriter, r *http.Request) {
	if h.musicbrainz == nil {
		http.Error(w, `{"error":"MusicBrainz клиент не инициализирован"}`, http.StatusInternalServerError)
		return
	}

	status := h.musicbrainz.GetOAuthStatus()
	if !status.Configured {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "MusicBrainz OAuth не настроен. Укажите MUSICBRAINZ_CLIENT_ID и MUSICBRAINZ_CLIENT_SECRET в файле .env",
		})
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

	redirectURI := ""
	if h.cfg != nil && h.cfg.MusicBrainzRedirectURI != "" {
		redirectURI = h.cfg.MusicBrainzRedirectURI
	} else {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		redirectURI = fmt.Sprintf("%s://%s/api/auth/musicbrainz/callback", scheme, r.Host)
	}

	authURL := h.musicbrainz.GetAuthorizationURL(state, redirectURI)

	if r.URL.Query().Get("format") == "json" || strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"url":          authURL,
			"redirect_uri": redirectURI,
		})
		return
	}

	http.Redirect(w, r, authURL, http.StatusFound)
}

// MusicBrainzCallback обрабатывает перенаправление от MusicBrainz OAuth с кодом авторизации.
func (h *TagHandler) MusicBrainzCallback(w http.ResponseWriter, r *http.Request) {
	if !auth.ConsumeOAuthState(w, r) {
		http.Error(w, "invalid or expired OAuth state", http.StatusForbidden)
		return
	}

	if h.musicbrainz == nil {
		http.Error(w, "MusicBrainz client is not initialized", http.StatusInternalServerError)
		return
	}

	// 1. Проверяем ошибку от провайдера OAuth
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		errDesc := r.URL.Query().Get("error_description")
		if errDesc == "" {
			errDesc = errParam
		}
		renderOAuthErrorPage(w, fmt.Sprintf("Авторизация MusicBrainz отменена или отклонена: %s", html.EscapeString(errDesc)))
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		renderOAuthErrorPage(w, "В запросе обратного вызова отсутствует обязательный параметр code")
		return
	}

	// 2. Формируем redirectURI, идентичный переданному при логине
	redirectURI := ""
	if h.cfg != nil && h.cfg.MusicBrainzRedirectURI != "" {
		redirectURI = h.cfg.MusicBrainzRedirectURI
	} else {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		redirectURI = fmt.Sprintf("%s://%s/api/auth/musicbrainz/callback", scheme, r.Host)
	}

	// 3. Обмениваем код на токен
	_, err := h.musicbrainz.ExchangeCode(r.Context(), code, redirectURI)
	if err != nil {
		renderOAuthErrorPage(w, fmt.Sprintf("Сбой обмена кода на токен MusicBrainz: %s", html.EscapeString(err.Error())))
		return
	}

	// 4. Очищаем state cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "mb_oauth_state",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	st := h.musicbrainz.GetOAuthStatus()
	renderOAuthSuccessPage(w, st.Username)
}

// MusicBrainzStatus возвращает JSON с текущим состоянием подключения OAuth 2.0.
func (h *TagHandler) MusicBrainzStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.musicbrainz == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"configured": false,
			"connected":  false,
		})
		return
	}
	status := h.musicbrainz.GetOAuthStatus()
	_ = json.NewEncoder(w).Encode(status)
}

// MusicBrainzDisconnect отключает аккаунт MusicBrainz и сбрасывает токены.
func (h *TagHandler) MusicBrainzDisconnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.musicbrainz == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		return
	}
	if err := h.musicbrainz.Disconnect(r.Context()); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
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
    .btn { display: inline-block; background: #38bdf8; color: #0f172a; padding: 10px 24px; border-radius: 10px; font-weight: 600; text-decoration: none; font-size: 14px; transition: opacity 0.2s; }
    .btn:hover { opacity: 0.9; }
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
