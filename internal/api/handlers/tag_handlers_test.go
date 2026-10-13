package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
)

func setupTestTagHandler(t *testing.T) (*TagHandler, *sandbox.PathSandbox, string) {
	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	tmpDir := filepath.Join(tempDir, "tmp")
	_ = os.MkdirAll(musicDir, 0755)
	_ = os.MkdirAll(tmpDir, 0755)

	musicSb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatal(err)
	}
	tmpSb, err := sandbox.NewPathSandbox(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	orgSvc := organizer.NewService(musicSb, tmpSb)
	handler := NewTagHandler(musicSb, tmpSb, orgSvc, nil)

	return handler, musicSb, musicDir
}

func TestTagHandler_GetTags_Directory(t *testing.T) {
	h, _, musicDir := setupTestTagHandler(t)

	albumDir := filepath.Join(musicDir, "MyAlbum")
	_ = os.MkdirAll(albumDir, 0755)

	// Create cover.jpg
	_ = os.WriteFile(filepath.Join(albumDir, "cover.jpg"), []byte("cover-data"), 0644)

	// Create 2 tracks
	t1 := filepath.Join(albumDir, "02. Second.mp3")
	t2 := filepath.Join(albumDir, "01. First.mp3")
	_ = os.WriteFile(t1, []byte("audio2"), 0644)
	_ = os.WriteFile(t2, []byte("audio1"), 0644)

	_ = id3.WriteTags(t1, id3.TrackTags{
		Title:       "Second Song",
		Artist:      "Band",
		Album:       "MyAlbum",
		TrackNumber: 2,
		Year:        2024,
		Genre:       "Rock",
	}, nil)

	_ = id3.WriteTags(t2, id3.TrackTags{
		Title:       "First Song",
		Artist:      "Band",
		Album:       "MyAlbum",
		TrackNumber: 1,
		Year:        2024,
		Genre:       "Rock",
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/tags?path=MyAlbum", nil)
	w := httptest.NewRecorder()
	h.GetTags(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Album struct {
			AlbumArtist string `json:"album_artist"`
			Album       string `json:"album"`
			Year        *int   `json:"year"`
			Genre       string `json:"genre"`
			TrackTotal  int    `json:"track_total"`
		} `json:"album"`
		Tracks    []id3.TrackTags `json:"tracks"`
		SourceDir string          `json:"source_dir"`
		CoverPath *string         `json:"cover_path"`
		HasCover  bool            `json:"has_cover"`
	}

	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Album.Album != "MyAlbum" {
		t.Errorf("expected album 'MyAlbum', got %q", resp.Album.Album)
	}
	if resp.Album.AlbumArtist != "Band" {
		t.Errorf("expected album_artist 'Band', got %q", resp.Album.AlbumArtist)
	}
	if resp.Album.Year == nil || *resp.Album.Year != 2024 {
		t.Errorf("expected year 2024, got %v", resp.Album.Year)
	}
	if len(resp.Tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(resp.Tracks))
	}
	// Verify sorted by TrackNumber
	if resp.Tracks[0].TrackNumber != 1 || resp.Tracks[0].Title != "First Song" {
		t.Errorf("expected track 0 to be First Song, got %+v", resp.Tracks[0])
	}
	if resp.Tracks[1].TrackNumber != 2 || resp.Tracks[1].Title != "Second Song" {
		t.Errorf("expected track 1 to be Second Song, got %+v", resp.Tracks[1])
	}
	if !resp.HasCover {
		t.Errorf("expected has_cover = true due to cover.jpg")
	}
	if resp.CoverPath == nil || *resp.CoverPath != "MyAlbum/cover.jpg" {
		t.Errorf("expected cover_path 'MyAlbum/cover.jpg', got %v", resp.CoverPath)
	}
}

func TestTagHandler_GetTags_SingleFile(t *testing.T) {
	h, _, musicDir := setupTestTagHandler(t)

	albumDir := filepath.Join(musicDir, "SingleAlbum")
	_ = os.MkdirAll(albumDir, 0755)

	t1 := filepath.Join(albumDir, "01. 21 Guns.mp3")
	_ = os.WriteFile(t1, []byte("audio"), 0644)

	_ = id3.WriteTags(t1, id3.TrackTags{
		Title:       "21 Guns",
		Artist:      "Green Day",
		Album:       "21st Century Breakdown",
		TrackNumber: 1,
		Year:        2009,
		Genre:       "Punk Rock",
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/tags?path=SingleAlbum/01.+21+Guns.mp3&as_album=true", nil)
	w := httptest.NewRecorder()
	h.GetTags(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Album struct {
			Album       string `json:"album"`
			AlbumArtist string `json:"album_artist"`
			Year        *int   `json:"year"`
		} `json:"album"`
		Tracks []id3.TrackTags `json:"tracks"`
		Title  string          `json:"title"`
	}

	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Album.Album != "21st Century Breakdown" {
		t.Errorf("expected album '21st Century Breakdown', got %q", resp.Album.Album)
	}
	if len(resp.Tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(resp.Tracks))
	}
	if resp.Tracks[0].Title != "21 Guns" {
		t.Errorf("expected title '21 Guns', got %q", resp.Tracks[0].Title)
	}
	if resp.Title != "21 Guns" {
		t.Errorf("expected backwards-compatible title '21 Guns', got %q", resp.Title)
	}
}

func TestTagHandler_GetDeezerAlbumDetails_ResolvesContributors(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/album/123":
			_, _ = w.Write([]byte(`{
				"id": 123,
				"title": "AA LANGUAGE",
				"artist": {"id": 1, "name": "Aarne"},
				"release_date": "2022-08-12",
				"cover_xl": "https://e-cdns-images.dzcdn.net/cover_xl.jpg",
				"genres": {"data": [{"id": 1, "name": "Hip Hop"}]},
				"tracks": {
					"data": [
						{
							"id": 1001,
							"title": "Тесно",
							"track_position": 1,
							"disk_number": 1,
							"duration": 158,
							"artist": {"id": 1, "name": "Aarne"}
						}
					]
				}
			}`))
		case "/track/1001":
			_, _ = w.Write([]byte(`{
				"id": 1001,
				"title": "Тесно",
				"track_position": 1,
				"disk_number": 1,
				"duration": 158,
				"isrc": "RUA123456789",
				"artist": {"id": 1, "name": "Aarne"},
				"album": {
					"id": 123,
					"title": "AA LANGUAGE",
					"release_date": "2022-08-12",
					"cover_xl": "https://e-cdns-images.dzcdn.net/cover_xl.jpg"
				},
				"contributors": [
					{"id": 1, "name": "Aarne"},
					{"id": 2, "name": "Bushido Zho"},
					{"id": 3, "name": "ANIKV"}
				]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	t.Setenv("DEEZER_API_BASE", mockServer.URL)
	t.Setenv("DEEZER_FALLBACK_API_BASE", mockServer.URL)

	h, _, _ := setupTestTagHandler(t)
	deezerClient := resolver.NewDeezerClient()
	h.deezer = deezerClient

	req := httptest.NewRequest(http.MethodGet, "/tags/deezer/albums/123", nil)
	// chi route context with URL param "id"
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.GetDeezerAlbumDetails(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Artist string `json:"artist"`
		Tracks []struct {
			TrackNumber  int      `json:"track_number"`
			Title        string   `json:"title"`
			Artist       string   `json:"artist"`
			Contributors []string `json:"contributors"`
			ISRC         string   `json:"isrc"`
		} `json:"tracks"`
	}

	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(resp.Tracks))
	}

	expectedArtist := `Aarne\\Bushido Zho\\ANIKV`
	if resp.Tracks[0].Artist != expectedArtist {
		t.Errorf("expected track artist %q with contributors, got %q", expectedArtist, resp.Tracks[0].Artist)
	}
	if resp.Tracks[0].ISRC != "RUA123456789" {
		t.Errorf("expected track ISRC 'RUA123456789', got %q", resp.Tracks[0].ISRC)
	}
	if len(resp.Tracks[0].Contributors) != 3 {
		t.Errorf("expected 3 contributors, got %v", resp.Tracks[0].Contributors)
	}
}

func TestTagHandler_SearchDeezerTracks_And_GetDetails(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search/track":
			_, _ = w.Write([]byte(`{
				"data": [
					{
						"id": 2002,
						"title": "Deja Vu",
						"duration": 180,
						"artist": {"id": 10, "name": "Big Baby Tape"},
						"album": {
							"id": 500,
							"title": "Bandana I",
							"release_date": "2021-10-22",
							"cover_xl": "https://e-cdns-images.dzcdn.net/bandana.jpg"
						},
						"contributors": [
							{"id": 10, "name": "Big Baby Tape"},
							{"id": 11, "name": "Kizaru"}
						]
					}
				]
			}`))
		case "/track/2002":
			_, _ = w.Write([]byte(`{
				"id": 2002,
				"title": "Deja Vu",
				"track_position": 2,
				"disk_number": 1,
				"duration": 180,
				"isrc": "RU9999999999",
				"artist": {"id": 10, "name": "Big Baby Tape"},
				"album": {
					"id": 500,
					"title": "Bandana I",
					"release_date": "2021-10-22",
					"cover_xl": "https://e-cdns-images.dzcdn.net/bandana.jpg",
					"artist": {"id": 10, "name": "Big Baby Tape"}
				},
				"contributors": [
					{"id": 10, "name": "Big Baby Tape"},
					{"id": 11, "name": "Kizaru"}
				]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	t.Setenv("DEEZER_API_BASE", mockServer.URL)
	t.Setenv("DEEZER_FALLBACK_API_BASE", mockServer.URL)

	h, _, _ := setupTestTagHandler(t)
	deezerClient := resolver.NewDeezerClient()
	h.deezer = deezerClient

	// Test Search
	reqSearch := httptest.NewRequest(http.MethodGet, "/tags/deezer/tracks?query=Bandana", nil)
	wSearch := httptest.NewRecorder()
	h.SearchDeezerTracks(wSearch, reqSearch)

	if wSearch.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", wSearch.Code, wSearch.Body.String())
	}

	var searchItems []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Artist string `json:"artist"`
		Album  string `json:"album"`
	}
	if err := json.NewDecoder(wSearch.Body).Decode(&searchItems); err != nil {
		t.Fatalf("failed to decode search: %v", err)
	}
	if len(searchItems) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(searchItems))
	}
	if searchItems[0].Artist != `Big Baby Tape\\Kizaru` {
		t.Errorf("expected multi-artist 'Big Baby Tape\\\\Kizaru', got %q", searchItems[0].Artist)
	}

	// Test Details
	reqDetails := httptest.NewRequest(http.MethodGet, "/tags/deezer/tracks/2002", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "2002")
	reqDetails = reqDetails.WithContext(context.WithValue(reqDetails.Context(), chi.RouteCtxKey, rctx))

	wDetails := httptest.NewRecorder()
	h.GetDeezerTrackDetails(wDetails, reqDetails)

	if wDetails.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", wDetails.Code, wDetails.Body.String())
	}

	var trackDetails struct {
		ID           string   `json:"id"`
		Title        string   `json:"title"`
		Artist       string   `json:"artist"`
		Contributors []string `json:"contributors"`
		Album        string   `json:"album"`
		AlbumArtist  string   `json:"album_artist"`
		Year         int      `json:"year"`
		TrackNumber  int      `json:"track_number"`
	}
	if err := json.NewDecoder(wDetails.Body).Decode(&trackDetails); err != nil {
		t.Fatalf("failed to decode details: %v", err)
	}
	if trackDetails.Artist != `Big Baby Tape\\Kizaru` {
		t.Errorf("expected track artist 'Big Baby Tape\\\\Kizaru', got %q", trackDetails.Artist)
	}
	if len(trackDetails.Contributors) != 2 {
		t.Errorf("expected 2 contributors, got %v", trackDetails.Contributors)
	}
	if trackDetails.Album != "Bandana I" {
		t.Errorf("expected album 'Bandana I', got %q", trackDetails.Album)
	}
}

