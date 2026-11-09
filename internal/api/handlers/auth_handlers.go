package handlers

import (
	"encoding/json"
	"net/http"

	"music-manager/internal/config"
	"music-manager/internal/core/auth"
)

type AuthHandler struct {
	cfg *config.Config
}

func NewAuthHandler(cfg *config.Config) *AuthHandler {
	return &AuthHandler{cfg: cfg}
}

type loginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type loginResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	if req.Login != h.cfg.AdminLogin {
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}

	if !auth.CheckPasswordHash(req.Password, h.cfg.AdminPasswordHash) {
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}

	token := auth.CreateSessionToken(req.Login, h.cfg.SecretKey)
	auth.SetSessionCookie(w, token, h.sessionPolicy(r))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(loginResponse{
		Success: true,
		Message: "Авторизация успешна",
	})
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	auth.ClearSessionCookie(w, h.sessionPolicy(r))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(h.sessionPolicy(r).Name)
	if err != nil || cookie.Value == "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"login":         "",
			"authenticated": false,
		})
		return
	}

	login, valid := auth.ValidateSessionToken(cookie.Value, h.cfg.SecretKey, h.sessionPolicy(r))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"login":         login,
		"authenticated": valid,
	})
}

func (h *AuthHandler) sessionPolicy(r *http.Request) auth.Policy {
	name := h.cfg.SessionCookieName
	if name == "" {
		name = auth.CookieName
	}
	return auth.Policy{Name: name, TTL: h.cfg.SessionTTL, Secure: h.cfg.SessionCookieSecure || r.TLS != nil}
}
