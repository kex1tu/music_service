package resolver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMusicBrainzClient_SearchReleases(t *testing.T) {
	var requestCount int32
	var capturedAuth string
	var capturedUserAgent string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		capturedAuth = r.Header.Get("Authorization")
		capturedUserAgent = r.Header.Get("User-Agent")

		if !strings.Contains(r.URL.Path, "/release") {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"count": 1,
			"releases": [
				{
					"id": "mbid-12345",
					"score": 100,
					"title": "A Night at the Opera",
					"status": "Official",
					"date": "1975-11-21",
					"country": "GB",
					"track-count": 12,
					"artist-credit": [
						{"name": "Queen", "joinphrase": " feat. "},
						{"name": "David Bowie", "joinphrase": ""}
					],
					"cover-art-archive": {
						"front": true,
						"count": 1
					},
					"label-info": [
						{
							"label": {"name": "EMI"}
						}
					]
				}
			]
		}`)
	}))
	defer server.Close()

	client := NewMusicBrainzClient("test-token-xyz")
	client.SetBaseURL(server.URL)
	client.SetCoverArtBaseURL("http://mock-cover.art/release")
	client.minInterval = 0 // Для быстрого выполнения тестов

	ctx := context.Background()
	results, err := client.SearchReleases(ctx, "Queen")
	if err != nil {
		t.Fatalf("unexpected search error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	rel := results[0]
	if rel.ID != "mbid-12345" {
		t.Errorf("expected ID mbid-12345, got %s", rel.ID)
	}
	if rel.Title != "A Night at the Opera" {
		t.Errorf("expected title 'A Night at the Opera', got %s", rel.Title)
	}
	if rel.Artist != "Queen\\\\David Bowie" {
		t.Errorf("expected artist 'Queen\\\\David Bowie', got %s", rel.Artist)
	}
	if rel.Year != 1975 {
		t.Errorf("expected year 1975, got %d", rel.Year)
	}
	if rel.Label != "EMI" {
		t.Errorf("expected label EMI, got %s", rel.Label)
	}
	if rel.CoverURL != "http://mock-cover.art/release/mbid-12345/front-500.jpg" {
		t.Errorf("expected cover URL http://mock-cover.art/release/mbid-12345/front-500.jpg, got %s", rel.CoverURL)
	}

	if capturedAuth != "Bearer test-token-xyz" {
		t.Errorf("expected auth Bearer test-token-xyz, got %s", capturedAuth)
	}
	if !strings.Contains(capturedUserAgent, "MusicManager") {
		t.Errorf("expected user-agent with MusicManager, got %s", capturedUserAgent)
	}

	// Проверяем кэширование: второй запрос должен вернуть те же данные без повторного сетевого вызова
	cachedResults, err := client.SearchReleases(ctx, "Queen")
	if err != nil {
		t.Fatalf("unexpected cached search error: %v", err)
	}
	if len(cachedResults) != 1 {
		t.Fatalf("expected 1 cached result, got %d", len(cachedResults))
	}
	if atomic.LoadInt32(&requestCount) != 1 {
		t.Errorf("expected request count 1 due to cache, got %d", atomic.LoadInt32(&requestCount))
	}
}

func TestMusicBrainzClient_GetRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/release/mbid-album-999") {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id": "mbid-album-999",
			"title": "Random Access Memories",
			"date": "2013-05-17",
			"artist-credit": [
				{"name": "Daft Punk", "joinphrase": ""}
			],
			"cover-art-archive": {
				"front": true,
				"count": 2
			},
			"genres": [
				{"name": "Electronic"},
				{"name": "Disco"}
			],
			"label-info": [
				{"label": {"name": "Columbia"}}
			],
			"media": [
				{
					"position": 1,
					"format": "CD",
					"track-count": 2,
					"tracks": [
						{
							"id": "t-1",
							"position": 1,
							"number": "1",
							"title": "Give Life Back to Music",
							"length": 275000,
							"artist-credit": [
								{"name": "Daft Punk", "joinphrase": ""}
							],
							"recording": {
								"id": "rec-1",
								"title": "Give Life Back to Music",
								"isrcs": ["USQX91300105"]
							}
						},
						{
							"id": "t-2",
							"position": 2,
							"number": "2",
							"title": "Get Lucky",
							"length": 369000,
							"artist-credit": [
								{"name": "Daft Punk", "joinphrase": " feat. "},
								{"name": "Pharrell Williams", "joinphrase": ""}
							],
							"recording": {
								"id": "rec-2",
								"title": "Get Lucky",
								"isrcs": ["USQX91300108"]
							}
						}
					]
				}
			]
		}`)
	}))
	defer server.Close()

	client := NewMusicBrainzClient("")
	client.SetBaseURL(server.URL)
	client.SetCoverArtBaseURL("https://coverartarchive.org/release")
	client.minInterval = 0

	ctx := context.Background()
	rel, err := client.GetRelease(ctx, "mbid-album-999")
	if err != nil {
		t.Fatalf("unexpected GetRelease error: %v", err)
	}

	if rel.ID != "mbid-album-999" {
		t.Errorf("expected ID mbid-album-999, got %s", rel.ID)
	}
	if rel.Title != "Random Access Memories" {
		t.Errorf("expected title 'Random Access Memories', got %s", rel.Title)
	}
	if rel.Artist != "Daft Punk" {
		t.Errorf("expected artist 'Daft Punk', got %s", rel.Artist)
	}
	if rel.Year != 2013 {
		t.Errorf("expected year 2013, got %d", rel.Year)
	}
	if rel.Genre != "Electronic" {
		t.Errorf("expected genre 'Electronic', got %s", rel.Genre)
	}
	if len(rel.Tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(rel.Tracks))
	}

	t1 := rel.Tracks[0]
	if t1.TrackNumber != 1 || t1.DiscNumber != 1 || t1.Title != "Give Life Back to Music" {
		t.Errorf("track 1 mismatch: %+v", t1)
	}
	if t1.Duration != 275 {
		t.Errorf("expected duration 275s, got %d", t1.Duration)
	}
	if t1.ISRC != "USQX91300105" {
		t.Errorf("expected ISRC USQX91300105, got %s", t1.ISRC)
	}

	t2 := rel.Tracks[1]
	if t2.Title != "Get Lucky" || t2.Artist != "Daft Punk\\\\Pharrell Williams" {
		t.Errorf("track 2 mismatch: %+v", t2)
	}
	if len(t2.Artists) != 2 || t2.Artists[1] != "Pharrell Williams" {
		t.Errorf("expected 2 artists in slice, got %+v", t2.Artists)
	}
}

