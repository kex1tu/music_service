package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"music-manager/internal/config"
	"music-manager/internal/core/auth"
	"music-manager/internal/services/resolver"
)

func TestMusicBrainzOAuthHandlers_LoginUnconfigured(t *testing.T) {
	tagHandler := NewTagHandler(nil, nil, nil, nil)
	mbClient := resolver.NewMusicBrainzClient("")
	tagHandler.SetMusicBrainz(mbClient)
	tagHandler.SetConfig(&config.Config{})

	req := httptest.NewRequest("GET", "/api/auth/musicbrainz/login", nil)
	w := httptest.NewRecorder()

	tagHandler.MusicBrainzLogin(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when unconfigured, got %d", w.Code)
	}
	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !strings.Contains(resp["error"], "MUSICBRAINZ_CLIENT_ID") {
		t.Errorf("expected error message mentioning MUSICBRAINZ_CLIENT_ID, got %s", resp["error"])
	}
}

func TestMusicBrainzOAuthHandlers_LoginConfigured(t *testing.T) {
	tagHandler := NewTagHandler(nil, nil, nil, nil)
	mbClient := resolver.NewMusicBrainzClient("")
	mbClient.SetOAuthConfig("cid-123", "csec-456", "https://music.test/api/auth/musicbrainz/callback")
	tagHandler.SetMusicBrainz(mbClient)
	tagHandler.SetConfig(&config.Config{
		MusicBrainzClientID:     "cid-123",
		MusicBrainzClientSecret: "csec-456",
		MusicBrainzRedirectURI:  "https://music.test/api/auth/musicbrainz/callback",
	})

	// 1. Browser navigation redirect
	req := httptest.NewRequest("GET", "/api/auth/musicbrainz/login", nil)
	w := httptest.NewRecorder()

	tagHandler.MusicBrainzLogin(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302 Found, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "https://musicbrainz.org/oauth2/authorize") {
		t.Errorf("unexpected redirect location: %s", loc)
	}
	if !strings.Contains(loc, "client_id=cid-123") {
		t.Errorf("missing client_id in location: %s", loc)
	}

	// Verify state cookie is set
	cookies := w.Result().Cookies()
	var stateCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "mb_oauth_state" {
			stateCookie = c
			break
		}
	}
	if stateCookie == nil || stateCookie.Value == "" {
		t.Errorf("state cookie was not set")
	}

	// 2. JSON format request
	reqJSON := httptest.NewRequest("GET", "/api/auth/musicbrainz/login?format=json", nil)
	wJSON := httptest.NewRecorder()

	tagHandler.MusicBrainzLogin(wJSON, reqJSON)

	if wJSON.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for JSON, got %d", wJSON.Code)
	}
	var jsonResp map[string]string
	if err := json.Unmarshal(wJSON.Body.Bytes(), &jsonResp); err != nil {
		t.Fatalf("failed to parse JSON response: %v", err)
	}
	if !strings.Contains(jsonResp["url"], "client_id=cid-123") {
		t.Errorf("expected URL in JSON, got %v", jsonResp)
	}
}

