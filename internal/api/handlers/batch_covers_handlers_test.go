package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"music-manager/internal/id3"
)

func TestFileHandler_NormalizeNavidrome(t *testing.T) {
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
	defer database.Close()

	albumDir := filepath.Join(musicDir, "Navidrome Test Album")
	_ = os.MkdirAll(albumDir, 0700)

	trackPath := filepath.Join(albumDir, "01. Song.mp3")
	dummyAudio := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 500)...)
	_ = os.WriteFile(trackPath, dummyAudio, 0600)

	tags := id3.TrackTags{
		Title:       "Song",
		Artist:      "Artist A\\\\Artist B",
		Album:       "Navidrome Test Album",
		TrackNumber: 1,
	}
	_ = id3.WriteTags(trackPath, tags, nil)

	// Single track downloaded previously with Album="Single"
	singleDir := filepath.Join(musicDir, "Single Artist")
	_ = os.MkdirAll(singleDir, 0700)
	singleTrack := filepath.Join(singleDir, "01. Hit Song.mp3")
	_ = os.WriteFile(singleTrack, dummyAudio, 0600)
	singleTags := id3.TrackTags{
		Title:       "Hit Song (feat. Star)",
		Artist:      "Lead Artist",
		Album:       "Single",
		AlbumArtist: "",
		TrackNumber: 1,
	}
	_ = id3.WriteTags(singleTrack, singleTags, nil)

	req := httptest.NewRequest(http.MethodPost, "/files/audit/normalize-navidrome", nil)
	w := httptest.NewRecorder()
	h.NormalizeNavidrome(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Success bool `json:"success"`
		Report  struct {
			TotalScanned     int `json:"total_scanned"`
			FilesPermsFixed  int `json:"files_perms_fixed"`
			DirectoriesFixed int `json:"directories_fixed"`
			TagsNormalized   int `json:"tags_normalized"`
			SinglesFixed     int `json:"singles_fixed"`
		} `json:"report"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !resp.Success {
		t.Errorf("expected success: true, got %v", resp)
	}
	if resp.Report.SinglesFixed != 1 {
		t.Errorf("expected SinglesFixed=1, got %d", resp.Report.SinglesFixed)
	}

	// Verify permissions
	st, _ := os.Stat(trackPath)
	if st.Mode().Perm() != 0644 {
		t.Errorf("expected file perm 0644, got %v", st.Mode().Perm())
	}

	dirSt, _ := os.Stat(albumDir)
	if dirSt.Mode().Perm() != 0755 {
		t.Errorf("expected dir perm 0755, got %v", dirSt.Mode().Perm())
	}

	// Verify single track tags on disk
	stTags, err := id3.ReadTags(singleTrack, "Single Artist/01. Hit Song.mp3")
	if err != nil {
		t.Fatalf("failed to read single track tags: %v", err)
	}
	if stTags.Title != "Hit Song" {
		t.Errorf("expected Title 'Hit Song', got %q", stTags.Title)
	}
	if stTags.Album != "Hit Song" {
		t.Errorf("expected Album 'Hit Song', got %q", stTags.Album)
	}
	if stTags.AlbumArtist == "" || !strings.Contains(stTags.AlbumArtist, "Star") {
		t.Errorf("expected AlbumArtist with Star, got %q", stTags.AlbumArtist)
	}
}

func TestFileHandler_ApplyCatalogAlbum(t *testing.T) {
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
	defer database.Close()

	albumDir := filepath.Join(musicDir, "Album To Tag")
	_ = os.MkdirAll(albumDir, 0755)

	t1 := filepath.Join(albumDir, "01. Old Title.mp3")
	dummyAudio := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 500)...)
	_ = os.WriteFile(t1, dummyAudio, 0644)

	tags1 := id3.TrackTags{
		Title:       "Old Title",
		Artist:      "Old Artist",
		Album:       "Album To Tag",
		TrackNumber: 1,
	}
	_ = id3.WriteTags(t1, tags1, nil)

	reqBody := ApplyCatalogAlbumRequest{
		FolderPath:  "Album To Tag",
		Album:       "Catalog Album Title",
		AlbumArtist: "Official Artist",
		Year:        2024,
		Genre:       "Hip-Hop",
		Tracks: []ApplyCatalogAlbumTrackItem{
			{
				Path:        "Album To Tag/01. Old Title.mp3",
				Title:       "New Master Title",
				Artist:      "Official Artist\\\\Featured Artist",
				TrackNumber: 1,
			},
		},
	}

	payload, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/files/audit/tag-issues/apply-catalog-album", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	h.ApplyCatalogAlbum(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	// Verify updated tags on disk
	updatedTags, err := id3.ReadTags(t1, "Album To Tag/01. Old Title.mp3")
	if err != nil {
		t.Fatalf("failed to read updated tags: %v", err)
	}

	if updatedTags.Title != "New Master Title" {
		t.Errorf("expected Title 'New Master Title', got %q", updatedTags.Title)
	}
	if updatedTags.Album != "Catalog Album Title" {
		t.Errorf("expected Album 'Catalog Album Title', got %q", updatedTags.Album)
	}
}
