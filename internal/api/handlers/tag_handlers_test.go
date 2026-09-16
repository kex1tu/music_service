package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
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
