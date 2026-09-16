package organizer

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
)

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

	// 1. Move to non-existent dest
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
	// Original files in dest should NOT be overwritten
	if data, _ := os.ReadFile(destMP3); string(data) != "different audio in dest" {
		t.Errorf("original dest file was overwritten!")
	}
}

func TestMoveAlbum_MultipleReleasesSeparation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "organizer_multi_rel_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	musicDir := filepath.Join(tempDir, "music")
	tmpDir := filepath.Join(tempDir, "tmp")
	_ = os.MkdirAll(musicDir, 0755)
	_ = os.MkdirAll(tmpDir, 0755)

	musicSb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatalf("failed to create music sandbox: %v", err)
	}
	tmpSb, err := sandbox.NewPathSandbox(tmpDir)
	if err != nil {
		t.Fatalf("failed to create tmp sandbox: %v", err)
	}

	svc := NewService(musicSb, tmpSb)

	// Release 1: "The Dark Side of the Moon" by "Pink Floyd"
	job1Dir := filepath.Join(tmpDir, "job-1", "converted")
	_ = os.MkdirAll(job1Dir, 0755)
	job1Track := filepath.Join(job1Dir, "1.mp3")
	_ = os.WriteFile(job1Track, []byte("audio-content-track-1"), 0644)
	job1Cover := filepath.Join(tmpDir, "job-1", "cover.jpg")
	_ = os.WriteFile(job1Cover, []byte("cover-image-1"), 0644)

	// Release 2: "Abbey Road" by "The Beatles"
	job2Dir := filepath.Join(tmpDir, "job-2", "converted")
	_ = os.MkdirAll(job2Dir, 0755)
	job2Track := filepath.Join(job2Dir, "1.mp3")
	_ = os.WriteFile(job2Track, []byte("audio-content-track-2"), 0644)
	job2Cover := filepath.Join(tmpDir, "job-2", "cover.jpg")
	_ = os.WriteFile(job2Cover, []byte("cover-image-2"), 0644)

	tracks := []id3.TrackTags{
		{
			Path:        "tmp_downloads/job-1/converted/1.mp3",
			Title:       "Speak to Me",
			Artist:      "Pink Floyd",
			Album:       "The Dark Side of the Moon",
			AlbumArtist: "Pink Floyd",
			TrackNumber: 1,
			TrackTotal:  10,
			Year:        1973,
		},
		{
			Path:        "tmp_downloads/job-2/converted/1.mp3",
			Title:       "Come Together",
			Artist:      "The Beatles",
			Album:       "Abbey Road",
			AlbumArtist: "The Beatles",
			TrackNumber: 1,
			TrackTotal:  17,
			Year:        1969,
		},
	}

	// Move all tracks in one batch operation
	res, err := svc.MoveAlbum("tmp_downloads", "", "", tracks, "")
	if err != nil {
		t.Fatalf("MoveAlbum failed: %v", err)
	}

	if len(res.Tracks) != 2 {
		t.Fatalf("expected 2 updated tracks, got %d", len(res.Tracks))
	}

	// Verify track 1 landed in its own folder with correct tags
	dest1 := filepath.Join(musicDir, "The Dark Side of the Moon", "01. Speak to Me.mp3")
	if !fileExists(dest1) {
		t.Errorf("expected track 1 at %s", dest1)
	}
	destCover1 := filepath.Join(musicDir, "The Dark Side of the Moon", "cover.jpg")
	if !fileExists(destCover1) {
		t.Errorf("expected cover 1 at %s", destCover1)
	} else if data, _ := os.ReadFile(destCover1); string(data) != "cover-image-1" {
		t.Errorf("expected cover-image-1 content, got %s", string(data))
	}

	// Verify track 2 landed in its own folder with correct tags
	dest2 := filepath.Join(musicDir, "Abbey Road", "01. Come Together.mp3")
	if !fileExists(dest2) {
		t.Errorf("expected track 2 at %s", dest2)
	}
	destCover2 := filepath.Join(musicDir, "Abbey Road", "cover.jpg")
	if !fileExists(destCover2) {
		t.Errorf("expected cover 2 at %s", destCover2)
	} else if data, _ := os.ReadFile(destCover2); string(data) != "cover-image-2" {
		t.Errorf("expected cover-image-2 content, got %s", string(data))
	}

	// Verify individual track tags preserved
	tags1, err := id3.ReadTags(dest1, "The Dark Side of the Moon/01. Speak to Me.mp3")
	if err != nil {
		t.Fatalf("failed to read tags from dest1: %v", err)
	}
	if tags1.Album != "The Dark Side of the Moon" {
		t.Errorf("expected Album 'The Dark Side of the Moon', got '%s'", tags1.Album)
	}
	if tags1.AlbumArtist != "Pink Floyd" {
		t.Errorf("expected AlbumArtist 'Pink Floyd', got '%s'", tags1.AlbumArtist)
	}

	tags2, err := id3.ReadTags(dest2, "Abbey Road/01. Come Together.mp3")
	if err != nil {
		t.Fatalf("failed to read tags from dest2: %v", err)
	}
	if tags2.Album != "Abbey Road" {
		t.Errorf("expected Album 'Abbey Road', got '%s'", tags2.Album)
	}
	if tags2.AlbumArtist != "The Beatles" {
		t.Errorf("expected AlbumArtist 'The Beatles', got '%s'", tags2.AlbumArtist)
	}
}