func TestMusicBrainzClient_ErrorHandling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error": "rate limit exceeded"}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := NewMusicBrainzClient("")
	client.SetBaseURL(server.URL)
	client.minInterval = 0

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := client.SearchReleases(ctx, "Queen")
	if err == nil {
		t.Fatal("expected error on 503, got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("expected error containing 503, got %v", err)
	}
}

type mockTokenPersistence struct {
	savedAccess  string
	savedRefresh string
	savedExpires time.Time
	savedUser    string
	cleared      bool
}

func (m *mockTokenPersistence) SaveTokens(ctx context.Context, accessToken, refreshToken string, expiresAt time.Time, username string) error {
	m.savedAccess = accessToken
	m.savedRefresh = refreshToken
	m.savedExpires = expiresAt
	m.savedUser = username
	m.cleared = false
	return nil
}

func (m *mockTokenPersistence) ClearTokens(ctx context.Context) error {
	m.savedAccess = ""
	m.savedRefresh = ""
	m.savedUser = ""
	m.cleared = true
	return nil
}

func TestMusicBrainzOAuth_AuthorizationURL(t *testing.T) {
	client := NewMusicBrainzClient("")
	client.SetOAuthConfig("my-client-id", "my-client-secret", "https://example.com/callback")

	urlStr := client.GetAuthorizationURL("state-random-123")
	if !strings.Contains(urlStr, "https://musicbrainz.org/oauth2/authorize") {
		t.Errorf("unexpected authorize URL base: %s", urlStr)
	}
	if !strings.Contains(urlStr, "client_id=my-client-id") {
		t.Errorf("missing client_id in URL: %s", urlStr)
	}
	if !strings.Contains(urlStr, "state=state-random-123") {
		t.Errorf("missing state in URL: %s", urlStr)
	}
	if !strings.Contains(urlStr, "redirect_uri=https%3A%2F%2Fexample.com%2Fcallback") {
		t.Errorf("missing redirect_uri in URL: %s", urlStr)
	}

	// Test override redirect URI
	overrideURL := client.GetAuthorizationURL("st2", "https://other.com/oauth")
	if !strings.Contains(overrideURL, "redirect_uri=https%3A%2F%2Fother.com%2Foauth") {
		t.Errorf("override redirect_uri failed: %s", overrideURL)
	}
}

