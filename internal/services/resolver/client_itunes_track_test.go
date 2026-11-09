package resolver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestITunesClient_SearchTracks(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		term := r.URL.Query().Get("term")
		if term == "" {
			http.Error(w, "missing term", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"resultCount": 1,
			"results": [
				{
					"trackName": "Song Title (feat. Featured Artist)",
					"artistName": "Primary Artist",
					"collectionName": "Album Name",
					"releaseDate": "2024-05-10T07:00:00Z",
					"trackNumber": 3,
					"trackTimeMillis": 215000,
					"artworkUrl100": "https://is1-ssl.mzstatic.com/image/thumb/100x100bb.jpg"
				}
			]
		}`))
	}))
	defer mockServer.Close()

	client := NewITunesClient(mockServer.Client())
	client.SetBaseURL(mockServer.URL)

	results, err := client.SearchTracks(context.Background(), "Primary Artist Song Title")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	match := results[0]
	if match.Title != "Song Title" {
		t.Errorf("expected clean title 'Song Title', got %q", match.Title)
	}
	if len(match.Artists) != 2 {
		t.Fatalf("expected 2 artists, got %d: %v", len(match.Artists), match.Artists)
	}
	if match.Artists[0] != "Primary Artist" || match.Artists[1] != "Featured Artist" {
		t.Errorf("unexpected artists: %v", match.Artists)
	}
	if match.Album != "Album Name" {
		t.Errorf("expected album 'Album Name', got %q", match.Album)
	}
	if match.Year != 2024 {
		t.Errorf("expected year 2024, got %d", match.Year)
	}
	if match.TrackNumber != 3 {
		t.Errorf("expected track number 3, got %d", match.TrackNumber)
	}
	if match.Duration != 215 {
		t.Errorf("expected duration 215s, got %d", match.Duration)
	}
	if match.CoverURL != "https://is1-ssl.mzstatic.com/image/thumb/1000x1000bb.jpg" {
		t.Errorf("expected upscaled 1000x1000 cover URL, got %q", match.CoverURL)
	}
}

func TestITunesClient_SearchTracks_Error(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mockServer.Close()

	client := NewITunesClient(mockServer.Client())
	client.SetBaseURL(mockServer.URL)

	_, err := client.SearchTracks(context.Background(), "Test Query")
	if err == nil {
		t.Fatalf("expected error on HTTP 500, got nil")
	}
}

func TestITunesClient_SearchTracksWithCountry(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		country := r.URL.Query().Get("country")
		if country != "US" {
			http.Error(w, "expected country US", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"resultCount": 1,
			"results": [
				{
					"trackName": "US Track",
					"artistName": "US Artist",
					"collectionName": "US Album",
					"releaseDate": "2023-01-01T00:00:00Z",
					"trackNumber": 1,
					"trackTimeMillis": 180000,
					"artworkUrl100": "https://is1-ssl.mzstatic.com/image/thumb/100x100bb.jpg"
				}
			]
		}`))
	}))
	defer mockServer.Close()

	client := NewITunesClient(mockServer.Client())
	client.SetBaseURL(mockServer.URL)

	results, err := client.SearchTracksWithCountry(context.Background(), "US Query", "US")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Title != "US Track" || results[0].Duration != 180 {
		t.Fatalf("unexpected result: %+v", results)
	}
}
