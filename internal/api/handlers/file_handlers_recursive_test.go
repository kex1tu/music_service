package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"music-manager/internal/id3"
)

func TestFileHandler_ListFiles_Recursive(t *testing.T) {
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
	defer database.Close()

	// 1. Создаем структуру каталогов
	album1 := filepath.Join(musicDir, "Rock", "Album1")
	album2 := filepath.Join(musicDir, "Pop", "Album2")
	hiddenDir := filepath.Join(musicDir, ".hidden")
	tmpDir := filepath.Join(musicDir, "tmp")
	tmpDlDir := filepath.Join(musicDir, "tmp_downloads", "job1")

	for _, d := range []string{album1, album2, hiddenDir, tmpDir, tmpDlDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("failed to create dir %s: %v", d, err)
		}
	}

	// 2. Создаем файлы
	f1 := filepath.Join(album1, "01. Song.mp3")
	f2 := filepath.Join(album1, "02. Track.mp3")
	f3 := filepath.Join(album2, "01. Hit.mp3")
	fLrc := filepath.Join(album2, "01. Hit.lrc")
	fTxt := filepath.Join(album2, "notes.txt")
	fCover := filepath.Join(album2, "cover.jpg")
	fHidden := filepath.Join(hiddenDir, "secret.mp3")
	fTmp := filepath.Join(tmpDir, "temp.mp3")
	fTmpDl := filepath.Join(tmpDlDir, "dl.mp3")

	dummyMP3 := []byte("fake mp3 audio frame stream")
	_ = os.WriteFile(f1, dummyMP3, 0644)
	_ = os.WriteFile(f2, dummyMP3, 0644)
	_ = os.WriteFile(f3, dummyMP3, 0644)
	_ = os.WriteFile(fLrc, []byte("[00:01.00]Hit lyrics"), 0644)
	_ = os.WriteFile(fTxt, []byte("album notes"), 0644)
	_ = os.WriteFile(fCover, []byte("image bytes"), 0644)
	_ = os.WriteFile(fHidden, dummyMP3, 0644)
	_ = os.WriteFile(fTmp, dummyMP3, 0644)
	_ = os.WriteFile(fTmpDl, dummyMP3, 0644)

	// Пишем ID3 теги для f1
	tags := id3.TrackTags{
		Title:       "Song One",
		Artist:      "RockBand",
		Album:       "Album1",
		TrackNumber: 1,
		Duration:    185.0,
	}
	_ = id3.WriteTags(f1, tags, nil)

	// Тест 1: Рекурсивный запрос корня
	req := httptest.NewRequest(http.MethodGet, "/files?recursive=true", nil)
	w := httptest.NewRecorder()
	h.ListFiles(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp directoryListingResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.TotalFiles != 3 {
		t.Errorf("expected 3 mp3 files, got %d", resp.TotalFiles)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(resp.Items))
	}

	// Проверяем, что в выдаче только MP3 файлы и нет скрытых/tmp каталогов
	var foundSongOne bool
	for _, item := range resp.Items {
		if item.Type != "file" {
			t.Errorf("expected type 'file', got %q for %s", item.Type, item.Path)
		}
		if !strings.HasSuffix(strings.ToLower(item.Name), ".mp3") {
			t.Errorf("expected .mp3 extension, got %s", item.Name)
		}
		if item.ID == nil || *item.ID == "" {
			t.Errorf("expected track ID hash for %s", item.Path)
		}
		if item.StreamURL == nil || !strings.HasPrefix(*item.StreamURL, "/api/tracks/") {
			t.Errorf("expected valid stream URL for %s", item.Path)
		}
		if strings.Contains(item.Path, ".hidden") || strings.Contains(item.Path, "tmp") {
			t.Errorf("unexpected directory in path: %s", item.Path)
		}

		if item.Name == "01. Song.mp3" {
			foundSongOne = true
			if item.Title == nil || *item.Title != "Song One" {
				t.Errorf("expected title 'Song One', got %v", item.Title)
			}
			if item.Artist == nil || *item.Artist != "RockBand" {
				t.Errorf("expected artist 'RockBand', got %v", item.Artist)
			}
			if item.TrackNumber == nil || *item.TrackNumber != 1 {
				t.Errorf("expected track number 1, got %v", item.TrackNumber)
			}
		}

		if item.Name == "01. Hit.mp3" {
			// Должен обнаружить сопутствующий .lrc файл
			if item.LyricsStatus != "lrc" {
				t.Errorf("expected lyrics_status 'lrc' for 01. Hit.mp3, got %q", item.LyricsStatus)
			}
		}
	}

	if !foundSongOne {
		t.Errorf("expected 01. Song.mp3 to be found in recursive items")
	}

	// Тест 2: Рекурсивный запрос подкаталога "Rock"
	reqSub := httptest.NewRequest(http.MethodGet, "/files?path=Rock&recursive=true", nil)
	wSub := httptest.NewRecorder()
	h.ListFiles(wSub, reqSub)

	if wSub.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", wSub.Code)
	}
	var respSub directoryListingResponse
	if err := json.NewDecoder(wSub.Body).Decode(&respSub); err != nil {
		t.Fatalf("failed to decode sub response: %v", err)
	}
	if respSub.TotalFiles != 2 {
		t.Errorf("expected 2 files under Rock, got %d", respSub.TotalFiles)
	}

	// Тест 3: Защита от Path Traversal при recursive=true
	traversalPaths := []string{
		"../../",
		"../",
		"../../etc/passwd",
		"%2e%2e%2f%2e%2e",
	}
	for _, tp := range traversalPaths {
		reqTrav := httptest.NewRequest(http.MethodGet, "/files?path="+tp+"&recursive=true", nil)
		wTrav := httptest.NewRecorder()
		h.ListFiles(wTrav, reqTrav)

		if wTrav.Code != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for traversal path %q, got %d", tp, wTrav.Code)
		}
	}
}

func TestFileHandler_ListFiles_NonRecursive_Compatibility(t *testing.T) {
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
	defer database.Close()

	albumDir := filepath.Join(musicDir, "Classical")
	_ = os.MkdirAll(albumDir, 0755)
	_ = os.WriteFile(filepath.Join(albumDir, "01. Symphony.mp3"), []byte("audio"), 0644)
	_ = os.WriteFile(filepath.Join(musicDir, "root_track.mp3"), []byte("audio"), 0644)

	// Запрос без recursive
	req := httptest.NewRequest(http.MethodGet, "/files", nil)
	w := httptest.NewRecorder()
	h.ListFiles(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp directoryListingResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// В корне должна быть 1 директория (Classical) и 1 файл (root_track.mp3)
	if resp.TotalDirectories != 1 {
		t.Errorf("expected 1 directory, got %d", resp.TotalDirectories)
	}
	if resp.TotalFiles != 1 {
		t.Errorf("expected 1 file, got %d", resp.TotalFiles)
	}

	var foundDir, foundFile bool
	for _, item := range resp.Items {
		if item.Type == "directory" && item.Name == "Classical" {
			foundDir = true
			if item.TrackCount == nil || *item.TrackCount != 1 {
				t.Errorf("expected track count 1 for Classical dir, got %v", item.TrackCount)
			}
		}
		if item.Type == "file" && item.Name == "root_track.mp3" {
			foundFile = true
		}
	}

	if !foundDir {
		t.Errorf("expected Classical directory in listing")
	}
	if !foundFile {
		t.Errorf("expected root_track.mp3 in listing")
	}
}
