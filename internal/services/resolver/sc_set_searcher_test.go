package resolver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMatchTracklists_DirectURL(t *testing.T) {
	matcher := NewFullReleaseMatcher(nil)

	targetTracks := []ResolvedTrack{
		{TrackID: "t1", TrackNumber: 1, Title: "Банкомат", Artist: "DEAD BLONDE", Duration: 200},
		{TrackID: "t2", TrackNumber: 2, Title: "Красный свет", Artist: "DEAD BLONDE", Duration: 198},
	}

	candTracks := []AlbumTrackCandidate{
		{
			Title:     "Банкомат",
			Artist:    "DEAD BLONDE",
			Duration:  200,
			DirectURL: "https://soundcloud.com/rusyarapscene/dead-blonde-bankomat",
		},
		{
			Title:     "Красный свет",
			Artist:    "DEAD BLONDE",
			Duration:  198,
			DirectURL: "https://soundcloud.com/rusyarapscene/dead-blonde-krasny-svet",
		},
	}

	urls, matched := matcher.MatchTracklists(targetTracks, candTracks)
	if matched != 2 {
		t.Fatalf("expected 2 matches, got %d", matched)
	}
	if urls[0] != "https://soundcloud.com/rusyarapscene/dead-blonde-bankomat" {
		t.Errorf("track 0 url = %q, want direct soundcloud url", urls[0])
	}
	if urls[1] != "https://soundcloud.com/rusyarapscene/dead-blonde-krasny-svet" {
		t.Errorf("track 1 url = %q, want direct soundcloud url", urls[1])
	}
}

func TestSoundCloudSetSearcher_ParseSetEntries(t *testing.T) {
	searcher := NewSoundCloudSetSearcher("")

	sampleOutput := `
{"title": "DEAD BLONDE - Банкомат [FREE DL]", "duration": 200.0, "webpage_url": "https://soundcloud.com/user/track1", "uploader": "Русская Рэп-Сцена", "playlist_title": "DEAD BLONDE - СПЛЕТНИЦА"}
{"title": "DEAD BLONDE - Красный свет (Buy = Free DL)", "duration": 198.0, "webpage_url": "https://soundcloud.com/user/track2", "uploader": "Русская Рэп-Сцена", "playlist_title": "DEAD BLONDE - СПЛЕТНИЦА"}
`
	cand, err := searcher.ParseOutputBytes([]byte(sampleOutput), "https://soundcloud.com/user/sets/spletnitsa")
	if err != nil {
		t.Fatalf("ParseOutputBytes failed: %v", err)
	}
	if cand == nil {
		t.Fatal("expected cand, got nil")
	}
	if len(cand.Tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(cand.Tracks))
	}
	if cand.Tracks[0].Title != "Банкомат" {
		t.Errorf("track 0 title = %q, want 'Банкомат'", cand.Tracks[0].Title)
	}
	if cand.Tracks[0].DirectURL != "https://soundcloud.com/user/track1" {
		t.Errorf("track 0 direct URL = %q", cand.Tracks[0].DirectURL)
	}
	if !strings.Contains(cand.Title, "СПЛЕТНИЦА") {
		t.Errorf("cand title = %q, want contain 'СПЛЕТНИЦА'", cand.Title)
	}
}

