package organizer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileSHA256(t *testing.T) {
	tempFile, err := os.CreateTemp("", "sha_test_*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())

	_, _ = tempFile.WriteString("hello world")
	_ = tempFile.Close()

	hash, err := FileSHA256(tempFile.Name())
	if err != nil {
		t.Fatalf("FileSHA256 error: %v", err)
	}
	// sha256 of "hello world"
	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if hash != expected {
		t.Errorf("hash mismatch: got %s, want %s", hash, expected)
	}
}

func TestMoveTrackWithCompanions_BasicAndLyrics(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "organizer_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	srcDir := filepath.Join(tempDir, "src")
	destDir := filepath.Join(tempDir, "dest")
	_ = os.MkdirAll(srcDir, 0755)
	_ = os.MkdirAll(destDir, 0755)

	srcMP3 := filepath.Join(srcDir, "01. Song.mp3")
	srcLRC := filepath.Join(srcDir, "01. Song.lrc")
	_ = os.WriteFile(srcMP3, []byte("audio data 1"), 0644)
	_ = os.WriteFile(srcLRC, []byte("[00:01.00]lyrics text"), 0644)

	destMP3 := filepath.Join(destDir, "01. Song.mp3")
	destLRC := filepath.Join(destDir, "01. Song.lrc")

	actualDest, err := MoveTrackWithCompanions(srcMP3, destMP3, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if actualDest != destMP3 {
		t.Errorf("expected %s, got %s", destMP3, actualDest)
	}
	if !fileExists(destMP3) || !fileExists(destLRC) {
		t.Errorf("expected both audio and lyrics moved to dest")
	}
	if fileExists(srcMP3) || fileExists(srcLRC) {
		t.Errorf("expected source audio and lyrics removed")
	}
}

func TestMoveTrackWithCompanions_CollisionIdentical(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "organizer_collision_ident_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	srcDir := filepath.Join(tempDir, "src")
	destDir := filepath.Join(tempDir, "dest")
	_ = os.MkdirAll(srcDir, 0755)
	_ = os.MkdirAll(destDir, 0755)

	srcMP3 := filepath.Join(srcDir, "01. Song.mp3")
	destMP3 := filepath.Join(destDir, "01. Song.mp3")
	_ = os.WriteFile(srcMP3, []byte("exact identical content"), 0644)
	_ = os.WriteFile(destMP3, []byte("exact identical content"), 0644)

	actualDest, err := MoveTrackWithCompanions(srcMP3, destMP3, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if actualDest != destMP3 {
		t.Errorf("expected deduplication to %s, got %s", destMP3, actualDest)
	}
	if fileExists(srcMP3) {
		t.Errorf("expected source to be deleted on deduplication")
	}
	if !fileExists(destMP3) {
		t.Errorf("expected dest to remain")
	}
}

func TestMoveTrackWithCompanions_CollisionDifferentContent(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "organizer_collision_diff_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	srcDir := filepath.Join(tempDir, "src")
	destDir := filepath.Join(tempDir, "dest")
	_ = os.MkdirAll(srcDir, 0755)
	_ = os.MkdirAll(destDir, 0755)

	srcMP3 := filepath.Join(srcDir, "01. Song.mp3")
	srcLRC := filepath.Join(srcDir, "01. Song.lrc")
	destMP3 := filepath.Join(destDir, "01. Song.mp3")
	destLRC := filepath.Join(destDir, "01. Song.lrc")

	_ = os.WriteFile(srcMP3, []byte("audio from source"), 0644)
	_ = os.WriteFile(srcLRC, []byte("lyrics from source"), 0644)
	_ = os.WriteFile(destMP3, []byte("different audio in dest"), 0644)
	_ = os.WriteFile(destLRC, []byte("different lyrics in dest"), 0644)

	actualDest, err := MoveTrackWithCompanions(srcMP3, destMP3, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedDestMP3 := filepath.Join(destDir, "01. Song (1).mp3")
	expectedDestLRC := filepath.Join(destDir, "01. Song (1).lrc")

	if actualDest != expectedDestMP3 {
		t.Errorf("expected suffixed dest %s, got %s", expectedDestMP3, actualDest)
	}
	if !fileExists(expectedDestMP3) {
		t.Errorf("expected %s to exist", expectedDestMP3)
	}
	if !fileExists(expectedDestLRC) {
		t.Errorf("expected companion %s to exist", expectedDestLRC)
	}
	if data, _ := os.ReadFile(destMP3); string(data) != "different audio in dest" {
		t.Errorf("original dest file was overwritten!")
	}
}

func TestMoveTrackWithCompanions_ImageAndLyrics(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "organizer_comp_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	srcDir := filepath.Join(tempDir, "src")
	destDir := filepath.Join(tempDir, "dest")
	_ = os.MkdirAll(srcDir, 0755)
	_ = os.MkdirAll(destDir, 0755)

	srcMP3 := filepath.Join(srcDir, "01. Song.mp3")
	srcLRC := filepath.Join(srcDir, "01. Song.lrc")
	srcJPG := filepath.Join(srcDir, "01. Song.jpg")

	_ = os.WriteFile(srcMP3, []byte("mp3-data"), 0644)
	_ = os.WriteFile(srcLRC, []byte("[00:01.00]lyrics"), 0644)
	_ = os.WriteFile(srcJPG, []byte("companion-jpg"), 0644)

	destMP3 := filepath.Join(destDir, "01. Renamed Song.mp3")
	actualDest, err := MoveTrackWithCompanions(srcMP3, destMP3, false)
	if err != nil {
		t.Fatalf("MoveTrackWithCompanions failed: %v", err)
	}
	if actualDest != destMP3 {
		t.Errorf("expected actualDest = %s, got %s", destMP3, actualDest)
	}

	if !fileExists(destMP3) {
		t.Errorf("expected %s to exist", destMP3)
	}
	destLRC := filepath.Join(destDir, "01. Renamed Song.lrc")
	if !fileExists(destLRC) {
		t.Errorf("expected %s to exist", destLRC)
	}
	destJPG := filepath.Join(destDir, "01. Renamed Song.jpg")
	if !fileExists(destJPG) {
		t.Errorf("expected %s to exist", destJPG)
	}

	if fileExists(srcMP3) || fileExists(srcLRC) || fileExists(srcJPG) {
		t.Errorf("source files still exist after move")
	}
}
