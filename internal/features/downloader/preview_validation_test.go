package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/services/resolver"
)

func TestFeaturesDownloader_ProcessSingleTrack_PreviewSnippetBlocked(t *testing.T) {
	fakeDir := setupFeatureMockMediaBinaries(t)

	// Override ffprobe in fakeDir to return actual duration 30.0s (a preview snippet)
	ffprobeScript := `#!/bin/sh
echo '{"format":{"duration":"30.0","bit_rate":"320000"},"streams":[{"codec_name":"mp3","bit_rate":"320000","sample_rate":"44100"}]}'
exit 0
`
	if err := os.WriteFile(filepath.Join(fakeDir, "ffprobe"), []byte(ffprobeScript), 0755); err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	musicDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(musicDir)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("fake-cover-bytes"))
	}))
	defer mockServer.Close()

	svc := NewService(ServiceConfig{
		TmpSandbox:   tmpSb,
		MusicSandbox: musicSb,
	})
	svc.client = mockServer.Client()

	job := &Job{
		ID:     "feat-preview-job",
		Status: StatusDownloading,
		Tracks: []resolver.ResolvedTrack{
			{
				TrackID:        "track-01",
				TrackNumber:    1,
				Title:          "Full Length Anthem",
				Artist:         "The Artist",
				Album:          "The Album",
				Duration:       210, // Expected 210 seconds!
				AudioSourceURL: "https://soundcloud.com/the-artist/snippet",
				Status:         "pending",
			},
		},
		TotalTracks: 1,
	}

	rawDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "raw"), true)
	convertedDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "converted"), true)
	_ = safefs.MkdirAll(rawDir, 0755)
	_ = safefs.MkdirAll(convertedDir, 0755)

	meta := &resolver.ReleaseMetadata{
		AlbumTitle:  "The Album",
		AlbumArtist: "The Artist",
		CoverURL:    mockServer.URL + "/cover.jpg",
	}

	// Process track: yt-dlp generates file, but ffprobe reports 30.0s for 210s expected
	ok := svc.processSingleTrack(
		context.Background(),
		job,
		meta,
		1,
		job.Tracks[0],
		rawDir,
		convertedDir,
		[]byte("fake-cover-bytes"),
		1,
	)

	// ok should be false because preview was detected
	if ok {
		t.Fatal("expected processSingleTrack to return false for 30s preview snippet, got true")
	}

	// Track status must be waiting_source
	snap := job.Snapshot()
	if snap.Tracks[0].Status != "waiting_source" {
		t.Errorf("expected track status 'waiting_source', got %q", snap.Tracks[0].Status)
	}
	if snap.Tracks[0].Error == nil || !strings.Contains(*snap.Tracks[0].Error, "detected 30s preview snippet") {
		t.Errorf("expected error containing 'detected 30s preview snippet', got: %v", snap.Tracks[0].Error)
	}

	// Converted MP3 must NOT exist
	matches, _ := safefs.Glob(filepath.Join(convertedDir, "*.mp3"))
	if len(matches) > 0 {
		t.Errorf("expected 0 converted MP3 files, found: %v", matches)
	}

	// Staging raw audio file must NOT remain
	rawMatches, _ := safefs.Glob(filepath.Join(rawDir, "*"))
	if len(rawMatches) > 0 {
		t.Errorf("expected raw directory to be clean after failure, found: %v", rawMatches)
	}
}