func TestMoveAlbum_SingleTrackAlbumEqualsTitle(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "organizer_single_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	musicDir := filepath.Join(tempDir, "music")
	tmpDir := filepath.Join(tempDir, "tmp")
	_ = os.MkdirAll(musicDir, 0755)
	_ = os.MkdirAll(tmpDir, 0755)

	musicSb, _ := sandbox.NewPathSandbox(musicDir)
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	svc := NewService(musicSb, tmpSb)

	jobDir := filepath.Join(tmpDir, "job-single", "converted")
	_ = os.MkdirAll(jobDir, 0755)
	jobTrack := filepath.Join(jobDir, "1.mp3")
	_ = os.WriteFile(jobTrack, []byte("audio-single-track"), 0644)

	tracks := []id3.TrackTags{
		{
			Path:        "tmp_downloads/job-single/converted/1.mp3",
			Title:       "Blinding Lights",
			Artist:      "The Weeknd",
			Album:       "", // Empty album -> should become "Blinding Lights"
			AlbumArtist: "The Weeknd",
			TrackNumber: 1,
		},
	}

	res, err := svc.MoveAlbum("tmp_downloads", "", "", tracks, "")
	if err != nil {
		t.Fatalf("MoveAlbum failed: %v", err)
	}

	expectedDir := "Blinding Lights"
	if res.DestinationDir != expectedDir {
		t.Errorf("expected DestinationDir '%s', got '%s'", expectedDir, res.DestinationDir)
	}

	destTrack := filepath.Join(musicDir, "Blinding Lights", "01. Blinding Lights.mp3")
	if !fileExists(destTrack) {
		t.Fatalf("expected track at %s", destTrack)
	}

	tags, err := id3.ReadTags(destTrack, "Blinding Lights/01. Blinding Lights.mp3")
	if err != nil {
		t.Fatalf("failed to read tags: %v", err)
	}
	if tags.Album != "Blinding Lights" {
		t.Errorf("expected album tag to be set to track title 'Blinding Lights', got '%s'", tags.Album)
	}
}

func TestMoveAlbum_Base64Cover(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "organizer_base64_cov_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	musicDir := filepath.Join(tempDir, "music")
	tmpDir := filepath.Join(tempDir, "tmp")
	_ = os.MkdirAll(musicDir, 0755)
	_ = os.MkdirAll(tmpDir, 0755)

	musicSb, _ := sandbox.NewPathSandbox(musicDir)
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	svc := NewService(musicSb, tmpSb)

	jobDir := filepath.Join(tmpDir, "job-b64", "converted")
	_ = os.MkdirAll(jobDir, 0755)
	jobTrack := filepath.Join(jobDir, "1.mp3")
	_ = os.WriteFile(jobTrack, []byte("audio-b64-track"), 0644)

	tracks := []id3.TrackTags{
		{
			Path:        "tmp_downloads/job-b64/converted/1.mp3",
			Title:       "Custom Song",
			Artist:      "Artist B",
			Album:       "Custom Album",
			AlbumArtist: "Artist B",
			TrackNumber: 1,
		},
	}

	b64Cover := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("decoded-jpeg-bytes"))

	res, err := svc.MoveAlbum("tmp_downloads", "Custom Album", "Artist B", tracks, b64Cover)
	if err != nil {
		t.Fatalf("MoveAlbum failed: %v", err)
	}
	expectedDir := "Custom Album"
	if res.DestinationDir != expectedDir {
		t.Errorf("expected DestinationDir '%s', got '%s'", expectedDir, res.DestinationDir)
	}

	coverPath := filepath.Join(musicDir, "Custom Album", "cover.jpg")
	if !fileExists(coverPath) {
		t.Fatalf("expected cover.jpg to exist at %s", coverPath)
	}
	data, _ := os.ReadFile(coverPath)
	if string(data) != "decoded-jpeg-bytes" {
		t.Errorf("expected 'decoded-jpeg-bytes', got '%s'", string(data))
	}
}
