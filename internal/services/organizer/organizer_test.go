package organizer

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
)

func TestSanitizeComponent(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		fallback  string
		maxLen    int
		expected  string
	}{
		{
			name:     "clean normal name",
			input:    "Greatest Hits",
			fallback: "Unknown",
			maxLen:   180,
			expected: "Greatest Hits",
		},
		{
			name:     "replace forbidden characters",
			input:    `AC/DC: What? * "Special" <Live> | Track`,
			fallback: "Unknown",
			maxLen:   180,
			expected: "AC_DC_ What_ _ _Special_ _Live_ _ Track",
		},
		{
			name:     "replace backslash and null byte with ampersand",
			input:    "Artist A\\Artist B\x00Artist C",
			fallback: "Unknown",
			maxLen:   180,
			expected: "Artist A & Artist B & Artist C",
		},
		{
			name:     "trim trailing dots and spaces",
			input:    "  Album Name...   ",
			fallback: "Unknown",
			maxLen:   180,
			expected: "Album Name",
		},
		{
			name:     "empty input returns fallback",
			input:    "   ",
			fallback: "Unknown Album",
			maxLen:   180,
			expected: "Unknown Album",
		},
		{
			name:     "truncate long title safely without breaking unicode",
			input:    strings.Repeat("ПриветМир", 30),
			fallback: "Unknown",
			maxLen:   20,
			expected: "ПриветМирПриветМирПр",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeComponent(tt.input, tt.fallback, tt.maxLen)
			if got != tt.expected {
				t.Errorf("SanitizeComponent(%q) = %q; want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestComputeTargetPath(t *testing.T) {
	tests := []struct {
		name         string
		artist       string
		album        string
		trackNum     int
		title        string
		origFilename string
		wantDir      string
		wantRelPath  string
	}{
		{
			name:         "normal album and track",
			artist:       "The Beatles",
			album:        "Abbey Road",
			trackNum:     1,
			title:        "Come Together",
			origFilename: "1.mp3",
			wantDir:      "Abbey Road - The Beatles",
			wantRelPath:  filepath.Join("Abbey Road - The Beatles", "01. Come Together.mp3"),
		},
		{
			name:         "single with empty album",
			artist:       "The Weeknd",
			album:        "",
			trackNum:     1,
			title:        "Blinding Lights",
			origFilename: "track.mp3",
			wantDir:      "Blinding Lights - The Weeknd",
			wantRelPath:  filepath.Join("Blinding Lights - The Weeknd", "01. Blinding Lights.mp3"),
		},
		{
			name:         "single with album named Single",
			artist:       "The Weeknd",
			album:        "Single",
			trackNum:     1,
			title:        "Starboy",
			origFilename: "01.mp3",
			wantDir:      "Starboy - The Weeknd",
			wantRelPath:  filepath.Join("Starboy - The Weeknd", "01. Starboy.mp3"),
		},
		{
			name:         "single with album named Singles (case-insensitive)",
			artist:       "Phil Collins",
			album:        "singles",
			trackNum:     0, // non-positive should default to 1
			title:        "01. 01. In the Air Tonight",
			origFilename: "track.mp3",
			wantDir:      "In the Air Tonight - Phil Collins",
			wantRelPath:  filepath.Join("In the Air Tonight - Phil Collins", "01. In the Air Tonight.mp3"),
		},
		{
			name:         "dangerous characters in album and title",
			artist:       "AC/DC",
			album:        "AC/DC Live?",
			trackNum:     5,
			title:        "Hells Bells: Special Edition",
			origFilename: "5.mp3",
			wantDir:      "AC_DC Live_ - AC_DC",
			wantRelPath:  filepath.Join("AC_DC Live_ - AC_DC", "05. Hells Bells_ Special Edition.mp3"),
		},
		{
			name:         "track title starting with numbers (21 Guns)",
			artist:       "Green Day",
			album:        "21st Century Breakdown",
			trackNum:     21,
			title:        "21 Guns",
			origFilename: "21. 21 Guns.mp3",
			wantDir:      "21st Century Breakdown - Green Day",
			wantRelPath:  filepath.Join("21st Century Breakdown - Green Day", "21. 21 Guns.mp3"),
		},
		{
			name:         "track title starting with year (1979)",
			artist:       "The Smashing Pumpkins",
			album:        "Mellon Collie",
			trackNum:     5,
			title:        "1979",
			origFilename: "05. 1979.mp3",
			wantDir:      "Mellon Collie - The Smashing Pumpkins",
			wantRelPath:  filepath.Join("Mellon Collie - The Smashing Pumpkins", "05. 1979.mp3"),
		},
		{
			name:         "track title with number prefix and digits title (01. 505)",
			artist:       "Arctic Monkeys",
			album:        "Favourite Worst Nightmare",
			trackNum:     12,
			title:        "12. 505",
			origFilename: "12. 505.mp3",
			wantDir:      "Favourite Worst Nightmare - Arctic Monkeys",
			wantRelPath:  filepath.Join("Favourite Worst Nightmare - Arctic Monkeys", "12. 505.mp3"),
		},
		{
			name:         "artist and track title starting with digit and dot (1.Kla$)",
			artist:       "1.Kla$",
			album:        "1.Kla$",
			trackNum:     1,
			title:        "01. 1.Kla$",
			origFilename: "01. 1.Kla$.mp3",
			wantDir:      "1.Kla$ - 1.Kla$",
			wantRelPath:  filepath.Join("1.Kla$ - 1.Kla$", "01. 1.Kla$.mp3"),
		},
		{
			name:         "single with 1.Kla$ and empty album",
			artist:       "1.Kla$",
			album:        "",
			trackNum:     1,
			title:        "1.Kla$",
			origFilename: "1.Kla$.mp3",
			wantDir:      "1.Kla$ - 1.Kla$",
			wantRelPath:  filepath.Join("1.Kla$ - 1.Kla$", "01. 1.Kla$.mp3"),
		},
		{
			name:         "artist and track title starting with 1.Царь",
			artist:       "1.Царь",
			album:        "Мой стиль",
			trackNum:     1,
			title:        "01. 1.Царь",
			origFilename: "01. 1.Царь.mp3",
			wantDir:      "Мой стиль - 1.Царь",
			wantRelPath:  filepath.Join("Мой стиль - 1.Царь", "01. 1.Царь.mp3"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			albumDir, trackRel := ComputeTargetPath(tt.artist, tt.album, tt.trackNum, tt.title, tt.origFilename)
			if albumDir != tt.wantDir {
				t.Errorf("ComputeTargetPath albumDir = %q; want %q", albumDir, tt.wantDir)
			}
			if trackRel != tt.wantRelPath {
				t.Errorf("ComputeTargetPath trackRel = %q; want %q", trackRel, tt.wantRelPath)
			}
		})
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
	dest1 := filepath.Join(musicDir, "The Dark Side of the Moon - Pink Floyd", "01. Speak to Me.mp3")
	if !fileExists(dest1) {
		t.Errorf("expected track 1 at %s", dest1)
	}
	destCover1 := filepath.Join(musicDir, "The Dark Side of the Moon - Pink Floyd", "cover.jpg")
	if !fileExists(destCover1) {
		t.Errorf("expected cover 1 at %s", destCover1)
	} else if data, _ := os.ReadFile(destCover1); string(data) != "cover-image-1" {
		t.Errorf("expected cover-image-1 content, got %s", string(data))
	}

	// Verify track 2 landed in its own folder with correct tags
	dest2 := filepath.Join(musicDir, "Abbey Road - The Beatles", "01. Come Together.mp3")
	if !fileExists(dest2) {
		t.Errorf("expected track 2 at %s", dest2)
	}
	destCover2 := filepath.Join(musicDir, "Abbey Road - The Beatles", "cover.jpg")
	if !fileExists(destCover2) {
		t.Errorf("expected cover 2 at %s", destCover2)
	} else if data, _ := os.ReadFile(destCover2); string(data) != "cover-image-2" {
		t.Errorf("expected cover-image-2 content, got %s", string(data))
	}

	// Verify individual track tags preserved
	tags1, err := id3.ReadTags(dest1, "The Dark Side of the Moon - Pink Floyd/01. Speak to Me.mp3")
	if err != nil {
		t.Fatalf("failed to read tags from dest1: %v", err)
	}
	if tags1.Album != "The Dark Side of the Moon" {
		t.Errorf("expected Album 'The Dark Side of the Moon', got '%s'", tags1.Album)
	}
	if tags1.AlbumArtist != "Pink Floyd" {
		t.Errorf("expected AlbumArtist 'Pink Floyd', got '%s'", tags1.AlbumArtist)
	}

	tags2, err := id3.ReadTags(dest2, "Abbey Road - The Beatles/01. Come Together.mp3")
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

	expectedDir := "Blinding Lights - The Weeknd"
	if res.DestinationDir != expectedDir {
		t.Errorf("expected DestinationDir '%s', got '%s'", expectedDir, res.DestinationDir)
	}

	destTrack := filepath.Join(musicDir, "Blinding Lights - The Weeknd", "01. Blinding Lights.mp3")
	if !fileExists(destTrack) {
		t.Fatalf("expected track at %s", destTrack)
	}

	tags, err := id3.ReadTags(destTrack, "Blinding Lights - The Weeknd/01. Blinding Lights.mp3")
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

func TestSaveBatch(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "organizer_save_batch_*")
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

	jobDir := filepath.Join(tmpDir, "job-batch", "converted")
	_ = os.MkdirAll(jobDir, 0755)
	jobTrack := filepath.Join(jobDir, "1.mp3")
	_ = os.WriteFile(jobTrack, []byte("audio-batch-track"), 0644)

	year := 2024
	req := BatchSaveRequest{
		SourceDir: "tmp_downloads/job-batch",
		Album: AlbumMeta{
			Album:       "Release 2024",
			AlbumArtist: "Famous Artist",
			Year:        &year,
			Genre:       "Synthwave",
		},
		Tracks: []TrackTags{
			{
				Path:        "tmp_downloads/job-batch/converted/1.mp3",
				Title:       "Nightcall",
				TrackNumber: 1,
			},
		},
	}

	res, err := svc.SaveBatch(req)
	if err != nil {
		t.Fatalf("SaveBatch failed: %v", err)
	}

	if len(res.Tracks) != 1 {
		t.Fatalf("expected 1 track in result, got %d", len(res.Tracks))
	}

	tr := res.Tracks[0]
	if tr.Album != "Release 2024" {
		t.Errorf("expected inherited Album 'Release 2024', got %q", tr.Album)
	}
	if tr.AlbumArtist != "Famous Artist" {
		t.Errorf("expected inherited AlbumArtist 'Famous Artist', got %q", tr.AlbumArtist)
	}
	if tr.Year != 2024 {
		t.Errorf("expected inherited Year 2024, got %d", tr.Year)
	}
	if tr.Genre != "Synthwave" {
		t.Errorf("expected inherited Genre 'Synthwave', got %q", tr.Genre)
	}
}

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
	cover := &id3.CoverInfo{
		MimeType: "image/jpeg",
		Data:     []byte("embedded-cover-data"),
	}
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

	// Verify MP3, LRC, and JPG were moved
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

	// Verify source files removed
	if fileExists(srcMP3) || fileExists(srcLRC) || fileExists(srcJPG) {
		t.Errorf("source files still exist after move")
	}
}


