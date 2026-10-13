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
)

func TestFileHandler_RenamePattern_SuccessAndLyrics(t *testing.T) {
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
	defer database.Close()

	// 1. Создаем папку и трек с сопутствующими текстами .lrc и .txt
	albumDir := filepath.Join(musicDir, "Rock")
	if err := os.MkdirAll(albumDir, 0755); err != nil {
		t.Fatal(err)
	}

	oldAudioPath := filepath.Join(albumDir, "01. OldSong.mp3")
	oldLrcPath := filepath.Join(albumDir, "01. OldSong.lrc")
	oldTxtPath := filepath.Join(albumDir, "01. OldSong.txt")

	_ = os.WriteFile(oldAudioPath, []byte("audio-content"), 0644)
	_ = os.WriteFile(oldLrcPath, []byte("[00:01.00]Lyrics line"), 0644)
	_ = os.WriteFile(oldTxtPath, []byte("Plain text lyrics"), 0644)

	// Добавляем запись в БД
	relOldPath := "Rock/01. OldSong.mp3"
	artRes, err := database.Conn().Exec("INSERT INTO artists (name, clean_name) VALUES ('RockBand', 'rockband')")
	if err != nil {
		t.Fatalf("failed to insert artist: %v", err)
	}
	artID, _ := artRes.LastInsertId()

	albRes, err := database.Conn().Exec("INSERT INTO albums (title, clean_title, artist_id, folder_path) VALUES ('RockAlbum', 'rockalbum', ?, 'Rock')", artID)
	if err != nil {
		t.Fatalf("failed to insert album: %v", err)
	}
	albID, _ := albRes.LastInsertId()

	_, err = database.Conn().Exec(`
		INSERT INTO tracks (album_id, artist_id, title, clean_title, file_path, file_size, bitrate, mtime)
		VALUES (?, ?, 'OldSong', 'oldsong', ?, 1234, 320000, 100.0)
	`, albID, artID, relOldPath)
	if err != nil {
		t.Fatalf("failed to insert test track: %v", err)
	}

	// 2. Отправляем запрос на переименование (dry_run = false)
	reqBody := RenamePatternRequest{
		DryRun: false,
		Items: []RenamePatternItem{
			{
				CurrentPath: relOldPath,
				NewName:     "01 - RockBand - NewSong.mp3",
			},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/files/rename-pattern", bytes.NewReader(bodyBytes))
	w := httptest.NewRecorder()

	h.RenamePattern(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var results []RenamePatternResult
	if err := json.NewDecoder(w.Body).Decode(&results); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	res := results[0]
	if res.Status != "ok" {
		t.Errorf("expected status 'ok', got %q (error: %s)", res.Status, res.Error)
	}
	expectedRel := "Rock/01 - RockBand - NewSong.mp3"
	if res.NewPath != expectedRel {
		t.Errorf("expected new_path %q, got %q", expectedRel, res.NewPath)
	}

	// 3. Проверяем файловую систему
	newAudioPath := filepath.Join(albumDir, "01 - RockBand - NewSong.mp3")
	newLrcPath := filepath.Join(albumDir, "01 - RockBand - NewSong.lrc")
	newTxtPath := filepath.Join(albumDir, "01 - RockBand - NewSong.txt")

	if _, err := os.Stat(oldAudioPath); !os.IsNotExist(err) {
		t.Errorf("old audio file still exists on disk")
	}
	if _, err := os.Stat(newAudioPath); err != nil {
		t.Errorf("new audio file does not exist on disk: %v", err)
	}

	if _, err := os.Stat(oldLrcPath); !os.IsNotExist(err) {
		t.Errorf("old .lrc file still exists on disk")
	}
	lrcContent, err := os.ReadFile(newLrcPath)
	if err != nil {
		t.Errorf("new .lrc file does not exist: %v", err)
	} else if string(lrcContent) != "[00:01.00]Lyrics line" {
		t.Errorf("unexpected .lrc content: %s", string(lrcContent))
	}

	if _, err := os.Stat(oldTxtPath); !os.IsNotExist(err) {
		t.Errorf("old .txt file still exists on disk")
	}
	txtContent, err := os.ReadFile(newTxtPath)
	if err != nil {
		t.Errorf("new .txt file does not exist: %v", err)
	} else if string(txtContent) != "Plain text lyrics" {
		t.Errorf("unexpected .txt content: %s", string(txtContent))
	}

	// 4. Проверяем обновление пути в БД
	var dbPath string
	err = database.Conn().QueryRow("SELECT file_path FROM tracks WHERE file_path = ?", expectedRel).Scan(&dbPath)
	if err != nil {
		t.Errorf("track file_path was not updated in DB: %v", err)
	}
	if dbPath != expectedRel {
		t.Errorf("expected DB file_path %q, got %q", expectedRel, dbPath)
	}
}

func TestFileHandler_RenamePattern_DryRun(t *testing.T) {
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
	defer database.Close()

	albumDir := filepath.Join(musicDir, "Pop")
	_ = os.MkdirAll(albumDir, 0755)
	audioPath := filepath.Join(albumDir, "01. Track.mp3")
	lrcPath := filepath.Join(albumDir, "01. Track.lrc")
	_ = os.WriteFile(audioPath, []byte("data"), 0644)
	_ = os.WriteFile(lrcPath, []byte("[00:00.00]Pop"), 0644)

	reqBody := RenamePatternRequest{
		DryRun: true,
		Items: []RenamePatternItem{
			{
				CurrentPath: "Pop/01. Track.mp3",
				NewName:     "01 - PopStar - Track.mp3",
			},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/files/rename-pattern", bytes.NewReader(bodyBytes))
	w := httptest.NewRecorder()

	h.RenamePattern(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var results []RenamePatternResult
	_ = json.NewDecoder(w.Body).Decode(&results)
	if len(results) != 1 || results[0].Status != "ok" {
		t.Fatalf("expected status ok in dry-run, got %+v", results)
	}

	// В режиме dry_run файлы на диске НЕ должны измениться
	if _, err := os.Stat(audioPath); err != nil {
		t.Errorf("source audio should still exist in dry run")
	}
	if _, err := os.Stat(lrcPath); err != nil {
		t.Errorf("source lrc should still exist in dry run")
	}
	targetPath := filepath.Join(albumDir, "01 - PopStar - Track.mp3")
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Errorf("target file should not exist after dry run")
	}
}

func TestFileHandler_RenamePattern_Collision(t *testing.T) {
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
	defer database.Close()

	albumDir := filepath.Join(musicDir, "Jazz")
	_ = os.MkdirAll(albumDir, 0755)
	f1 := filepath.Join(albumDir, "01. Track.mp3")
	f2 := filepath.Join(albumDir, "02. Track.mp3")
	_ = os.WriteFile(f1, []byte("audio1"), 0644)
	_ = os.WriteFile(f2, []byte("audio2"), 0644)

	// Коллизия 1: Попытка переименовать f1 в f2 (файл f2 уже существует на диске)
	reqBody := RenamePatternRequest{
		DryRun: false,
		Items: []RenamePatternItem{
			{
				CurrentPath: "Jazz/01. Track.mp3",
				NewName:     "02. Track.mp3",
			},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/files/rename-pattern", bytes.NewReader(bodyBytes))
	w := httptest.NewRecorder()
	h.RenamePattern(w, req)

	var results []RenamePatternResult
	_ = json.NewDecoder(w.Body).Decode(&results)
	if len(results) != 1 {
		t.Fatalf("expected 1 result")
	}
	if results[0].Status != "conflict" {
		t.Errorf("expected conflict status, got %q", results[0].Status)
	}

	// Коллизия 2: Два трека в одном запросе переименовываются в одно и то же новое имя
	reqBody2 := RenamePatternRequest{
		DryRun: true,
		Items: []RenamePatternItem{
			{
				CurrentPath: "Jazz/01. Track.mp3",
				NewName:     "SameDestination.mp3",
			},
			{
				CurrentPath: "Jazz/02. Track.mp3",
				NewName:     "SameDestination.mp3",
			},
		},
	}
	bodyBytes2, _ := json.Marshal(reqBody2)
	req2 := httptest.NewRequest(http.MethodPost, "/files/rename-pattern", bytes.NewReader(bodyBytes2))
	w2 := httptest.NewRecorder()
	h.RenamePattern(w2, req2)

	var results2 []RenamePatternResult
	_ = json.NewDecoder(w2.Body).Decode(&results2)
	if len(results2) != 2 {
		t.Fatalf("expected 2 results")
	}
	if results2[0].Status != "ok" {
		t.Errorf("first item should succeed, got %q", results2[0].Status)
	}
	if results2[1].Status != "conflict" {
		t.Errorf("second item should conflict, got %q", results2[1].Status)
	}
}

func TestFileHandler_RenamePattern_SandboxTraversal(t *testing.T) {
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
	defer database.Close()

	albumDir := filepath.Join(musicDir, "Metal")
	_ = os.MkdirAll(albumDir, 0755)
	validFile := filepath.Join(albumDir, "track.mp3")
	_ = os.WriteFile(validFile, []byte("metal"), 0644)

	traversalTests := []struct {
		name        string
		currentPath string
		newName     string
	}{
		{
			name:        "current_path traversal ../../",
			currentPath: "../../etc/shadow",
			newName:     "valid.mp3",
		},
		{
			name:        "new_name traversal ../../",
			currentPath: "Metal/track.mp3",
			newName:     "../../../../etc/danger.mp3",
		},
		{
			name:        "current_path null byte",
			currentPath: "Metal/track\x00.mp3",
			newName:     "valid.mp3",
		},
		{
			name:        "new_name null byte",
			currentPath: "Metal/track.mp3",
			newName:     "hack\x00.mp3",
		},
		{
			name:        "URL-encoded traversal %2e%2e",
			currentPath: "Metal/track.mp3",
			newName:     "%2e%2e%2f%2e%2e%2fescaped.mp3",
		},
	}

	for _, tc := range traversalTests {
		t.Run(tc.name, func(t *testing.T) {
			reqBody := RenamePatternRequest{
				DryRun: false,
				Items: []RenamePatternItem{
					{
						CurrentPath: tc.currentPath,
						NewName:     tc.newName,
					},
				},
			}
			bodyBytes, _ := json.Marshal(reqBody)
			req := httptest.NewRequest(http.MethodPost, "/files/rename-pattern", bytes.NewReader(bodyBytes))
			w := httptest.NewRecorder()
			h.RenamePattern(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 with result errors, got %d", w.Code)
			}

			var results []RenamePatternResult
			_ = json.NewDecoder(w.Body).Decode(&results)
			if len(results) != 1 {
				t.Fatalf("expected 1 result")
			}
			if results[0].Status != "error" {
				t.Errorf("expected status 'error' for traversal, got %q", results[0].Status)
			}
			if results[0].Error == "" {
				t.Errorf("expected non-empty error message")
			}

			// Проверяем, что исходный файл не тронут
			if _, err := os.Stat(validFile); err != nil {
				t.Errorf("valid source file was unexpectedly modified or deleted")
			}
		})
	}
}

func TestFileHandler_RenamePattern_NonExistentSource(t *testing.T) {
	h, _, _, database, _, _ := setupTestFileHandler(t)
	defer database.Close()

	reqBody := RenamePatternRequest{
		DryRun: false,
		Items: []RenamePatternItem{
			{
				CurrentPath: "Ghost/NotFound.mp3",
				NewName:     "NewName.mp3",
			},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/files/rename-pattern", bytes.NewReader(bodyBytes))
	w := httptest.NewRecorder()
	h.RenamePattern(w, req)

	var results []RenamePatternResult
	_ = json.NewDecoder(w.Body).Decode(&results)
	if len(results) != 1 || results[0].Status != "error" {
		t.Fatalf("expected error status for non-existent source, got %+v", results)
	}
	if !strings.Contains(results[0].Error, "does not exist") {
		t.Errorf("expected 'does not exist' error, got %q", results[0].Error)
	}
}
