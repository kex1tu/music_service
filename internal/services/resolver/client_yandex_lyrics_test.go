package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestYandexMusicClient_GetTrackLyrics_SuccessLRC(t *testing.T) {
	const expectedLRC = "[00:01.00]First line\n[00:05.50]Second line with timestamps"
	var apiAuthHeader atomic.Value
	var downloadAuthHeader atomic.Value

	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tracks/12345/lyrics":
			apiAuthHeader.Store(r.Header.Get("Authorization"))

			format := r.URL.Query().Get("format")
			if format != "LRC" {
				http.Error(w, "unexpected format", http.StatusBadRequest)
				return
			}
			timeStamp := r.URL.Query().Get("timeStamp")
			sign := r.URL.Query().Get("sign")
			if timeStamp == "" || sign == "" {
				http.Error(w, "missing sign params", http.StatusBadRequest)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{
					"downloadUrl": ts.URL + "/storage/lyrics_12345.lrc",
					"lyricId":     99901,
				},
			})
		case "/storage/lyrics_12345.lrc":
			downloadAuthHeader.Store(r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(expectedLRC))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewYandexMusicClient("my-secret-token")
	client.SetSignKey("fixture-signing-key")
	client.SetBaseURL(ts.URL)

	lyrics, synced, err := client.GetTrackLyrics(context.Background(), 12345)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !synced {
		t.Errorf("expected synced=true, got %v", synced)
	}
	if lyrics != expectedLRC {
		t.Errorf("lyrics mismatch:\ngot:  %q\nwant: %q", lyrics, expectedLRC)
	}

	// Security check: API must receive Authorization: OAuth ...
	auth := apiAuthHeader.Load()
	if auth != "OAuth my-secret-token" {
		t.Errorf("API Authorization header mismatch: got %v, want 'OAuth my-secret-token'", auth)
	}

	// Security check: downloadUrl must NOT receive Authorization header
	dlAuth := downloadAuthHeader.Load()
	if dlAuth != nil && dlAuth.(string) != "" {
		t.Errorf("downloadUrl must NOT receive Authorization header, but got: %q", dlAuth)
	}
}

func TestYandexMusicClient_GetTrackLyrics_FallbackToText(t *testing.T) {
	const expectedText = "Just plain text lyrics without any timestamps\nSecond line"

	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tracks/67890/lyrics":
			format := r.URL.Query().Get("format")
			if format == "LRC" {
				// No synced LRC for this track
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "not-found"})
				return
			}
			if format == "TEXT" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"result": map[string]any{
						"downloadUrl": ts.URL + "/storage/lyrics_67890.txt",
						"lyricId":     99902,
					},
				})
				return
			}
			http.Error(w, "unknown format", http.StatusBadRequest)
		case "/storage/lyrics_67890.txt":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(expectedText))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewYandexMusicClient("my-secret-token")
	client.SetSignKey("fixture-signing-key")
	client.SetBaseURL(ts.URL)

	lyrics, synced, err := client.GetTrackLyrics(context.Background(), 67890)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if synced {
		t.Errorf("expected synced=false for plain text, got %v", synced)
	}
	if lyrics != expectedText {
		t.Errorf("lyrics mismatch: got %q, want %q", lyrics, expectedText)
	}
}

func TestYandexMusicClient_GetTrackLyrics_NoToken(t *testing.T) {
	client := NewYandexMusicClient("")
	client.SetBaseURL("http://127.0.0.1:9999") // Should never make network request

	_, _, err := client.GetTrackLyrics(context.Background(), 12345)
	if !errors.Is(err, ErrYandexNoToken) {
		t.Errorf("expected ErrYandexNoToken, got %v", err)
	}
}

func TestYandexMusicClient_GetTrackLyrics_InvalidID(t *testing.T) {
	client := NewYandexMusicClient("some-token")

	for _, invalidID := range []int64{0, -1, -999} {
		_, _, err := client.GetTrackLyrics(context.Background(), invalidID)
		if err == nil {
			t.Errorf("expected error for invalid trackID %d, got nil", invalidID)
		}
	}
}

func TestYandexMusicClient_GetTrackLyrics_NotFoundBoth(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	client := NewYandexMusicClient("token")
	client.SetSignKey("fixture-signing-key")
	client.SetBaseURL(ts.URL)

	_, _, err := client.GetTrackLyrics(context.Background(), 11111)
	if !errors.Is(err, ErrYandexLyricsNotFound) {
		t.Errorf("expected ErrYandexLyricsNotFound, got %v", err)
	}
}

func TestYandexMusicClient_GetTrackLyrics_EmptyOrWhitespaceResponse(t *testing.T) {
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tracks/22222/lyrics":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{
					"downloadUrl": ts.URL + "/storage/empty.lrc",
				},
			})
		case "/storage/empty.lrc":
			_, _ = w.Write([]byte("   \n\t  \n "))
		}
	}))
	defer ts.Close()

	client := NewYandexMusicClient("token")
	client.SetSignKey("fixture-signing-key")
	client.SetBaseURL(ts.URL)

	_, _, err := client.GetTrackLyrics(context.Background(), 22222)
	if !errors.Is(err, ErrYandexLyricsNotFound) {
		t.Errorf("expected ErrYandexLyricsNotFound for empty text, got %v", err)
	}
}

