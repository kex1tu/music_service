package handlers

import (
	"music-manager/internal/config"
	"music-manager/internal/core/auth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginHTTPSUsesConfiguredCookie(t *testing.T) {
	hash, err := auth.HashPassword("fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	h := NewAuthHandler(&config.Config{AdminLogin: "admin", AdminPasswordHash: hash, SecretKey: "fixture-session-key", SessionCookieName: "custom_session", SessionTTL: 60})
	r := httptest.NewRequest("POST", "https://example.test/login", strings.NewReader(`{"login":"admin","password":"fixture-password"}`))
	w := httptest.NewRecorder()
	h.Login(w, r)
	if w.Code != 200 {
		t.Fatalf("login: %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("expected a session cookie")
	}
	c := cookies[0]
	if c.Name != "custom_session" || c.MaxAge != 60 || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("bad session cookie: %+v", c)
	}
}
