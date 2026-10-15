package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestAuth_HashAndCheck(t *testing.T) {
	pw := "secret_password"
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	if !CheckPasswordHash(pw, hash) {
		t.Errorf("CheckPasswordHash failed for correct password")
	}

	if CheckPasswordHash("wrong_password", hash) {
		t.Errorf("CheckPasswordHash succeeded for wrong password")
	}
}

func TestAuth_SessionTokens(t *testing.T) {
	secret := "test_secret_key_123"
	login := "admin"

	token := CreateSessionToken(login, secret)
	extractedLogin, valid := ValidateSessionToken(token, secret)
	if !valid || extractedLogin != login {
		t.Fatalf("expected valid token with login %q, got %q, valid: %v", login, extractedLogin, valid)
	}

	// Tampered token
	_, validTampered := ValidateSessionToken(token+"tampered", secret)
	if validTampered {
		t.Errorf("tampered token should be invalid")
	}

	// Different secret
	_, validDiffSecret := ValidateSessionToken(token, "different_secret")
	if validDiffSecret {
		t.Errorf("token with different secret should be invalid")
	}

	// Malformed tokens
	malformed := []string{
		"",
		"singlepart",
		"two:parts",
		"one:two:three:four",
		"login:not_a_number:signature",
	}
	for _, m := range malformed {
		if _, ok := ValidateSessionToken(m, secret); ok {
			t.Errorf("expected malformed token %q to be invalid", m)
		}
	}
}

func TestAuth_FutureAndExpiredTimestamps(t *testing.T) {
	secret := "test_secret_key_123"
	login := "admin"

	signPayload := func(payload string) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(payload))
		sig := hex.EncodeToString(mac.Sum(nil))
		return payload + ":" + sig
	}

	// 1. Future timestamp (> 300 seconds in future)
	futureTs := strconv.FormatInt(time.Now().Unix()+400, 10)
	futureToken := signPayload(login + ":" + futureTs)
	if _, valid := ValidateSessionToken(futureToken, secret); valid {
		t.Errorf("token with future timestamp should be rejected as invalid")
	}

	// 2. Acceptable future timestamp (within 300 seconds, e.g. slight clock drift)
	nearFutureTs := strconv.FormatInt(time.Now().Unix()+60, 10)
	nearFutureToken := signPayload(login + ":" + nearFutureTs)
	if _, valid := ValidateSessionToken(nearFutureToken, secret); !valid {
		t.Errorf("token within clock drift allowance (+60s) should be valid")
	}

	// 3. Expired timestamp (> 7 days)
	expiredTs := strconv.FormatInt(time.Now().Unix()-(8*24*3600), 10)
	expiredToken := signPayload(login + ":" + expiredTs)
	if _, valid := ValidateSessionToken(expiredToken, secret); valid {
		t.Errorf("token older than 7 days should be rejected as expired")
	}

	// 4. Valid timestamp (6 days old)
	sixDaysOldTs := strconv.FormatInt(time.Now().Unix()-(6*24*3600), 10)
	validToken := signPayload(login + ":" + sixDaysOldTs)
	if _, valid := ValidateSessionToken(validToken, secret); !valid {
		t.Errorf("token within 7 days should be valid")
	}
}

func TestAuth_UsernameSanitization(t *testing.T) {
	secret := "test_secret_key_123"
	// User with colons should have colons replaced with underscore
	login := "user:with:colons"
	token := CreateSessionToken(login, secret)

	extractedLogin, valid := ValidateSessionToken(token, secret)
	if !valid {
		t.Fatalf("expected token to be valid")
	}
	if extractedLogin != "user_with_colons" {
		t.Errorf("expected clean login user_with_colons, got %q", extractedLogin)
	}
}

func TestAuth_Cookies(t *testing.T) {
	// 1. SetSessionCookie
	w := httptest.NewRecorder()
	SetSessionCookie(w, "test_token_val")

	resp := w.Result()
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected at least one cookie")
	}

	var sessCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == CookieName {
			sessCookie = c
			break
		}
	}

	if sessCookie == nil {
		t.Fatalf("expected cookie with name %s", CookieName)
	}
	if sessCookie.Value != "test_token_val" {
		t.Errorf("expected value test_token_val, got %s", sessCookie.Value)
	}
	if !sessCookie.HttpOnly {
		t.Errorf("expected HttpOnly=true")
	}
	if sessCookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("expected SameSite=Strict")
	}
	if sessCookie.Path != "/" {
		t.Errorf("expected Path=/, got %s", sessCookie.Path)
	}
	if sessCookie.MaxAge != 7*24*3600 {
		t.Errorf("expected MaxAge=604800, got %d", sessCookie.MaxAge)
	}

	// 2. ClearSessionCookie
	w2 := httptest.NewRecorder()
	ClearSessionCookie(w2)

	resp2 := w2.Result()
	cookies2 := resp2.Cookies()
	var clearCookie *http.Cookie
	for _, c := range cookies2 {
		if c.Name == CookieName {
			clearCookie = c
			break
		}
	}

	if clearCookie == nil {
		t.Fatalf("expected clear cookie")
	}
	if clearCookie.Value != "" || clearCookie.MaxAge != -1 {
		t.Errorf("expected cleared cookie with MaxAge=-1 and empty value, got val=%q maxage=%d", clearCookie.Value, clearCookie.MaxAge)
	}
}

func TestAuth_Middleware(t *testing.T) {
	secret := "secret_key_mid"
	mw := Middleware(secret)

	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("protected_ok"))
	})
	protected := mw(dummyHandler)

	validToken := CreateSessionToken("admin", secret)

	// 1. Valid Cookie
	req1 := httptest.NewRequest("GET", "/protected", nil)
	req1.AddCookie(&http.Cookie{Name: CookieName, Value: validToken})
	rec1 := httptest.NewRecorder()
	protected.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK || rec1.Body.String() != "protected_ok" {
		t.Errorf("expected 200 OK with cookie, got %d, body: %s", rec1.Code, rec1.Body.String())
	}

	// 2. Valid Query Parameter (?token=...)
	req2 := httptest.NewRequest("GET", fmt.Sprintf("/protected?token=%s", validToken), nil)
	rec2 := httptest.NewRecorder()
	protected.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || rec2.Body.String() != "protected_ok" {
		t.Errorf("expected 200 OK with query token, got %d, body: %s", rec2.Code, rec2.Body.String())
	}

	// 3. Valid Authorization Bearer Header
	req3 := httptest.NewRequest("GET", "/protected", nil)
	req3.Header.Set("Authorization", "Bearer "+validToken)
	rec3 := httptest.NewRecorder()
	protected.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK || rec3.Body.String() != "protected_ok" {
		t.Errorf("expected 200 OK with Bearer header, got %d, body: %s", rec3.Code, rec3.Body.String())
	}

	// 4. Missing token -> 401 Unauthorized
	req4 := httptest.NewRequest("GET", "/protected", nil)
	rec4 := httptest.NewRecorder()
	protected.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing token, got %d", rec4.Code)
	}

	// 5. Invalid token -> 401 Unauthorized
	req5 := httptest.NewRequest("GET", "/protected", nil)
	req5.AddCookie(&http.Cookie{Name: CookieName, Value: "invalid:fake:token"})
	rec5 := httptest.NewRecorder()
	protected.ServeHTTP(rec5, req5)
	if rec5.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid token, got %d", rec5.Code)
	}
}