func TestTagHandler_BatchSaveTags_SingleTrackPropagation(t *testing.T) {
	h, _, musicDir := setupTestTagHandler(t)

	singleDir := filepath.Join(musicDir, "MySingle")
	_ = os.MkdirAll(singleDir, 0755)

	t1 := filepath.Join(singleDir, "01. Song.mp3")
	_ = os.WriteFile(t1, []byte("audio-single"), 0644)

	_ = id3.WriteTags(t1, id3.TrackTags{
		Title:       "Single Song",
		Artist:      "Super Artist",
		TrackNumber: 1,
		Year:        2025,
		Genre:       "Electronic",
	}, nil)

	body := `{
		"album": {
			"album_artist": "",
			"album": "",
			"year": null,
			"genre": ""
		},
		"tracks": [
			{
				"path": "MySingle/01. Song.mp3",
				"title": "Single Song",
				"artist": "Super Artist",
				"album": "",
				"album_artist": "",
				"track_number": 1,
				"year": 2025,
				"genre": "Electronic"
			}
		],
		"source_dir": "MySingle"
	}`

	req := httptest.NewRequest(http.MethodPost, "/tags/batch", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.BatchSaveTags(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Success        bool            `json:"success"`
		DestinationDir string          `json:"destination_dir"`
		Tracks         []id3.TrackTags `json:"tracks"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !resp.Success {
		t.Fatalf("expected success true")
	}

	expectedDir := "Single Song - Super Artist"
	if resp.DestinationDir != expectedDir {
		t.Errorf("expected destination_dir %q, got %q", expectedDir, resp.DestinationDir)
	}

	if len(resp.Tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(resp.Tracks))
	}
	tr := resp.Tracks[0]
	if tr.Album != "Single Song" {
		t.Errorf("expected album 'Single Song', got %q", tr.Album)
	}
	if tr.AlbumArtist != "Super Artist" {
		t.Errorf("expected album_artist 'Super Artist', got %q", tr.AlbumArtist)
	}
	if tr.Year != 2025 {
		t.Errorf("expected year 2025, got %d", tr.Year)
	}
	if tr.Genre != "Electronic" {
		t.Errorf("expected genre 'Electronic', got %q", tr.Genre)
	}
}
