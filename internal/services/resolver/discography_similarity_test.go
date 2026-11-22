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
		INSERT INTO tracks (album_id, artist_id, title, clean_title, file_path, file_size, mtime)
		VALUES (1, 1, 'One More Time', 'one more time', 'Daft Punk - Discovery/01.mp3', 1000, 1);
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
			Tracklist: []db.CatalogTrack{{
				BackfillSelection: db.BackfillSelection{TrackPosition: db.ReleasePosition(1, 1)}, Title: "One More Time",
			}},
		},
		{
			ArtistName:    "Daft Punk",
			CleanArtist:   "daft punk",
			Title:         "Discovery",
			DeezerID:      "1001",
			CleanTitle:    CleanReleaseTitle("Discovery"),
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

func TestReconcileWithLibrary_MultiArtistAndTracks(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "reconcile_multi_test_*")
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

	// 1. Создаем альбом с несколькими артистами (Big Baby Tape\\Kizaru)
	// В БД album_artist записан как Big Baby Tape, а artist_id = Big Baby Tape
	_, err = database.Conn().ExecContext(ctx, `
		INSERT INTO artists (id, name, clean_name) VALUES (1, 'Big Baby Tape', 'big baby tape');
		INSERT INTO artists (id, name, clean_name) VALUES (2, 'Aarne', 'aarne');
		INSERT INTO albums (id, title, clean_title, album_artist, artist_id, year, folder_path)
		VALUES (1, 'Bandana I', 'bandana i', 'Big Baby Tape\\Kizaru', 1, 2021, 'Bandana I');
		
		-- Сингл в сборнике "Singles" от Aarne
		INSERT INTO albums (id, title, clean_title, album_artist, artist_id, year, folder_path)
		VALUES (2, 'Singles', 'singles', 'Aarne', 2, 2024, 'Singles');
		
		-- Дорожка сингла "Вместе" в таблице tracks
		INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, mtime)
		VALUES (1, 2, 2, 'Вместе', 'вместе', 'Aarne/Singles/01. Вместе.mp3', 1000, 1.0),
		       (2, 1, 1, 'Bandana', 'bandana', 'Bandana I/01.mp3', 1000, 1.0);
	`)
	if err != nil {
		t.Fatalf("failed to setup test database: %v", err)
	}

	agg := NewDiscographyAggregator(nil, nil, nil, database)

	releases := []AggregatedRelease{
		// Релиз 1: Bandana I, сканируется под Kizaru (хотя в БД artist_id = Big Baby Tape)
		{
			ArtistName:  "Kizaru, Big Baby Tape",
			CleanArtist: "kizaru",
			Title:       "Bandana I",
			CleanTitle:  CleanReleaseTitle("Bandana I"),
			Year:        2021,
			DeezerID:    "1111",
			Tracklist: []db.CatalogTrack{{
				BackfillSelection: db.BackfillSelection{TrackPosition: db.ReleasePosition(1, 1)}, Title: "Bandana",
			}},
		},
		// Релиз 2: Сингл "Вместе" (Aarne, Big Baby Tape), проверяется для Big Baby Tape
		// В albums альбом называется "Singles", но в tracks есть дорожка "Вместе"
		{
			ArtistName:  "Big Baby Tape, Aarne",
			CleanArtist: "big baby tape",
			Title:       "Вместе",
			ReleaseType: "single",
			CleanTitle:  CleanReleaseTitle("Вместе"),
			Year:        2024,
			DeezerID:    "2222",
		},
		// Релиз 3: Совершенно новый релиз от Kizaru (нет в библиотеке)
		{
			ArtistName:  "Kizaru",
			CleanArtist: "kizaru",
			Title:       "Brand New 2026",
			CleanTitle:  CleanReleaseTitle("Brand New 2026"),
			Year:        2026,
			DeezerID:    "3333",
		},
	}

	agg.reconcileWithLibrary(ctx, "Kizaru", releases)

	for _, r := range releases {
		switch r.Title {
		case "Bandana I":
			if r.Status != "in_library" || r.IsRadar {
				t.Errorf("Bandana I: expected in_library and !is_radar, got status=%s, is_radar=%v", r.Status, r.IsRadar)
			}
		case "Вместе":
			if r.Status != "in_library" || r.IsRadar {
				t.Errorf("Вместе (single in tracks): expected in_library and !is_radar, got status=%s, is_radar=%v", r.Status, r.IsRadar)
			}
		case "Brand New 2026":
			if r.Status != "new" || !r.IsRadar {
				t.Errorf("Brand New 2026: expected new and is_radar=true, got status=%s, is_radar=%v", r.Status, r.IsRadar)
			}
		}
	}
}

func TestExtractAllReleaseArtists(t *testing.T) {
	contributors := []DeezerArtist{
		{Name: "21 Savage"},
		{Name: "Drake"},
	}
	artists := ExtractAllReleaseArtists("Drake", contributors, "Her Loss (feat. Travis Scott)")
	want := []string{"Drake", "21 Savage", "Travis Scott"}

	if len(artists) != len(want) {
		t.Fatalf("expected %d artists, got %d: %v", len(want), len(artists), artists)
	}
	for i, w := range want {
		if artists[i] != w {
			t.Errorf("artist[%d]: got %q, want %q", i, artists[i], w)
		}
	}
}

