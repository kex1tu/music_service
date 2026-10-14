package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/services/resolver"
)

func TestTagHandler_SearchCatalog(t *testing.T) {
	// Mock Deezer Server
	deezerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"data": [
				{
					"id": 1001,
					"title": "Abbey Road",
					"nb_tracks": 17,
					"artist": {"id": 1, "name": "The Beatles"},
					"cover_xl": "https://deezer.com/abbey.jpg"
				}
			]
		}`)
	}))
	defer deezerServer.Close()

	// Mock MusicBrainz Server
	mbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"count": 1,
			"releases": [
				{
					"id": "mbid-abbey",
					"title": "Abbey Road",
					"date": "1969-09-26",
					"track-count": 17,
					"artist-credit": [{"name": "The Beatles", "joinphrase": ""}],
					"cover-art-archive": {"front": true, "count": 1}
				}
			]
		}`)
	}))
	defer mbServer.Close()

	// Mock Shazam / iTunes Server
	shazamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"resultCount": 1,
			"results": [
				{
					"trackName": "Come Together",
					"artistName": "The Beatles",
					"collectionName": "Abbey Road",
					"primaryGenreName": "Rock",
					"releaseDate": "1969-09-26T00:00:00Z",
					"trackNumber": 1,
					"trackTimeMillis": 259000,
					"artworkUrl100": "https://itunes.com/100x100bb.jpg"
				}
			]
		}`)
	}))
	defer shazamServer.Close()

	deezerClient := resolver.NewDeezerClient()
	deezerClient.SetBaseURL(deezerServer.URL)
	mbClient := resolver.NewMusicBrainzClient("")
	mbClient.SetBaseURL(mbServer.URL)

	itunesClient := resolver.NewITunesClient(nil)
	itunesClient.SetBaseURL(shazamServer.URL)
	shazamProvider := resolver.NewShazamProvider(itunesClient)

	tagHandler := &TagHandler{
		deezer:      deezerClient,
		musicbrainz: mbClient,
		shazam:      shazamProvider,
	}

	req := httptest.NewRequest("GET", "/api/catalog/search?query=The+Beatles+Abbey+Road", nil)
	w := httptest.NewRecorder()

	tagHandler.SearchCatalog(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp resolver.CatalogSearchResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Deezer.Count != 1 || len(resp.Deezer.Items) != 1 {
		t.Errorf("expected 1 Deezer item, got %d", resp.Deezer.Count)
	}
	if resp.Deezer.Items[0].Title != "Abbey Road" {
		t.Errorf("Deezer item title mismatch: %s", resp.Deezer.Items[0].Title)
	}

	if resp.MusicBrainz.Count != 1 || len(resp.MusicBrainz.Items) != 1 {
		t.Errorf("expected 1 MusicBrainz item, got %d", resp.MusicBrainz.Count)
	}
	if resp.MusicBrainz.Items[0].ID != "mbid-abbey" {
		t.Errorf("MusicBrainz ID mismatch: %s", resp.MusicBrainz.Items[0].ID)
	}

	if resp.Shazam.Count != 1 || len(resp.Shazam.Items) != 1 {
		t.Errorf("expected 1 Shazam item, got %d", resp.Shazam.Count)
	}
	if resp.Shazam.Items[0].Title != "Abbey Road" {
		t.Errorf("Shazam item title mismatch: %s", resp.Shazam.Items[0].Title)
	}
}

func TestTagHandler_GetCatalogDeezerAlbum(t *testing.T) {
	deezerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/album/42" {
			fmt.Fprint(w, `{
				"id": 42,
				"title": "Discovery",
				"release_date": "2001-03-12",
				"artist": {"id": 1, "name": "Daft Punk"},
				"cover_xl": "https://deezer.com/discovery.jpg",
				"tracks": {
					"data": [
						{
							"id": 101,
							"title": "One More Time",
							"track_position": 1,
							"duration": 320,
							"artist": {"name": "Daft Punk"}
						}
					]
				}
			}`)
			return
		}
		if r.URL.Path == "/track/101" {
			fmt.Fprint(w, `{
				"id": 101,
				"title": "One More Time",
				"isrc": "FR06N0100010",
				"disk_number": 1,
				"contributors": [
					{"id": 1, "name": "Daft Punk"},
					{"id": 2, "name": "Romanthony"}
				]
			}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer deezerServer.Close()

	deezerClient := resolver.NewDeezerClient()
	deezerClient.SetBaseURL(deezerServer.URL)
	tagHandler := &TagHandler{
		deezer: deezerClient,
	}

	r := chi.NewRouter()
	r.Get("/api/catalog/deezer/album/{id}", tagHandler.GetCatalogDeezerAlbum)

	req := httptest.NewRequest("GET", "/api/catalog/deezer/album/42", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	var rel resolver.CatalogReleaseDetails
	if err := json.NewDecoder(w.Body).Decode(&rel); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if rel.ID != "42" || rel.Title != "Discovery" {
		t.Errorf("release metadata mismatch: %+v", rel)
	}
	if len(rel.Tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(rel.Tracks))
	}

	tr := rel.Tracks[0]
	if tr.Title != "One More Time" {
		t.Errorf("track title mismatch: %s", tr.Title)
	}
	if tr.Artist != "Daft Punk\\\\Romanthony" {
		t.Errorf("expected multi-artist 'Daft Punk\\\\Romanthony', got %s", tr.Artist)
	}
	if tr.ISRC != "FR06N0100010" {
		t.Errorf("expected ISRC FR06N0100010, got %s", tr.ISRC)
	}
}

func TestTagHandler_AcousticRecognition_SandboxSafety(t *testing.T) {
	tempDir := t.TempDir()
	musicBox, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatalf("sandbox create: %v", err)
	}

	audioFile := filepath.Join(tempDir, "sample.mp3")
	if err := os.WriteFile(audioFile, []byte("fake mp3 audio"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	shazamProvider := resolver.NewShazamProvider(nil)
	shazamProvider.SetCommandExecutor(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`{
			"matches": [{"id":"1"}],
			"track": {
				"key": "999888",
				"title": "Around the World",
				"subtitle": "Daft Punk",
				"genres": {"primary": "House"},
				"images": {"coverart": "https://img.shazam.com/cover.jpg"},
				"sections": [{"type":"SONG","metadata":[{"title":"Album","text":"Homework"}]}]
			}
		}`), nil
	})

	tagHandler := &TagHandler{
		musicSandbox: musicBox,
		shazam:       shazamProvider,
	}

	// 1. Valid safe relative path
	req := httptest.NewRequest("GET", "/api/catalog/search?audio_path=sample.mp3", nil)
	w := httptest.NewRecorder()
	tagHandler.SearchCatalog(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	var resp resolver.CatalogSearchResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Shazam.Count != 1 || resp.Shazam.Items[0].Title != "Homework" {
		t.Errorf("acoustic recognition item mismatch: %+v", resp.Shazam)
	}

	// 2. Path Traversal attack prevention: ../../etc/passwd must not escape sandbox
	badReq := httptest.NewRequest("GET", "/api/catalog/search?audio_path=../../../../etc/passwd", nil)
	badW := httptest.NewRecorder()
	tagHandler.SearchCatalog(badW, badReq)

	var badResp resolver.CatalogSearchResponse
	_ = json.NewDecoder(badW.Body).Decode(&badResp)
	if badResp.Shazam.Count != 0 {
		t.Errorf("expected 0 recognized items on path traversal attack, got %d", badResp.Shazam.Count)
	}
}
