package lyrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/core/sandbox"
)

func TestLyricsService_SaveAndGet(t *testing.T) {
	tempDir := t.TempDir()
	sb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewService(sb)

	// Create a dummy mp3 file
	mp3Path := filepath.Join(tempDir, "01. Test.mp3")
	if err := os.WriteFile(mp3Path, []byte("fake mp3 data"), 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Initial state: no lyrics
	resp, err := svc.GetLyrics("01. Test.mp3")
	if err != nil {
		t.Fatalf("GetLyrics failed: %v", err)
	}
	if resp.Kind != "none" || resp.Lyrics != nil {
		t.Errorf("expected kind none and nil lyrics, got kind=%s", resp.Kind)
	}

	// 2. Save synced lyrics (.lrc)
	syncedContent := "[00:12.34]Hello world\n[00:15.67]Second line"
	if err := svc.SaveLyrics("01. Test.mp3", syncedContent, "auto"); err != nil {
		t.Fatalf("SaveLyrics failed: %v", err)
	}

	resp2, err := svc.GetLyrics("01. Test.mp3")
	if err != nil {
		t.Fatalf("GetLyrics failed: %v", err)
	}
	if resp2.Kind != "lrc" || !resp2.Synced || resp2.Lyrics == nil || *resp2.Lyrics != syncedContent {
		t.Errorf("expected kind lrc, synced=true, got kind=%s synced=%v", resp2.Kind, resp2.Synced)
	}

	// Check that file is on disk
	lrcPath := filepath.Join(tempDir, "01. Test.lrc")
	if _, err := os.Stat(lrcPath); err != nil {
		t.Errorf("expected .lrc file on disk")
	}

	// 3. Delete lyrics
	deleted, err := svc.DeleteLyrics("01. Test.mp3")
	if err != nil || !deleted {
		t.Errorf("expected deleted=true, got %v, err: %v", deleted, err)
	}
	if _, err := os.Stat(lrcPath); !os.IsNotExist(err) {
		t.Errorf("expected .lrc file to be deleted")
	}

	// 4. Delete non-existent lyrics returns false, nil
	deleted2, err := svc.DeleteLyrics("01. Test.mp3")
	if err != nil || deleted2 {
		t.Errorf("expected deleted=false for absent lyrics, got %v, err: %v", deleted2, err)
	}
}

func TestLyricsService_FormatSwitchAndAuto(t *testing.T) {
	tempDir := t.TempDir()
	sb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewService(sb)
	mp3Path := filepath.Join(tempDir, "track.mp3")
	_ = os.WriteFile(mp3Path, []byte("audio"), 0644)

	// Save plain text lyrics (.txt)
	plainText := "Just some plain lyrics without timestamps\nLine 2"
	if err := svc.SaveLyrics("track.mp3", plainText, "txt"); err != nil {
		t.Fatalf("SaveLyrics plain failed: %v", err)
	}

	txtPath := filepath.Join(tempDir, "track.txt")
	lrcPath := filepath.Join(tempDir, "track.lrc")

	if _, err := os.Stat(txtPath); err != nil {
		t.Fatalf("expected .txt file to exist")
	}

	resp, err := svc.GetLyrics("track.mp3")
	if err != nil || resp.Kind != "txt" || resp.Synced {
		t.Fatalf("expected kind txt and synced=false, got kind=%s synced=%v", resp.Kind, resp.Synced)
	}

	// Overwrite with synced .lrc via "auto"
	syncedText := "[01:05.20]Synced line here"
	if err := svc.SaveLyrics("track.mp3", syncedText, "auto"); err != nil {
		t.Fatalf("SaveLyrics auto (synced) failed: %v", err)
	}

	// Verify .txt was removed and .lrc was created
	if _, err := os.Stat(txtPath); !os.IsNotExist(err) {
		t.Errorf("expected old .txt file to be removed when .lrc is saved")
	}
	if _, err := os.Stat(lrcPath); err != nil {
		t.Errorf("expected .lrc file to exist")
	}

	// Overwrite back with plain text via "auto"
	if err := svc.SaveLyrics("track.mp3", plainText, "auto"); err != nil {
		t.Fatalf("SaveLyrics auto (plain) failed: %v", err)
	}
	if _, err := os.Stat(lrcPath); !os.IsNotExist(err) {
		t.Errorf("expected old .lrc file to be removed when .txt is saved")
	}
	if _, err := os.Stat(txtPath); err != nil {
		t.Errorf("expected .txt file to exist")
	}

	// Test deleting when BOTH .txt and .lrc somehow exist
	_ = os.WriteFile(lrcPath, []byte(syncedText), 0644)
	deleted, err := svc.DeleteLyrics("track.mp3")
	if err != nil || !deleted {
		t.Errorf("expected deleted=true when deleting both files, got %v", deleted)
	}
	if _, err := os.Stat(lrcPath); !os.IsNotExist(err) {
		t.Errorf("expected .lrc to be deleted")
	}
	if _, err := os.Stat(txtPath); !os.IsNotExist(err) {
		t.Errorf("expected .txt to be deleted")
	}
}

func TestLyricsService_SandboxTraversalBlocked(t *testing.T) {
	tempDir := t.TempDir()
	sb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewService(sb)

	traversalPaths := []string{
		"../../etc/passwd",
		"folder/../../../outside.mp3",
		"%2e%2e/%2e%2e/secret.mp3",
		"evil\x00track.mp3",
	}

	for _, p := range traversalPaths {
		if _, err := svc.GetLyrics(p); err == nil {
			t.Errorf("expected GetLyrics(%q) to fail sandbox check", p)
		}
		if err := svc.SaveLyrics(p, "text", "txt"); err == nil {
			t.Errorf("expected SaveLyrics(%q) to fail sandbox check", p)
		}
		if _, err := svc.DeleteLyrics(p); err == nil {
			t.Errorf("expected DeleteLyrics(%q) to fail sandbox check", p)
		}
	}
}

func TestLrclibClient_Search_ExactMatch(t *testing.T) {
	// Mock server handling /get
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/get" {
			track := r.URL.Query().Get("track_name")
			artist := r.URL.Query().Get("artist_name")

			if track == "Synced Track" && artist == "Artist A" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"trackName":    "Synced Track",
					"artistName":   "Artist A",
					"plainLyrics":  "plain text",
					"syncedLyrics": "[00:10.00] Synced line",
				})
				return
			}
			if track == "Plain Track" && artist == "Artist B" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"trackName":   "Plain Track",
					"artistName":  "Artist B",
					"plainLyrics": "Only plain lyrics available",
				})
				return
			}
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	client := NewLrclibClient()
	client.SetBaseURL(ts.URL)

	ctx := context.Background()

	// 1. Synced result
	res1, err := client.Search(ctx, "Artist A", "Synced Track", "Album 1", 180)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res1 == nil || !res1.Synced || res1.Source != "lrclib" || res1.Lyrics != "[00:10.00] Synced line" {
		t.Errorf("unexpected res1: %+v", res1)
	}

	// 2. Plain result
	res2, err := client.Search(ctx, "Artist B", "Plain Track", "", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2 == nil || res2.Synced || res2.Source != "lrclib" || res2.Lyrics != "Only plain lyrics available" {
		t.Errorf("unexpected res2: %+v", res2)
	}

	// 3. Empty input
	resEmpty, err := client.Search(ctx, "", "Title", "", 0)
	if err != nil || resEmpty != nil {
		t.Errorf("expected nil for empty artist, got %+v, err=%v", resEmpty, err)
	}
}