func TestIsDeluxeOrRemaster(t *testing.T) {
	cases := []struct {
		title    string
		expected bool
	}{
		{"In Rainbows (Deluxe Edition)", true},
		{"OK Computer (Remastered 2017)", true},
		{"Kid A (Bonus Tracks)", true},
		{"Amnesiac (Expanded Edition)", true},
		{"The Bends (20th Anniversary Edition)", true},
		{"Hail to the Thief (Special Edition)", true},
		{"Paranoid Android", false},
		{"Karma Police", false},
	}

	for _, tc := range cases {
		if got := IsDeluxeOrRemaster(tc.title); got != tc.expected {
			t.Errorf("IsDeluxeOrRemaster(%q) = %v, want %v", tc.title, got, tc.expected)
		}
	}
}

func TestAggregator_MultiArtistName(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/search/artist" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": 55, "name": "PrimaryArtist"},
				},
			})
			return
		}
		if r.URL.Path == "/artist/55/albums" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"id":           501,
						"title":        "Big Collab",
						"release_date": "2024-01-01",
						"record_type":  "album",
						"nb_tracks":    10,
						"artist": map[string]any{
							"name": "PrimaryArtist",
						},
					},
				},
			})
			return
		}
		if r.URL.Path == "/album/501" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    501,
				"title": "Big Collab",
				"contributors": []map[string]any{
					{"name": "PrimaryArtist"},
					{"name": "CoArtistOne"},
					{"name": "CoArtistTwo"},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	deezerClient := NewDeezerClient()
	deezerClient.SetBaseURL(ts.URL)

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_agg.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	agg := NewDiscographyAggregator(deezerClient, nil, nil, database)
	releases, err := agg.Aggregate(context.Background(), "PrimaryArtist", "", nil)
	if err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	if len(releases) != 1 {
		t.Fatalf("expected 1 aggregated release, got %d", len(releases))
	}

	rel := releases[0]
	expectedArtist := "PrimaryArtist, CoArtistOne, CoArtistTwo"
	if rel.ArtistName != expectedArtist {
		t.Errorf("expected ArtistName %q, got %q", expectedArtist, rel.ArtistName)
	}
}

func TestDeezerClient_AdditionalMethods(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/artist/27" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":         27,
				"name":       "Daft Punk",
				"picture_xl": "https://example.com/dp_xl.jpg",
			})
			return
		}
		if r.URL.Path == "/search/track" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"id":             111,
						"title":          "One More Time",
						"duration":       320,
						"track_position": 1,
						"artist": map[string]any{
							"id":   27,
							"name": "Daft Punk",
						},
					},
				},
			})
			return
		}
		if r.URL.Path == "/cover.jpg" {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("fake_image_bytes"))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	client := NewDeezerClient()
	client.SetBaseURL(ts.URL)

	// 1. GetArtist
	art, err := client.GetArtist(context.Background(), "27")
	if err != nil {
		t.Fatalf("GetArtist failed: %v", err)
	}
	if art.Name != "Daft Punk" {
		t.Errorf("expected Daft Punk, got %q", art.Name)
	}

	// 2. SearchTracks
	tracks, err := client.SearchTracks(context.Background(), "One More Time")
	if err != nil {
		t.Fatalf("SearchTracks failed: %v", err)
	}
	if len(tracks) != 1 || tracks[0].Title != "One More Time" {
		t.Errorf("expected 1 track 'One More Time', got %+v", tracks)
	}

	// 3. SearchTrackVersions
	versions, err := client.SearchTrackVersions(context.Background(), "Daft Punk", "One More Time")
	if err != nil {
		t.Fatalf("SearchTrackVersions failed: %v", err)
	}
	if len(versions) != 1 {
		t.Errorf("expected 1 track version, got %d", len(versions))
	}

	// 4. DownloadCoverBytes
	bytes, err := client.DownloadCoverBytes(context.Background(), ts.URL+"/cover.jpg")
	if err != nil {
		t.Fatalf("DownloadCoverBytes failed: %v", err)
	}
	if string(bytes) != "fake_image_bytes" {
		t.Errorf("unexpected image bytes: %s", string(bytes))
	}
}

func TestDeduplicateReleases(t *testing.T) {
	agg := NewDiscographyAggregator(nil, nil, nil, nil)

	raw := []AggregatedRelease{
		{
			Title:      "Discovery",
			CleanTitle: CleanReleaseTitle("Discovery"),
			ArtistName: "Daft Punk",
			Year:       2001,
			DeezerID:   "101",
			TrackCount: 14,
		},
		{
			Title:         "Discovery",
			CleanTitle:    CleanReleaseTitle("Discovery"),
			ArtistName:    "Daft Punk, Romanthony",
			Year:          2001,
			MusicBrainzID: "mb-disc-101",
			DeezerID:      "101",
			TrackCount:    14,
		},
		{
			Title:      "Human After All",
			CleanTitle: CleanReleaseTitle("Human After All"),
			ArtistName: "Daft Punk",
			Year:       2005,
			DeezerID:   "202",
			TrackCount: 10,
		},
	}

	deduped := agg.deduplicateReleases(raw)
	if len(deduped) != 2 {
		t.Fatalf("expected 2 deduped releases, got %d", len(deduped))
	}

	disc := deduped[0]
	if disc.DeezerID != "101" || disc.MusicBrainzID != "mb-disc-101" {
		t.Errorf("expected merged IDs (deezer: 101, mb: mb-disc-101), got deezer:%s mb:%s", disc.DeezerID, disc.MusicBrainzID)
	}
	if disc.ArtistName != "Daft Punk, Romanthony" {
		t.Errorf("expected merged artist 'Daft Punk, Romanthony', got %q", disc.ArtistName)
	}
}
