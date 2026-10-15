package organizer

import (
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
)

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

func TestSaveBatch_SingleTrackMetadataPropagationToRelease(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "organizer_single_prop_*")
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

	// Single track download where release-level album and artist are empty/omitted
	req := BatchSaveRequest{
		SourceDir: "tmp_downloads/job-single",
		Album: AlbumMeta{
			Album:       "", // Empty or single
			AlbumArtist: "",
			Year:        nil,
			Genre:       "",
		},
		Tracks: []TrackTags{
			{
				Path:        "tmp_downloads/job-single/converted/1.mp3",
				Title:       "Hit Song",
				Artist:      "Main Artist",
				TrackNumber: 1,
				Year:        2023,
				Genre:       "Hip-Hop",
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
	if tr.Album != "Hit Song" {
		t.Errorf("expected Album 'Hit Song', got %q", tr.Album)
	}
	if tr.AlbumArtist != "Main Artist" {
		t.Errorf("expected AlbumArtist 'Main Artist', got %q", tr.AlbumArtist)
	}
	if tr.Year != 2023 {
		t.Errorf("expected Year 2023, got %d", tr.Year)
	}
	if tr.Genre != "Hip-Hop" {
		t.Errorf("expected Genre 'Hip-Hop', got %q", tr.Genre)
	}

	expectedDir := "Hit Song - Main Artist"
	if res.DestinationDir != expectedDir {
		t.Errorf("expected DestinationDir %q, got %q", expectedDir, res.DestinationDir)
	}
}

func TestOrganizer_EmptyDirCleanup(t *testing.T) {
	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	_ = os.MkdirAll(musicDir, 0755)

	// 1. Full empty hierarchy cleanup
	nestedDir := filepath.Join(musicDir, "EmptyArtist", "EmptyAlbum", "SubDir")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatal(err)
	}

	CleanEmptyParents(nestedDir, musicDir)

	if fileExists(filepath.Join(musicDir, "EmptyArtist")) || dirExists(filepath.Join(musicDir, "EmptyArtist")) {
		t.Errorf("expected EmptyArtist to be cleaned up")
	}
	if !dirExists(musicDir) {
		t.Errorf("expected root musicDir to remain intact")
	}

	// 2. Partial cleanup when sibling has files
	artistDir := filepath.Join(musicDir, "ActiveArtist")
	album1 := filepath.Join(artistDir, "EmptyAlbum")
	album2 := filepath.Join(artistDir, "FullAlbum")
	_ = os.MkdirAll(album1, 0755)
	_ = os.MkdirAll(album2, 0755)
	_ = os.WriteFile(filepath.Join(album2, "song.mp3"), []byte("data"), 0644)

	CleanEmptyParents(album1, musicDir)

	if dirExists(album1) {
		t.Errorf("expected album1 to be removed")
	}
	if !dirExists(artistDir) {
		t.Errorf("expected active artistDir to be preserved because album2 has files")
	}
}

func TestOrganizer_RemoveIfEmpty(t *testing.T) {
	tempDir := t.TempDir()
	emptyDir := filepath.Join(tempDir, "empty")
	_ = os.MkdirAll(emptyDir, 0755)

	RemoveIfEmpty(emptyDir)
	if dirExists(emptyDir) {
		t.Errorf("expected empty dir to be removed")
	}

	fullDir := filepath.Join(tempDir, "full")
	_ = os.MkdirAll(fullDir, 0755)
	_ = os.WriteFile(filepath.Join(fullDir, "file.txt"), []byte("hi"), 0644)

	RemoveIfEmpty(fullDir)
	if !dirExists(fullDir) {
		t.Errorf("expected non-empty dir to not be removed")
	}
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

