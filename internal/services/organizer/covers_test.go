package organizer

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
)

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
	expectedDir := "Custom Album - Artist B"
	if res.DestinationDir != expectedDir {
		t.Errorf("expected DestinationDir '%s', got '%s'", expectedDir, res.DestinationDir)
	}

	coverPath := filepath.Join(musicDir, "Custom Album - Artist B", "cover.jpg")
	if !fileExists(coverPath) {
		t.Fatalf("expected cover.jpg to exist at %s", coverPath)
	}
	data, _ := os.ReadFile(coverPath)
	if string(data) != "decoded-jpeg-bytes" {
		t.Errorf("expected 'decoded-jpeg-bytes', got '%s'", string(data))
	}
}

func TestMoveAlbum_Single_TrackNamedCover(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "organizer_single_cov_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	musicDir := filepath.Join(tempDir, "music")
	tmpDir := filepath.Join(tempDir, "tmp")
	_ = os.MkdirAll(musicDir, 0755)
	_ = os.MkdirAll(tmpDir, 0755)

	musicSb, _ := sandbox.NewPathSandbox(musicDir)
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	svc := NewService(musicSb, tmpSb)

	jobDir := filepath.Join(tmpDir, "job-single-cov", "converted")
	_ = os.MkdirAll(jobDir, 0755)
	jobTrack := filepath.Join(jobDir, "1.mp3")
	_ = os.WriteFile(jobTrack, []byte("fake-mp3-data"), 0644)
	jobCover := filepath.Join(tmpDir, "job-single-cov", "cover.jpg")
	_ = os.WriteFile(jobCover, []byte("my-single-cover-art"), 0644)

	tracks := []id3.TrackTags{
		{
			Path:        "tmp_downloads/job-single-cov/converted/1.mp3",
			Title:       "1.Kla$",
			Artist:      "1.Kla$",
			Album:       "",
			AlbumArtist: "1.Kla$",
			TrackNumber: 1,
		},
	}

	res, err := svc.MoveAlbum("tmp_downloads/job-single-cov", "", "", tracks, "")
	if err != nil {
		t.Fatalf("MoveAlbum failed: %v", err)
	}

	targetFolder := filepath.Join(musicDir, res.DestinationDir)
	destTrack := filepath.Join(targetFolder, "01. 1.Kla$.mp3")
	if !fileExists(destTrack) {
		t.Fatalf("expected track at %s", destTrack)
	}

	// 1. Проверяем наличие стандартного cover.jpg
	destCover := filepath.Join(targetFolder, "cover.jpg")
	if !fileExists(destCover) {
		t.Errorf("expected cover.jpg to exist at %s", destCover)
	}

	// 2. Проверяем наличие обложки по имени трека (01. 1.Kla$.jpg)
	destTrackCover := filepath.Join(targetFolder, "01. 1.Kla$.jpg")
	if !fileExists(destTrackCover) {
		t.Errorf("expected track-named cover '01. 1.Kla$.jpg' to exist at %s", destTrackCover)
	} else if data, _ := os.ReadFile(destTrackCover); string(data) != "my-single-cover-art" {
		t.Errorf("expected 'my-single-cover-art', got %s", string(data))
	}
}

func TestMoveAlbum_Single_EmbeddedCoverExtraction(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "organizer_embed_cov_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	musicDir := filepath.Join(tempDir, "music")
	tmpDir := filepath.Join(tempDir, "tmp")
	_ = os.MkdirAll(musicDir, 0755)
	_ = os.MkdirAll(tmpDir, 0755)

	musicSb, _ := sandbox.NewPathSandbox(musicDir)
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	svc := NewService(musicSb, tmpSb)

	jobDir := filepath.Join(tmpDir, "job-embed", "converted")
	_ = os.MkdirAll(jobDir, 0755)
	jobTrack := filepath.Join(jobDir, "1.mp3")
	_ = os.WriteFile(jobTrack, make([]byte, 5000), 0644)

	// Записываем теги с встроенной обложкой APIC
	initialTags := id3.TrackTags{
		Title:       "Embedded Track",
		Artist:      "Artist E",
		Album:       "",
		TrackNumber: 1,
	}
	var artwork bytes.Buffer
	if err := jpeg.Encode(&artwork, image.NewRGBA(image.Rect(0, 0, 16, 16)), nil); err != nil {
		t.Fatal(err)
	}
	cover := &id3.CoverInfo{MimeType: "image/jpeg", Data: artwork.Bytes()}
	_ = id3.WriteTags(jobTrack, initialTags, cover)

	tracks := []id3.TrackTags{
		{
			Path:        "tmp_downloads/job-embed/converted/1.mp3",
			Title:       "Embedded Track",
			Artist:      "Artist E",
			Album:       "",
			AlbumArtist: "Artist E",
			TrackNumber: 1,
		},
	}

	res, err := svc.MoveAlbum("tmp_downloads/job-embed", "", "", tracks, "")
	if err != nil {
		t.Fatalf("MoveAlbum failed: %v", err)
	}

	targetFolder := filepath.Join(musicDir, res.DestinationDir)
	destCover := filepath.Join(targetFolder, "cover.jpg")
	destTrackCover := filepath.Join(targetFolder, "01. Embedded Track.jpg")

	if !fileExists(destCover) {
		t.Errorf("expected extracted cover.jpg to exist at %s", destCover)
	}
	if !fileExists(destTrackCover) {
		t.Errorf("expected extracted 01. Embedded Track.jpg to exist at %s", destTrackCover)
	}
}