func TestLrclibClient_Search_Fallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/get" {
			// Exact match returns 404 to trigger fallback to /search
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/search" {
			q := r.URL.Query().Get("q")
			if q == "Daft Punk One More Time" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([]map[string]interface{}{
					{
						"trackName":    "One More Time (Live)",
						"artistName":   "Daft Punk",
						"plainLyrics":  "Live version plain",
						"syncedLyrics": "",
					},
					{
						"trackName":    "One More Time",
						"artistName":   "Daft Punk",
						"plainLyrics":  "Studio plain",
						"syncedLyrics": "[00:15.00] One more time!",
					},
				})
				return
			}
			if q == "Unknown Artist Unknown" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
				return
			}
			if q == "Plain Only Song" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([]map[string]interface{}{
					{
						"trackName":   "Song",
						"artistName":  "Plain Only",
						"plainLyrics": "Plain only text",
					},
				})
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	client := NewLrclibClient()
	client.SetBaseURL(ts.URL)

	ctx := context.Background()

	// 1. Fallback prefers syncedLyrics over plainLyrics in list
	res, err := client.Search(ctx, "Daft Punk", "One More Time", "Discovery", 320)
	if err != nil {
		t.Fatalf("search fallback failed: %v", err)
	}
	if res == nil || !res.Synced || res.Lyrics != "[00:15.00] One more time!" {
		t.Errorf("expected synced item from search fallback, got %+v", res)
	}

	// 2. Fallback to plain lyrics when no synced lyrics present
	resPlain, err := client.Search(ctx, "Plain Only", "Song", "", 0)
	if err != nil {
		t.Fatalf("search plain fallback failed: %v", err)
	}
	if resPlain == nil || resPlain.Synced || resPlain.Lyrics != "Plain only text" {
		t.Errorf("expected plain item from search fallback, got %+v", resPlain)
	}

	// 3. Fallback with 0 search results returns nil, nil
	resNone, err := client.Search(ctx, "Unknown Artist", "Unknown", "", 0)
	if err != nil || resNone != nil {
		t.Errorf("expected nil for no results, got %+v, err=%v", resNone, err)
	}
}