func TestSoundCloudSetSearcher_GetClientID_Dynamic(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><script src="/assets/app-12345.js"></script></head><body></body></html>`))
			return
		}
		if r.URL.Path == "/assets/app-12345.js" {
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte(`window.__ENV={client_id:"99aabbccddeeff001122334455667788",version:"1.0"};`))
			return
		}
		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	searcher := NewSoundCloudSetSearcher("")
	searcher.SetCustomHTTPClient(mockServer.Client())
	searcher.SetBaseURLs("", mockServer.URL)
	searcher.InvalidateClientID()

	cid, err := searcher.GetClientID(context.Background())
	if err != nil {
		t.Fatalf("GetClientID failed: %v", err)
	}
	if cid != "99aabbccddeeff001122334455667788" {
		t.Errorf("got cid %q, want '99aabbccddeeff001122334455667788'", cid)
	}
}

func TestSoundCloudSetSearcher_SearchSetsAPI_KishlakWithHydration(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.HasPrefix(r.URL.Path, "/search/albums") {
			resp := scAPISearchResponse{
				Collection: []scAPIAlbumItem{
					{
						ID:           1670891711,
						Title:        "сборник хуйни и картинки",
						PermalinkURL: "https://soundcloud.com/kishlak/sets/sbornik-xujni-i-kartinki",
						ArtworkURL:   "https://i1.sndcdn.com/artworks-sample-large.jpg",
						TrackCount:   3,
						User: scAPIUser{
							Username:  "Кишлак☆",
							Permalink: "kishlak",
						},
						Tracks: []scAPITrackItem{
							{
								ID:           1591814707,
								Title:        "возвращайся [intro] (prod. кишлак)",
								Duration:     95000,
								PermalinkURL: "https://soundcloud.com/kishlak/vozvrashchaysya-intro-prod-kishlak",
								User:         scAPIUser{Username: "Кишлак☆"},
							},
							{
								ID: 1591814935, // Stub track needing hydration
							},
							{
								ID: 1615307139, // Stub track needing hydration
							},
						},
					},
				},
				TotalResults: 1,
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/search/playlists_without_albums") {
			resp := scAPISearchResponse{
				Collection:   []scAPIAlbumItem{},
				TotalResults: 0,
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/tracks") {
			// Hydration endpoint
			hydratedTracks := []scAPITrackItem{
				{
					ID:           1591814935,
					Title:        "сборник хуйни и картинки (prod. кишлак)",
					Duration:     107000,
					PermalinkURL: "https://soundcloud.com/kishlak/sbornik-khuyni-i-kartinki-prod-kishlak",
					User:         scAPIUser{Username: "Кишлак☆"},
				},
				{
					ID:           1615307139,
					Title:        "а у нас на районе (prod. кишлак)",
					Duration:     156000,
					PermalinkURL: "https://soundcloud.com/kishlak/a-u-nas-na-rayone-prod-kishlak",
					User:         scAPIUser{Username: "Кишлак☆"},
				},
			}
			_ = json.NewEncoder(w).Encode(hydratedTracks)
			return
		}

		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	searcher := NewSoundCloudSetSearcher("")
	searcher.SetCustomHTTPClient(mockServer.Client())
	searcher.SetBaseURLs(mockServer.URL, mockServer.URL)

	candidates, err := searcher.SearchSoundCloudSets(context.Background(), "Кишлак", "Сборник хуйни и картинки", 3)
	if err != nil {
		t.Fatalf("SearchSoundCloudSets failed: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}

	cand := candidates[0]
	if cand.Title != "сборник хуйни и картинки" {
		t.Errorf("cand title = %q, want 'сборник хуйни и картинки'", cand.Title)
	}
	if !strings.Contains(cand.Artist, "Кишлак") {
		t.Errorf("cand artist = %q, want contain 'Кишлак'", cand.Artist)
	}
	if cand.CoverURL != "https://i1.sndcdn.com/artworks-sample-t500x500.jpg" {
		t.Errorf("cand cover = %q, want upgraded t500x500 cover", cand.CoverURL)
	}
	if len(cand.Tracks) != 3 {
		t.Fatalf("expected 3 tracks in candidate, got %d", len(cand.Tracks))
	}

	// Verify track 1 (was pre-hydrated)
	if cand.Tracks[0].Duration != 95 {
		t.Errorf("track 0 duration = %d, want 95s", cand.Tracks[0].Duration)
	}
	if cand.Tracks[0].DirectURL != "https://soundcloud.com/kishlak/vozvrashchaysya-intro-prod-kishlak" {
		t.Errorf("track 0 url = %q", cand.Tracks[0].DirectURL)
	}

	// Verify track 2 (was hydrated from /tracks)
	if cand.Tracks[1].Duration != 107 {
		t.Errorf("track 1 duration = %d, want 107s", cand.Tracks[1].Duration)
	}
	if cand.Tracks[1].Title != "Сборник хуйни и картинки" && cand.Tracks[1].Title != "сборник хуйни и картинки" {
		t.Errorf("track 1 title = %q", cand.Tracks[1].Title)
	}

	// Verify track 3 (was hydrated from /tracks)
	if cand.Tracks[2].Duration != 156 {
		t.Errorf("track 2 duration = %d, want 156s", cand.Tracks[2].Duration)
	}
}

func TestSoundCloudSetSearcher_ClientIDRefreshOn401(t *testing.T) {
	attempts := 0
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/search/albums") {
			attempts++
			clientID := r.URL.Query().Get("client_id")
			if clientID == "expired_client_id" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))
				return
			}
			resp := scAPISearchResponse{
				Collection: []scAPIAlbumItem{
					{
						ID:           1234,
						Title:        "Valid Album",
						PermalinkURL: "https://soundcloud.com/artist/sets/valid",
						TrackCount:   1,
						User:         scAPIUser{Username: "Artist"},
						Tracks: []scAPITrackItem{
							{ID: 1, Title: "Track 1", Duration: 120000, PermalinkURL: "https://soundcloud.com/artist/t1"},
						},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><head><script src="/assets/fresh.js"></script></head></html>`))
			return
		}
		if r.URL.Path == "/assets/fresh.js" {
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte(`client_id="freshvalidclientid123456789012"`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/search/playlists_without_albums") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(scAPISearchResponse{})
			return
		}
		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	searcher := NewSoundCloudSetSearcher("")
	searcher.SetCustomHTTPClient(mockServer.Client())
	searcher.SetBaseURLs(mockServer.URL, mockServer.URL)

	// Pre-populate with expired client_id that is considered unexpired in cache
	searcher.clientIDMu.Lock()
	searcher.clientID = "expired_client_id"
	searcher.clientIDExp = time.Now().Add(time.Hour)
	searcher.clientIDMu.Unlock()

	cands, err := searcher.SearchSetsAPI(context.Background(), "query", 3)
	if err != nil {
		t.Fatalf("SearchSetsAPI failed: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 cand after refresh, got %d", len(cands))
	}
	if attempts < 2 {
		t.Errorf("expected at least 2 attempts (initial 401 + retry), got %d", attempts)
	}
}
