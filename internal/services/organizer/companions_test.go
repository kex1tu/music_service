package organizer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestDedupPreservesDifferentLyrics(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp3")
	dst := filepath.Join(dir, "dst.mp3")
	for _, p := range []string{src, dst} {
		if err := os.WriteFile(p, []byte("identical-audio"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "src.lrc"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dst.lrc"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	actual, err := MoveTrackWithCompanions(src, dst, false)
	if err != nil {
		t.Fatal(err)
	}
	if actual == dst {
		t.Fatal("conflicting lyrics deduplicated")
	}
	b, err := os.ReadFile(strings.TrimSuffix(actual, ".mp3") + ".lrc")
	if err != nil || string(b) != "new" {
		t.Fatalf("new lyrics lost: %q %v", b, err)
	}
	b, err = os.ReadFile(filepath.Join(dir, "dst.lrc"))
	if err != nil || string(b) != "old" {
		t.Fatalf("old lyrics changed: %q %v", b, err)
	}
}

func TestCompanionFailurePreservesAudio(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp3")
	dst := filepath.Join(dir, "dst.mp3")
	if err := os.WriteFile(src, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src.lrc"), []byte("lyrics"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dst.lrc"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := MoveTrackWithCompanions(src, dst, false); err == nil {
		t.Fatal("expected companion failure")
	}
	b, err := os.ReadFile(src)
	if err != nil || string(b) != "original" {
		t.Fatalf("original lost: %q %v", b, err)
	}
	b, err = os.ReadFile(filepath.Join(dir, "src.lrc"))
	if err != nil || string(b) != "lyrics" {
		t.Fatalf("lyrics lost: %q %v", b, err)
	}
}

func TestMoveTrackWithCompanions_LongCyrillicCollisionRecalculatesBudget(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	destDir := filepath.Join(tempDir, "dest")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 120 Cyrillic characters = 240 bytes. With prefix "01. " (4 bytes) = 244 bytes base, + ".mp3" (4 bytes) = 248 bytes.
	longTitle := strings.Repeat("ДлинныйТрек", 22) // 242 bytes
	longFilename := BuildTrackFilename(1, longTitle, "Track", MaxComponentBytes)
	destMP3 := filepath.Join(destDir, longFilename)

	// Prepare track 1
	src1MP3 := filepath.Join(srcDir, "track1.mp3")
	src1LRC := filepath.Join(srcDir, "track1.lrc")
	src1JPG := filepath.Join(srcDir, "track1.jpeg")
	if err := os.WriteFile(src1MP3, []byte("audio-content-track-1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src1LRC, []byte("[00:01.00] track 1 lyrics"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src1JPG, []byte("jpeg-image-track-1"), 0644); err != nil {
		t.Fatal(err)
	}

	// Move track 1 to destMP3
	dest1, err := MoveTrackWithCompanions(src1MP3, destMP3, false)
	if err != nil {
		t.Fatalf("failed to move track 1: %v", err)
	}
	if len(filepath.Base(dest1)) > MaxComponentBytes {
		t.Errorf("dest1 len = %d > %d", len(filepath.Base(dest1)), MaxComponentBytes)
	}
	if !fileExists(dest1) {
		t.Fatalf("dest1 does not exist: %s", dest1)
	}
	dest1LRC := strings.TrimSuffix(dest1, ".mp3") + ".lrc"
	dest1JPG := strings.TrimSuffix(dest1, ".mp3") + ".jpeg"
	if !fileExists(dest1LRC) || !fileExists(dest1JPG) {
		t.Fatalf("expected track 1 companions to exist at %s and %s", dest1LRC, dest1JPG)
	}

	// Prepare track 2 (different content, same destination path to force collision)
	src2MP3 := filepath.Join(srcDir, "track2.mp3")
	src2LRC := filepath.Join(srcDir, "track2.lrc")
	src2JPG := filepath.Join(srcDir, "track2.jpeg")
	if err := os.WriteFile(src2MP3, []byte("audio-content-track-2-different"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src2LRC, []byte("[00:01.00] track 2 lyrics"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src2JPG, []byte("jpeg-image-track-2"), 0644); err != nil {
		t.Fatal(err)
	}

	// Move track 2 to same destMP3
	dest2, err := MoveTrackWithCompanions(src2MP3, destMP3, false)
	if err != nil {
		t.Fatalf("failed to move track 2: %v", err)
	}
	if dest2 == dest1 {
		t.Fatalf("track 2 should not overwrite track 1, got same path: %s", dest2)
	}
	if len(filepath.Base(dest2)) > MaxComponentBytes {
		t.Errorf("dest2 len = %d > %d", len(filepath.Base(dest2)), MaxComponentBytes)
	}
	if !utf8.ValidString(filepath.Base(dest2)) {
		t.Errorf("dest2 basename is not valid UTF-8: %s", filepath.Base(dest2))
	}
	if !strings.Contains(dest2, " (1).mp3") {
		t.Errorf("dest2 should contain suffix ' (1).mp3', got %s", dest2)
	}

	// Verify track 2 companions exist with matching suffix
	dest2LRC := strings.TrimSuffix(dest2, ".mp3") + ".lrc"
	dest2JPG := strings.TrimSuffix(dest2, ".mp3") + ".jpeg"
	if !fileExists(dest2LRC) {
		t.Fatalf("expected track 2 companion LRC to exist: %s", dest2LRC)
	}
	if !fileExists(dest2JPG) {
		t.Fatalf("expected track 2 companion JPEG to exist: %s", dest2JPG)
	}
	if len(filepath.Base(dest2JPG)) > MaxComponentBytes {
		t.Errorf("dest2 companion JPEG len = %d > %d", len(filepath.Base(dest2JPG)), MaxComponentBytes)
	}

	// Verify content integrity: track 1 content was not overwritten
	data1, _ := os.ReadFile(dest1)
	if string(data1) != "audio-content-track-1" {
		t.Errorf("track 1 audio content corrupted: %s", string(data1))
	}
	data2, _ := os.ReadFile(dest2)
	if string(data2) != "audio-content-track-2-different" {
		t.Errorf("track 2 audio content corrupted: %s", string(data2))
	}
	lrcData1, _ := os.ReadFile(dest1LRC)
	if string(lrcData1) != "[00:01.00] track 1 lyrics" {
		t.Errorf("track 1 lyrics corrupted: %s", string(lrcData1))
	}
	lrcData2, _ := os.ReadFile(dest2LRC)
	if string(lrcData2) != "[00:01.00] track 2 lyrics" {
		t.Errorf("track 2 lyrics corrupted: %s", string(lrcData2))
	}

	// Track 3 collision: third track with different content
	src3MP3 := filepath.Join(srcDir, "track3.mp3")
	if err := os.WriteFile(src3MP3, []byte("audio-content-track-3"), 0644); err != nil {
		t.Fatal(err)
	}
	dest3, err := MoveTrackWithCompanions(src3MP3, destMP3, false)
	if err != nil {
		t.Fatalf("failed to move track 3: %v", err)
	}
	if !strings.Contains(dest3, " (2).mp3") {
		t.Errorf("dest3 should contain suffix ' (2).mp3', got %s", dest3)
	}
	if len(filepath.Base(dest3)) > MaxComponentBytes {
		t.Errorf("dest3 len = %d > %d", len(filepath.Base(dest3)), MaxComponentBytes)
	}
}