func TestMusicBrainzOAuthHandlers_CallbackAndStatusAndDisconnect(t *testing.T) {
	// Mock MusicBrainz OAuth server
	mockMB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			code := r.FormValue("code")
			if code != "valid-code-777" {
				http.Error(w, `{"error":"invalid_code"}`, http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{
				"access_token": "bearer-tok-abc",
				"token_type": "Bearer",
				"expires_in": 3600,
				"refresh_token": "refresh-tok-def"
			}`)
		case "/userinfo":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{
				"sub": "alice_listener"
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockMB.Close()

	tagHandler := NewTagHandler(nil, nil, nil, nil)
	mbClient := resolver.NewMusicBrainzClient("")
	mbClient.SetOAuthBaseURL(mockMB.URL)
	mbClient.SetOAuthConfig("cid-123", "csec-456", "https://music.test/api/auth/musicbrainz/callback")
	tagHandler.SetMusicBrainz(mbClient)
	tagHandler.SetConfig(&config.Config{
		MusicBrainzClientID:     "cid-123",
		MusicBrainzClientSecret: "csec-456",
		MusicBrainzRedirectURI:  "https://music.test/api/auth/musicbrainz/callback",
	})

	// 1. Initial Status -> not connected
	wStatus := httptest.NewRecorder()
	tagHandler.MusicBrainzStatus(wStatus, httptest.NewRequest("GET", "/api/auth/musicbrainz/status", nil))
	var st resolver.MusicBrainzOAuthStatus
	_ = json.Unmarshal(wStatus.Body.Bytes(), &st)
	if !st.Configured || st.Connected {
		t.Errorf("expected configured=true, connected=false, got %+v", st)
	}

	// 2. Callback with error
	wErr := httptest.NewRecorder()
	reqErr := httptest.NewRequest("GET", "/api/auth/musicbrainz/callback?error=access_denied&error_description=User+cancelled", nil)
	auth.RegisterOAuthState("fixture-reqErr")
	reqErr.AddCookie(&http.Cookie{Name: "mb_oauth_state", Value: "fixture-reqErr"})
	qreqErr := reqErr.URL.Query()
	qreqErr.Set("state", "fixture-reqErr")
	reqErr.URL.RawQuery = qreqErr.Encode()
	tagHandler.MusicBrainzCallback(wErr, reqErr)
	if wErr.Code != http.StatusBadRequest || !strings.Contains(wErr.Body.String(), "отменена или отклонена") {
		t.Errorf("unexpected error handling: %d, body: %s", wErr.Code, wErr.Body.String())
	}

	// 3. Callback with missing code
	wNoCode := httptest.NewRecorder()
	reqNoCode := httptest.NewRequest("GET", "/api/auth/musicbrainz/callback", nil)
	auth.RegisterOAuthState("fixture-reqNoCode")
	reqNoCode.AddCookie(&http.Cookie{Name: "mb_oauth_state", Value: "fixture-reqNoCode"})
	qreqNoCode := reqNoCode.URL.Query()
	qreqNoCode.Set("state", "fixture-reqNoCode")
	reqNoCode.URL.RawQuery = qreqNoCode.Encode()
	tagHandler.MusicBrainzCallback(wNoCode, reqNoCode)
	if wNoCode.Code != http.StatusBadRequest || !strings.Contains(wNoCode.Body.String(), "отсутствует") {
		t.Errorf("unexpected missing code handling: %d", wNoCode.Code)
	}

	// 4. Callback success
	wSuccess := httptest.NewRecorder()
	reqSuccess := httptest.NewRequest("GET", "/api/auth/musicbrainz/callback?code=valid-code-777", nil)
	auth.RegisterOAuthState("fixture-reqSuccess")
	reqSuccess.AddCookie(&http.Cookie{Name: "mb_oauth_state", Value: "fixture-reqSuccess"})
	qreqSuccess := reqSuccess.URL.Query()
	qreqSuccess.Set("state", "fixture-reqSuccess")
	reqSuccess.URL.RawQuery = qreqSuccess.Encode()
	tagHandler.MusicBrainzCallback(wSuccess, reqSuccess)
	if wSuccess.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on successful callback, got %d", wSuccess.Code)
	}
	body := wSuccess.Body.String()
	if !strings.Contains(body, "alice_listener") || !strings.Contains(body, "MusicBrainz успешно подключен") {
		t.Errorf("expected success page with username, got: %s", body)
	}

	// 5. Status -> connected with username
	wStatus2 := httptest.NewRecorder()
	tagHandler.MusicBrainzStatus(wStatus2, httptest.NewRequest("GET", "/api/auth/musicbrainz/status", nil))
	var st2 resolver.MusicBrainzOAuthStatus
	_ = json.Unmarshal(wStatus2.Body.Bytes(), &st2)
	if !st2.Connected || st2.Username != "alice_listener" {
		t.Errorf("expected connected=true with alice_listener, got %+v", st2)
	}

	// 6. Disconnect
	wDisc := httptest.NewRecorder()
	tagHandler.MusicBrainzDisconnect(wDisc, httptest.NewRequest("POST", "/api/auth/musicbrainz/disconnect", nil))
	if wDisc.Code != http.StatusOK {
		t.Errorf("expected 200 OK on disconnect, got %d", wDisc.Code)
	}

	// 7. Status -> connected=false
	wStatus3 := httptest.NewRecorder()
	tagHandler.MusicBrainzStatus(wStatus3, httptest.NewRequest("GET", "/api/auth/musicbrainz/status", nil))
	var st3 resolver.MusicBrainzOAuthStatus
	_ = json.Unmarshal(wStatus3.Body.Bytes(), &st3)
	if st3.Connected || st3.Username != "" {
		t.Errorf("expected disconnected status, got %+v", st3)
	}
}
