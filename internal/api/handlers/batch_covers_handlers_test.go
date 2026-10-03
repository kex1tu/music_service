package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

	req := httptest.NewRequest(http.MethodPost, "/files/audit/normalize-navidrome", nil)
	w := httptest.NewRecorder()
	h.NormalizeNavidrome(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Errorf("expected success: true, got %v", resp)
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
