package resolver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/db"
)

func TestLastfmClient(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Query().Get("method")
		if method != "artist.getsimilar" {
			http.Error(w, "bad method", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"similarartists": {
				"artist": [
					{"name": "Justice", "match": "0.95", "url": "https://last.fm/music/Justice"},
					{"name": "Kavinsky", "match": "0.88", "url": "https://last.fm/music/Kavinsky"}
				]
			}
		}`))
	}))
	defer ts.Close()

	client := NewLastfmClient("dummy_key")
	client.SetBaseURL(ts.URL)

	res, err := client.GetSimilarArtists(context.Background(), "Daft Punk", 5)
	if err != nil {
		t.Fatalf("GetSimilarArtists failed: %v", err)
	}
	if len(res) != 2 || res[0].Name != "Justice" || res[0].Match != 0.95 {
		t.Fatalf("unexpected similar artists: %+v", res)
	}
}

func TestDeezerArtistMethods(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/search/artist" {
			_, _ = w.Write([]byte(`{
				"data": [
					{"id": 27, "name": "Daft Punk", "picture_xl": "https://example.com/dp.jpg"}
				]
			}`))
			return
		}
		if r.URL.Path == "/artist/27/related" {
			_, _ = w.Write([]byte(`{
				"data": [
					{"id": 100, "name": "Justice", "picture_medium": "https://example.com/justice.jpg"}
				]
			}`))
			return
		}
		if r.URL.Path == "/artist/27/albums" {
			_, _ = w.Write([]byte(`{
				"data": [
					{
						"id": 1001,
						"title": "Discovery",
						"cover_xl": "https://example.com/disc.jpg",
						"release_date": "2001-03-12",
						"record_type": "album",
						"nb_tracks": 14
					}
				]
			}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	client := NewDeezerClient()
	client.baseURL = ts.URL

	art, err := client.SearchArtist(context.Background(), "Daft Punk")
	if err != nil {
		t.Fatalf("SearchArtist failed: %v", err)
	}
	if art.ID != 27 || art.Name != "Daft Punk" {
		t.Fatalf("unexpected artist: %+v", art)
	}

	related, err := client.GetRelatedArtists(context.Background(), "27")
	if err != nil {
		t.Fatalf("GetRelatedArtists failed: %v", err)
	}
	if len(related) != 1 || related[0].Name != "Justice" {
		t.Fatalf("unexpected related: %+v", related)
	}

	albums, err := client.GetArtistAlbums(context.Background(), "27")
	if err != nil {
		t.Fatalf("GetArtistAlbums failed: %v", err)
	}
	if len(albums) != 1 || albums[0].Title != "Discovery" {
		t.Fatalf("unexpected albums: %+v", albums)
	}
}

func TestDiscographyAggregator_DeduplicationAndReconcile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "aggregator_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	// Вставляем альбом в локальную базу
	_, err = database.Conn().ExecContext(ctx, `
		INSERT INTO artists (id, name, clean_name) VALUES (1, 'Daft Punk', 'daft punk');
		INSERT INTO albums (id, title, clean_title, album_artist, artist_id, year, folder_path)
		VALUES (1, 'Discovery', 'discovery', 'Daft Punk', 1, 2001, 'Daft Punk - Discovery');
	`)
	if err != nil {
		t.Fatalf("failed to insert album: %v", err)
	}

	agg := NewDiscographyAggregator(nil, nil, nil, database)

	// Тест дедупликации
	raw := []AggregatedRelease{
		{
			ArtistName:  "Daft Punk",
			CleanArtist: "daft punk",
			Title:       "Discovery",
			CleanTitle:  CleanReleaseTitle("Discovery"),
			Year:        2001,
			DeezerID:    "1001",
		},
		{
			ArtistName:    "Daft Punk",
			CleanArtist:   "daft punk",
			Title:         "Discovery (Remastered)",
			CleanTitle:    CleanReleaseTitle("Discovery (Remastered)"),
			Year:          2001,
			MusicBrainzID: "mb-disc",
		},
		{
			ArtistName:  "Daft Punk",
			CleanArtist: "daft punk",
			Title:       "New Single 2026",
			CleanTitle:  CleanReleaseTitle("New Single 2026"),
			Year:        2026,
			DeezerID:    "2002",
		},
		{
			ArtistName:  "Daft Punk",
			CleanArtist: "daft punk",
			Title:       "Old Rare Album",
			CleanTitle:  CleanReleaseTitle("Old Rare Album"),
			Year:        1995,
			DeezerID:    "3003",
		},
	}

	deduped := agg.deduplicateReleases(raw)
	if len(deduped) != 3 {
		t.Fatalf("expected 3 releases after deduplication, got %d", len(deduped))
	}

	// Проверяем склейку DeezerID и MusicBrainzID для Discovery
	var discRelease *AggregatedRelease
	for i := range deduped {
		if deduped[i].CleanTitle == "discovery" {
			discRelease = &deduped[i]
			break
		}
	}
	if discRelease == nil || discRelease.DeezerID != "1001" || discRelease.MusicBrainzID != "mb-disc" {
		t.Fatalf("Discovery was not merged correctly: %+v", discRelease)
	}

	// Тест reconcileWithLibrary
	agg.reconcileWithLibrary(ctx, "Daft Punk", deduped)

	for _, r := range deduped {
		if r.CleanTitle == "discovery" && r.Status != "in_library" {
			t.Errorf("expected Discovery to be in_library, got %s", r.Status)
		}
		if r.CleanTitle == "new single 2026" && (r.Status != "new" || !r.IsRadar) {
			t.Errorf("expected New Single 2026 to be new/is_radar, got status=%s is_radar=%v", r.Status, r.IsRadar)
		}
		if r.CleanTitle == "old rare album" && (r.Status != "missing" || r.IsRadar) {
			t.Errorf("expected Old Rare Album to be missing/!is_radar, got status=%s is_radar=%v", r.Status, r.IsRadar)
		}
	}
}

func TestSimilarityEngine(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "sim_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	tsDeezer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/search/artist" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": 27, "name": "Daft Punk"}},
			})
			return
		}
		if r.URL.Path == "/artist/27/related" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": 1, "name": "Justice"},
					{"id": 2, "name": "Kavinsky"},
				},
			})
			return
		}
	}))
	defer tsDeezer.Close()

	deezerClient := NewDeezerClient()
	deezerClient.baseURL = tsDeezer.URL

	simEngine := NewSimilarityEngine(deezerClient, nil, database)
	names, err := simEngine.FetchAndCacheRelations(ctx, "Daft Punk")
	if err != nil {
		t.Fatalf("FetchAndCacheRelations failed: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 related artists, got %d", len(names))
	}

	cached, err := simEngine.GetRelatedArtistsFromCache(ctx, "Daft Punk", 5)
	if err != nil {
		t.Fatalf("GetRelatedArtistsFromCache failed: %v", err)
	}
	if len(cached) != 2 || cached[0] != "justice" {
		t.Fatalf("unexpected cached related artists: %+v", cached)
	}
}
