package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"music-manager/internal/services/downloader"
	"music-manager/internal/services/resolver"
)

func TestGetDiscography_Validation(t *testing.T) {
	h := NewDownloadHandler(nil, nil)

	// 1. Empty URL
	req := httptest.NewRequest(http.MethodGet, "/downloads/discography?url=", nil)
	w := httptest.NewRecorder()
	h.GetDiscography(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for empty url, got %d", w.Code)
	}

	// 2. Unrecognized URL
	req2 := httptest.NewRequest(http.MethodGet, "/downloads/discography?url=https://unknown-music-service.com/artist/123", nil)
	w2 := httptest.NewRecorder()
	h.GetDiscography(w2, req2)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for unrecognized url, got %d", w2.Code)
	}
}

func TestGetDiscography_YandexMusic(t *testing.T) {
	// Mock Yandex Music API
	mockYt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/artists/249991/brief-info":
			w.Write([]byte(`{
				"result": {
					"artist": {
						"id": 249991,
						"name": "Oxxxymiron",
						"cover": {"uri": "avatars.yandex.net/get-music-content/123/%%"}
					}
				}
			}`))
		case "/artists/249991/direct-albums":
			w.Write([]byte(`{
				"result": {
					"albums": [
						{
							"id": 3087311,
							"title": "Горгород",
							"year": 2015,
							"metaType": "music",
							"coverUri": "avatars.yandex.net/get-music-content/456/%%",
							"trackCount": 11,
							"artists": [{"id": 249991, "name": "Oxxxymiron"}]
						}
					]
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockYt.Close()

	yandexClient := resolver.NewYandexMusicClient("")
	yandexClient.SetBaseURL(mockYt.URL)

	sourceRes := resolver.NewSourceResolver(nil, nil, "")
	// Use mock yandex client in resolver via reflection or internal field
	// Or pass directly to test
	svc := downloader.NewService(context.Background(), nil, sourceRes, nil, nil, "", 2)
	defer svc.Close()
	sourceRes.Yandex().SetBaseURL(mockYt.URL)

	h := NewDownloadHandler(svc, nil)

	req := httptest.NewRequest(http.MethodGet, "/downloads/discography?url=https://music.yandex.ru/artist/249991", nil)
	w := httptest.NewRecorder()
	h.GetDiscography(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp DiscographyResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode JSON: %v", err)
	}

	if resp.ArtistName != "Oxxxymiron" {
		t.Errorf("ArtistName = %q, want 'Oxxxymiron'", resp.ArtistName)
	}
	if resp.Source != "yandex" {
		t.Errorf("Source = %q, want 'yandex'", resp.Source)
	}
	if len(resp.Releases) != 1 {
		t.Fatalf("Releases len = %d, want 1", len(resp.Releases))
	}
	if resp.Releases[0].Title != "Горгород" {
		t.Errorf("Release title = %q, want 'Горгород'", resp.Releases[0].Title)
	}
	if resp.Releases[0].CoverURL != "https://avatars.yandex.net/get-music-content/456/1000x1000" {
		t.Errorf("CoverURL = %q", resp.Releases[0].CoverURL)
	}
}

func TestIsYouTubeArtistURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://www.youtube.com/@NoizeMC", true},
		{"https://music.youtube.com/channel/UC12345", true},
		{"https://www.youtube.com/user/eminem", true},
		{"https://www.youtube.com/c/artist", true},
		{"https://www.youtube.com/watch?v=12345", false},
		{"https://deezer.com/artist/123", false},
	}

	for _, tt := range tests {
		got := isYouTubeArtistURL(tt.url)
		if got != tt.want {
			t.Errorf("isYouTubeArtistURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestGetDiscography_YandexHTTP451Geoblock(t *testing.T) {
	mockYt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnavailableForLegalReasons)
		w.Write([]byte(`{"error":{"name":"Unavailable For Legal Reasons"}}`))
	}))
	defer mockYt.Close()

	sourceRes := resolver.NewSourceResolver(nil, nil, "")
	sourceRes.Yandex().SetBaseURL(mockYt.URL)

	svc := downloader.NewService(context.Background(), nil, sourceRes, nil, nil, "", 2)
	defer svc.Close()
	h := NewDownloadHandler(svc, nil)

	req := httptest.NewRequest(http.MethodGet, "/downloads/discography?url=https://music.yandex.ru/artist/8135481", nil)
	w := httptest.NewRecorder()
	h.GetDiscography(w, req)

	if w.Code != http.StatusUnavailableForLegalReasons {
		t.Fatalf("Expected 451 Unavailable For Legal Reasons, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode JSON: %v", err)
	}

	if !strings.Contains(resp["detail"], "HTTP 451 Geoblock") && !strings.Contains(resp["detail"], "451") {
		t.Errorf("Expected detail to mention 451 Geoblock, got: %s", resp["detail"])
	}
}
