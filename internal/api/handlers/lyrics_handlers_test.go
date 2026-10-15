package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/services/lyrics"
)

type mockLyricsSearcher struct{}

func (m *mockLyricsSearcher) Search(ctx context.Context, artist, title, album string, duration int) (*lyrics.LyricsSearchResult, error) {
	if artist == "Daft Punk" && title == "One More Time" {
		return &lyrics.LyricsSearchResult{
			Lyrics:     "[00:10.00] One more time",
			Synced:     true,
			Source:     "lrclib",
			TrackName:  title,
			ArtistName: artist,
		}, nil
	}
	return nil, nil
}

func TestLyricsHandler_GetLyrics(t *testing.T) {
	tempDir := t.TempDir()
	sb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	lyrSvc := lyrics.NewService(sb)
	handler := NewLyricsHandler(lyrSvc)

	// 1. Missing path
	req := httptest.NewRequest("GET", "/api/lyrics", nil)
	rec := httptest.NewRecorder()
	handler.GetLyrics(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing path, got %d", rec.Code)
	}

	// 2. Existing lyrics file
	mp3Path := filepath.Join(tempDir, "song.mp3")
	_ = os.WriteFile(mp3Path, []byte("audio"), 0644)
	_ = lyrSvc.SaveLyrics("song.mp3", "[00:05.00]Hello world", "lrc")

	req2 := httptest.NewRequest("GET", "/api/lyrics?path=song.mp3", nil)
	rec2 := httptest.NewRecorder()
	handler.GetLyrics(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec2.Code)
	}

	var resp lyrics.LyricsResponse
	if err := json.NewDecoder(rec2.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Kind != "lrc" || !resp.Synced || resp.Lyrics == nil || *resp.Lyrics != "[00:05.00]Hello world" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestLyricsHandler_SearchLyrics(t *testing.T) {
	tempDir := t.TempDir()
	sb, _ := sandbox.NewPathSandbox(tempDir)

	lyrSvc := lyrics.NewService(sb, &mockLyricsSearcher{})
	handler := NewLyricsHandler(lyrSvc)

	req := httptest.NewRequest("GET", "/api/lyrics/search?artist=Daft+Punk&title=One+More+Time&duration=320", nil)
	rec := httptest.NewRecorder()
	handler.SearchLyrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var res lyrics.LyricsSearchResult
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode search result: %v", err)
	}
	if res.Source != "lrclib" || !res.Synced || res.Lyrics != "[00:10.00] One more time" {
		t.Errorf("unexpected search result: %+v", res)
	}
}

func TestLyricsHandler_SaveLyrics(t *testing.T) {
	tempDir := t.TempDir()
	sb, _ := sandbox.NewPathSandbox(tempDir)

	lyrSvc := lyrics.NewService(sb)
	handler := NewLyricsHandler(lyrSvc)

	// 1. Invalid JSON
	reqBad := httptest.NewRequest("POST", "/api/lyrics", bytes.NewBufferString("{invalid_json"))
	recBad := httptest.NewRecorder()
	handler.SaveLyrics(recBad, reqBad)
	if recBad.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad json, got %d", recBad.Code)
	}

	// 2. Valid save
	body, _ := json.Marshal(map[string]string{
		"track_path": "track.mp3",
		"content":    "[00:01.00]Lyric line",
		"kind":       "lrc",
	})
	req := httptest.NewRequest("POST", "/api/lyrics", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.SaveLyrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d, body: %s", rec.Code, rec.Body.String())
	}

	// 3. Path Traversal returns 500 error json
	badBody, _ := json.Marshal(map[string]string{
		"track_path": "../../evil.mp3",
		"content":    "payload",
		"kind":       "txt",
	})
	reqTrav := httptest.NewRequest("POST", "/api/lyrics", bytes.NewReader(badBody))
	recTrav := httptest.NewRecorder()
	handler.SaveLyrics(recTrav, reqTrav)
	if recTrav.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for traversal path, got %d", recTrav.Code)
	}
}

func TestLyricsHandler_DeleteLyrics(t *testing.T) {
	tempDir := t.TempDir()
	sb, _ := sandbox.NewPathSandbox(tempDir)

	lyrSvc := lyrics.NewService(sb)
	handler := NewLyricsHandler(lyrSvc)

	// 1. Missing path
	req := httptest.NewRequest("DELETE", "/api/lyrics", nil)
	rec := httptest.NewRecorder()
	handler.DeleteLyrics(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing path, got %d", rec.Code)
	}

	// 2. Existing lyrics deleted
	_ = lyrSvc.SaveLyrics("test.mp3", "lyrics", "txt")
	req2 := httptest.NewRequest("DELETE", "/api/lyrics?path=test.mp3", nil)
	rec2 := httptest.NewRecorder()
	handler.DeleteLyrics(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec2.Code)
	}

	var res map[string]any
	_ = json.NewDecoder(rec2.Body).Decode(&res)
	if res["deleted"] != true {
		t.Errorf("expected deleted=true, got %+v", res)
	}
}