func TestYandexMusicClient_GetTrackLyrics_CorruptedHTMLResponse(t *testing.T) {
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tracks/33333/lyrics":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{
					"downloadUrl": ts.URL + "/storage/error.html",
				},
			})
		case "/storage/error.html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<!DOCTYPE html><html><head><title>404 Not Found</title></head><body>Error</body></html>"))
		}
	}))
	defer ts.Close()

	client := NewYandexMusicClient("token")
	client.SetSignKey("fixture-signing-key")
	client.SetBaseURL(ts.URL)

	_, _, err := client.GetTrackLyrics(context.Background(), 33333)
	if err == nil || !strings.Contains(err.Error(), "HTML") {
		t.Errorf("expected HTML rejection error, got %v", err)
	}
}

func TestYandexMusicClient_GetTrackLyrics_CorruptedJSONResponse(t *testing.T) {
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tracks/44444/lyrics":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{
					"downloadUrl": ts.URL + "/storage/error.json",
				},
			})
		case "/storage/error.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error": "AccessDenied", "message": "Signature expired"}`))
		}
	}))
	defer ts.Close()

	client := NewYandexMusicClient("token")
	client.SetSignKey("fixture-signing-key")
	client.SetBaseURL(ts.URL)

	_, _, err := client.GetTrackLyrics(context.Background(), 44444)
	if err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Errorf("expected JSON rejection error, got %v", err)
	}
}

func TestYandexMusicClient_GetTrackLyrics_TooLargeResponse(t *testing.T) {
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tracks/55555/lyrics":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{
					"downloadUrl": ts.URL + "/storage/huge.lrc",
				},
			})
		case "/storage/huge.lrc":
			// Generate 600 KB payload (limit is 512 KB)
			hugeData := strings.Repeat("[00:01.00] line\n", 40000)
			_, _ = w.Write([]byte(hugeData))
		}
	}))
	defer ts.Close()

	client := NewYandexMusicClient("token")
	client.SetSignKey("fixture-signing-key")
	client.SetBaseURL(ts.URL)

	_, _, err := client.GetTrackLyrics(context.Background(), 55555)
	if err == nil || !strings.Contains(err.Error(), "лимит") {
		t.Errorf("expected size limit error, got %v", err)
	}
}

func TestYandexMusicClient_GetTrackLyrics_StatusErrors(t *testing.T) {
	tests := []struct {
		status      int
		errContains string
	}{
		{http.StatusUnauthorized, "HTTP 401"},
		{http.StatusForbidden, "HTTP 403"},
		{http.StatusUnavailableForLegalReasons, "HTTP 451"},
		{http.StatusTooManyRequests, "HTTP 429"},
		{http.StatusInternalServerError, "HTTP 500"},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("Status_%d", tc.status), func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer ts.Close()

			client := NewYandexMusicClient("token")
			client.SetSignKey("fixture-signing-key")
			client.SetBaseURL(ts.URL)

			_, _, err := client.GetTrackLyrics(context.Background(), 77777)
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("status %d: expected error containing %q, got %v", tc.status, tc.errContains, err)
			}
		})
	}
}

func TestYandexMusicClient_GetTrackLyrics_ContextCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := NewYandexMusicClient("token")
	client.SetSignKey("fixture-signing-key")
	client.SetBaseURL(ts.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, _, err := client.GetTrackLyrics(ctx, 88888)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestYandexMusicClient_GetTrackLyrics_RedirectLimitAndSecurity(t *testing.T) {
	redirectCount := 0
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/tracks/99999/lyrics":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{
					"downloadUrl": ts.URL + "/redirect/1",
				},
			})
		case strings.HasPrefix(r.URL.Path, "/redirect/"):
			redirectCount++
			if r.Header.Get("Authorization") != "" {
				t.Errorf("Authorization header leaked to redirect request: %s", r.Header.Get("Authorization"))
			}
			// Endless redirect loop
			http.Redirect(w, r, fmt.Sprintf("%s/redirect/%d", ts.URL, redirectCount+1), http.StatusFound)
		}
	}))
	defer ts.Close()

	client := NewYandexMusicClient("secret-token")
	client.SetSignKey("fixture-signing-key")
	client.SetBaseURL(ts.URL)

	_, _, err := client.GetTrackLyrics(context.Background(), 99999)
	if err == nil || !strings.Contains(err.Error(), "перенаправлений") {
		t.Errorf("expected redirect limit error, got %v", err)
	}
	if redirectCount > 6 {
		t.Errorf("redirect count exceeded limit: %d", redirectCount)
	}
}

func TestYandexMusicClient_GetTrackLyrics_RejectsOtherStorageOrigins(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		t.Run(fmt.Sprintf("redirect=%t", redirect), func(t *testing.T) {
			var reached atomic.Bool
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached.Store(true)
				_, _ = w.Write([]byte("Unexpected lyrics"))
			}))
			defer other.Close()

			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/storage" {
					http.Redirect(w, r, other.URL, http.StatusFound)
					return
				}
				location := other.URL
				if redirect {
					location = "/storage"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"result": map[string]string{"downloadUrl": location},
				})
			}))
			defer api.Close()

			client := NewYandexMusicClient("fixture-token")
			client.SetSignKey("fixture-signing-key")
			client.SetBaseURL(api.URL)
			if _, _, err := client.GetTrackLyrics(context.Background(), 1); err == nil {
				t.Fatal("expected an error for an unapproved storage origin")
			}
			if reached.Load() {
				t.Fatal("the client contacted a disallowed storage origin")
			}
		})
	}
}