func TestMusicBrainzOAuth_ExchangeAndUserInfoAndRefresh(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.Method != "POST" {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			grantType := r.FormValue("grant_type")
			if grantType == "authorization_code" {
				code := r.FormValue("code")
				clientID := r.FormValue("client_id")
				clientSecret := r.FormValue("client_secret")
				if code != "auth-code-123" || clientID != "cid" || clientSecret != "csec" {
					http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{
					"access_token": "acc-token-999",
					"token_type": "Bearer",
					"expires_in": 3600,
					"refresh_token": "ref-token-888"
				}`)
				return
			} else if grantType == "refresh_token" {
				rf := r.FormValue("refresh_token")
				if rf != "ref-token-888" {
					http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{
					"access_token": "acc-token-new",
					"token_type": "Bearer",
					"expires_in": 3600,
					"refresh_token": "ref-token-new"
				}`)
				return
			}
			http.Error(w, "unknown grant", http.StatusBadRequest)

		case "/userinfo":
			auth := r.Header.Get("Authorization")
			if auth != "Bearer acc-token-999" && auth != "Bearer acc-token-new" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{
				"sub": "musiclover",
				"profile": "https://musicbrainz.org/user/musiclover"
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	client := NewMusicBrainzClient("")
	client.SetOAuthBaseURL(mockServer.URL)
	client.SetOAuthConfig("cid", "csec", "https://app.test/cb")
	client.minInterval = 0

	persistence := &mockTokenPersistence{}
	client.SetPersistence(persistence)

	ctx := context.Background()

	// Initial status: not connected, configured
	st := client.GetOAuthStatus()
	if !st.Configured || st.Connected {
		t.Fatalf("unexpected initial status: %+v", st)
	}

	// 1. ExchangeCode
	resp, err := client.ExchangeCode(ctx, "auth-code-123", "https://app.test/cb")
	if err != nil {
		t.Fatalf("ExchangeCode failed: %v", err)
	}
	if resp.AccessToken != "acc-token-999" || resp.RefreshToken != "ref-token-888" {
		t.Errorf("unexpected token response: %+v", resp)
	}

	// Verify status after exchange
	st = client.GetOAuthStatus()
	if !st.Connected || st.Username != "musiclover" {
		t.Errorf("unexpected status after exchange: %+v", st)
	}
	if persistence.savedAccess != "acc-token-999" || persistence.savedUser != "musiclover" {
		t.Errorf("persistence not updated: %+v", persistence)
	}

	// 2. RefreshToken
	rfResp, err := client.RefreshToken(ctx, "")
	if err != nil {
		t.Fatalf("RefreshToken failed: %v", err)
	}
	if rfResp.AccessToken != "acc-token-new" || rfResp.RefreshToken != "ref-token-new" {
		t.Errorf("unexpected refresh response: %+v", rfResp)
	}
	if persistence.savedAccess != "acc-token-new" {
		t.Errorf("persistence not updated on refresh: %+v", persistence)
	}

	// 3. Disconnect
	if err := client.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect failed: %v", err)
	}
	st = client.GetOAuthStatus()
	if st.Connected || st.Username != "" {
		t.Errorf("status not cleared after disconnect: %+v", st)
	}
	if !persistence.cleared {
		t.Errorf("persistence not cleared after disconnect")
	}
}

func TestMusicBrainzOAuth_AutoRefresh(t *testing.T) {
	var refreshCalled int32
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			atomic.AddInt32(&refreshCalled, 1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{
				"access_token": "auto-refreshed-token",
				"token_type": "Bearer",
				"expires_in": 3600
			}`)
		case "/release":
			auth := r.Header.Get("Authorization")
			if auth != "Bearer auto-refreshed-token" {
				http.Error(w, "stale or missing token", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"count": 0, "releases": []}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	client := NewMusicBrainzClient("")
	client.SetBaseURL(mockServer.URL)
	client.SetOAuthBaseURL(mockServer.URL)
	client.SetOAuthConfig("cid", "csec", "https://app.test/cb")
	client.minInterval = 0

	// Set token expiring in 10 seconds (less than 1 minute)
	client.SetTokens("almost-expired", "rf-123", time.Now().Add(10*time.Second), "user")

	ctx := context.Background()
	_, err := client.SearchReleases(ctx, "Test")
	if err != nil {
		t.Fatalf("SearchReleases failed: %v", err)
	}

	if atomic.LoadInt32(&refreshCalled) != 1 {
		t.Errorf("expected RefreshToken to be called automatically, got %d", refreshCalled)
	}
	if client.GetOAuthStatus().Connected != true {
		t.Errorf("client should remain connected")
	}
}