func TestLrclibClient_Search_ErrorsAnd500(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer ts.Close()

	client := NewLrclibClient()
	client.SetBaseURL(ts.URL)

	res, err := client.Search(context.Background(), "Artist", "Title", "", 0)
	if err != nil || res != nil {
		t.Errorf("expected nil, nil on 500 error, got %+v, err=%v", res, err)
	}

	// Invalid URL
	clientBad := NewLrclibClient()
	clientBad.SetBaseURL("http://127.0.0.1:0")
	_, errBad := clientBad.Search(context.Background(), "Artist", "Title", "", 0)
	if errBad == nil {
		t.Errorf("expected error for unreachable host")
	}
}

type mockLyricsProvider struct {
	result *LyricsSearchResult
	err    error
}

func (m *mockLyricsProvider) Search(ctx context.Context, artist, title, album string, duration int) (*LyricsSearchResult, error) {
	return m.result, m.err
}

func TestLyricsService_SearchLyrics_ProviderInjection(t *testing.T) {
	tempDir := t.TempDir()
	sb, _ := sandbox.NewPathSandbox(tempDir)

	mock := &mockLyricsProvider{
		result: &LyricsSearchResult{
			Lyrics: "[00:01.00] Mocked lyrics",
			Synced: true,
			Source: "mock",
		},
	}

	svc := NewService(sb, mock)
	res, err := svc.SearchLyrics(context.Background(), "A", "T", "", 0)
	if err != nil {
		t.Fatalf("SearchLyrics failed: %v", err)
	}
	if res.Source != "mock" || res.Lyrics != "[00:01.00] Mocked lyrics" {
		t.Errorf("unexpected mocked result: %+v", res)
	}

	// Test nil result returns Source: none
	mockNil := &mockLyricsProvider{result: nil}
	svcNil := NewService(sb, mockNil)
	resNil, err := svcNil.SearchLyrics(context.Background(), "A", "T", "", 0)
	if err != nil || resNil.Source != "none" {
		t.Errorf("expected Source: none for nil provider result, got %+v", resNil)
	}

	// Test Service with nil provider
	svcNoProv := &Service{musicSandbox: sb, provider: nil}
	resNoProv, err := svcNoProv.SearchLyrics(context.Background(), "A", "T", "", 0)
	if err != nil || resNoProv.Source != "none" {
		t.Errorf("expected Source: none when provider is nil, got %+v", resNoProv)
	}
}
