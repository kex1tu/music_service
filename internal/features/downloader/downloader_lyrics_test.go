package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
)

type mockFeatureYandexClient struct {
	lyrics string
	synced bool
	err    error
	calls  int32
	lastID int64
}

func (m *mockFeatureYandexClient) GetTrackLyrics(ctx context.Context, trackID int64) (string, bool, error) {
	atomic.AddInt32(&m.calls, 1)
	m.lastID = trackID
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	return m.lyrics, m.synced, m.err
}

type mockFeatureLRCLIB struct {
	result *lyrics.LyricsSearchResult
	err    error
	calls  int32
}

func (m *mockFeatureLRCLIB) Search(ctx context.Context, artist, title, album string, duration int) (*lyrics.LyricsSearchResult, error) {
	atomic.AddInt32(&m.calls, 1)
	return m.result, m.err
}

func setupFeatureMockMediaBinaries(t *testing.T) string {
	t.Helper()
	fakeDir := t.TempDir()

	ytDlpSrc := filepath.Join(fakeDir, "mock_ytdlp.go")
	ytDlpSrcCode := `package main

import (
	"os"
	"path/filepath"
	"strings"
)

func main() {
	var out string
	for i := 0; i < len(os.Args); i++ {
		if os.Args[i] == "-o" && i+1 < len(os.Args) {
			out = os.Args[i+1]
			break
		}
	}
	if out != "" {
		realOut := strings.Replace(out, "%(ext)s", "mp3", 1)
		_ = os.MkdirAll(filepath.Dir(realOut), 0755)
		minimalMP3 := []byte{0xFF, 0xFB, 0x90, 0x64, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		_ = os.WriteFile(realOut, minimalMP3, 0644)
	}
}
`
	if err := os.WriteFile(ytDlpSrc, []byte(ytDlpSrcCode), 0644); err != nil {
		t.Fatalf("failed to write mock_ytdlp.go: %v", err)
	}
	fakeYtDlpBin := filepath.Join(fakeDir, "yt-dlp")
	buildCmd := exec.Command("go", "build", "-o", fakeYtDlpBin, ytDlpSrc)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to compile mock yt-dlp ELF: %v, out: %s", err, string(out))
	}

	denoScript := "#!/bin/sh\nprintf 'deno 2.9.8\\n'\n"
	if err := os.WriteFile(filepath.Join(fakeDir, "deno"), []byte(denoScript), 0755); err != nil {
		t.Fatalf("failed to write fake deno: %v", err)
	}

	fakeFfmpeg := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "aspectralstats" ] || echo "$arg" | grep -q "aspectralstats"; then
    echo "lavfi.aspectralstats.rolloff=20500.0" >&2
    exit 0
  fi
done
last=""
for arg in "$@"; do
  last="$arg"
done
if [ -n "$last" ]; then
  mkdir -p "$(dirname "$last")"
  printf '\xff\xfb\x90\x64\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00' > "$last"
fi
exit 0
`
	fakeFfprobe := `#!/bin/sh
echo '{"format":{"duration":"180.0","bit_rate":"320000"},"streams":[{"codec_name":"mp3","bit_rate":"320000","sample_rate":"44100"}]}'
exit 0
`
	if err := os.WriteFile(filepath.Join(fakeDir, "ffmpeg"), []byte(fakeFfmpeg), 0755); err != nil {
		t.Fatalf("failed to write fake ffmpeg: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fakeDir, "ffprobe"), []byte(fakeFfprobe), 0755); err != nil {
		t.Fatalf("failed to write fake ffprobe: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+origPath)
	return fakeDir
}

func TestFeaturesDownloader_SingleTrack_YandexLyricsLRC_And_NoID3Lyrics(t *testing.T) {
	setupFeatureMockMediaBinaries(t)

	tmpDir := t.TempDir()
	musicDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(musicDir)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("fake-cover-bytes"))
	}))
	defer mockServer.Close()

	mockYandex := &mockFeatureYandexClient{
		lyrics: "[00:02.00] Feature Yandex Synced Line 1\n[00:08.00] Feature Yandex Synced Line 2",
		synced: true,
	}
	mockLrcLib := &mockFeatureLRCLIB{
		result: &lyrics.LyricsSearchResult{
			Lyrics: "[00:10.00] LRCLIB lyrics",
			Synced: true,
			Source: "lrclib",
		},
	}

	lyricsSvc := lyrics.NewService(musicSb, mockLrcLib)
	lyricsSvc.SetYandexClient(mockYandex)

	svc := NewService(ServiceConfig{
		TmpSandbox:   tmpSb,
		MusicSandbox: musicSb,
		Lyrics:       lyricsSvc,
	})
	svc.client = mockServer.Client()

	job := &Job{
		ID:     "feat-yandex-lrc-job",
		Status: StatusDownloading,
		Tracks: []resolver.ResolvedTrack{
			{
				TrackID:        "ya_88888_1",
				CatalogTrackID: "ya_88888",
				YandexTrackID:  88888,
				TrackNumber:    1,
				Title:          "Hit Song",
				Artist:         "Feature Artist",
				Album:          "Feature Album",
				Duration:       180,
				AudioSourceURL: "https://music.youtube.com/watch?v=dummy",
			},
		},
	}

	rawDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "raw"), true)
	convertedDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "converted"), true)
	_ = safefs.MkdirAll(rawDir, 0755)
	_ = safefs.MkdirAll(convertedDir, 0755)

	meta := &resolver.ReleaseMetadata{
		AlbumTitle:  "Feature Album",
		AlbumArtist: "Feature Artist",
		CoverURL:    mockServer.URL + "/cover.jpg",
	}

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

	if !ok {
		var trErr string
		if job.Tracks[0].Error != nil {
			trErr = *job.Tracks[0].Error
		}
		t.Fatalf("expected processSingleTrack to return true, got false (err: %s)", trErr)
	}

	// 1. Verify Yandex client called with exact ID 88888
	if atomic.LoadInt32(&mockYandex.calls) != 1 {
		t.Errorf("expected 1 call to Yandex client, got %d", mockYandex.calls)
	}
	if mockYandex.lastID != 88888 {
		t.Errorf("expected Yandex track ID 88888, got %d", mockYandex.lastID)
	}
	if atomic.LoadInt32(&mockLrcLib.calls) != 0 {
		t.Errorf("expected 0 calls to LRCLIB fallback, got %d", mockLrcLib.calls)
	}

	// 2. Verify companion .lrc file in convertedDir
	expectedLrcPath := filepath.Join(convertedDir, "01. Hit Song.lrc")
	lrcContent, err := safefs.ReadFile(expectedLrcPath)
	if err != nil {
		t.Fatalf("expected .lrc companion file at %s: %v", expectedLrcPath, err)
	}
	if string(lrcContent) != mockYandex.lyrics {
		t.Errorf("expected .lrc content %q, got %q", mockYandex.lyrics, string(lrcContent))
	}

	// 3. Verify MP3 ID3 tags: MUST NOT contain USLT or SYLT
	mp3Path := filepath.Join(convertedDir, "01. Hit Song.mp3")
	rawFrames, err := id3.ReadRawFrames(mp3Path)
	if err != nil {
		t.Fatalf("failed to read raw ID3 frames from %s: %v", mp3Path, err)
	}
	if len(rawFrames) == 0 {
		t.Fatalf("expected non-zero ID3 frames in generated MP3")
	}
	for _, frame := range rawFrames {
		if frame.ID == "USLT" || frame.ID == "SYLT" {
			t.Errorf("FORBIDDEN lyrics frame %q found in MP3 tags!", frame.ID)
		}
	}

	// 4. Verify companion relocation with organizer.MoveTrackWithCompanions
	destMP3 := filepath.Join(musicDir, "Feature Artist", "Feature Album", "01. Hit Song.mp3")
	_ = os.MkdirAll(filepath.Dir(destMP3), 0755)
	actualDest, err := organizer.MoveTrackWithCompanions(mp3Path, destMP3, false)
	if err != nil {
		t.Fatalf("MoveTrackWithCompanions failed: %v", err)
	}
	if actualDest != destMP3 {
		t.Errorf("expected dest %q, got %q", destMP3, actualDest)
	}

	destLRC := filepath.Join(musicDir, "Feature Artist", "Feature Album", "01. Hit Song.lrc")
	movedLrcContent, err := safefs.ReadFile(destLRC)
	if err != nil {
		t.Fatalf("expected moved .lrc at %s: %v", destLRC, err)
	}
	if string(movedLrcContent) != mockYandex.lyrics {
		t.Errorf("moved .lrc content mismatch: got %q", string(movedLrcContent))
	}
}

func TestFeaturesDownloader_SingleTrack_YandexLyricsPlainText(t *testing.T) {
	setupFeatureMockMediaBinaries(t)

	tmpDir := t.TempDir()
	musicDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(musicDir)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("fake-cover-bytes"))
	}))
	defer mockServer.Close()

	mockYandex := &mockFeatureYandexClient{
		lyrics: "Feature plain text lyrics line 1\nFeature plain text lyrics line 2",
		synced: false,
	}

	lyricsSvc := lyrics.NewService(musicSb)
	lyricsSvc.SetYandexClient(mockYandex)

	svc := NewService(ServiceConfig{
		TmpSandbox:   tmpSb,
		MusicSandbox: musicSb,
		Lyrics:       lyricsSvc,
	})
	svc.client = mockServer.Client()

	job := &Job{
		ID:     "feat-yandex-txt-job",
		Status: StatusDownloading,
		Tracks: []resolver.ResolvedTrack{
			{
				TrackID:        "ya_77777_1",
				CatalogTrackID: "ya_77777",
				YandexTrackID:  77777,
				TrackNumber:    1,
				Title:          "Acoustic Song",
				Artist:         "Acoustic Artist",
				Album:          "Acoustic Album",
				Duration:       180,
				AudioSourceURL: "https://music.youtube.com/watch?v=dummy",
			},
		},
	}

	rawDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "raw"), true)
	convertedDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "converted"), true)
	_ = safefs.MkdirAll(rawDir, 0755)
	_ = safefs.MkdirAll(convertedDir, 0755)

	meta := &resolver.ReleaseMetadata{
		AlbumTitle:  "Acoustic Album",
		AlbumArtist: "Acoustic Artist",
		CoverURL:    mockServer.URL + "/cover.jpg",
	}

	ok := svc.processSingleTrack(
		context.Background(),
		job,
		meta,
		1,
		job.Tracks[0],
		rawDir,
		convertedDir,
		nil,
		1,
	)

	if !ok {
		t.Fatalf("expected processSingleTrack to return true")
	}

	// 1. Verify companion .txt exists
	txtPath := filepath.Join(convertedDir, "01. Acoustic Song.txt")
	txtContent, err := safefs.ReadFile(txtPath)
	if err != nil {
		t.Fatalf("expected .txt companion file at %s: %v", txtPath, err)
	}
	if string(txtContent) != mockYandex.lyrics {
		t.Errorf("expected .txt content %q, got %q", mockYandex.lyrics, string(txtContent))
	}

	// 2. Verify no .lrc exists
	lrcPath := filepath.Join(convertedDir, "01. Acoustic Song.lrc")
	if _, err := safefs.Stat(lrcPath); !os.IsNotExist(err) {
		t.Errorf("did not expect .lrc when plain text lyrics received")
	}

	// 3. Confirm NO USLT/SYLT in MP3
	mp3Path := filepath.Join(convertedDir, "01. Acoustic Song.mp3")
	rawFrames, err := id3.ReadRawFrames(mp3Path)
	if err != nil {
		t.Fatalf("failed to read raw ID3 frames: %v", err)
	}
	for _, frame := range rawFrames {
		if frame.ID == "USLT" || frame.ID == "SYLT" {
			t.Errorf("FORBIDDEN lyrics frame %q found in MP3 tags!", frame.ID)
		}
	}
}

func TestFeaturesDownloader_SingleTrack_LyricsErrorDoesNotAbortAudio(t *testing.T) {
	setupFeatureMockMediaBinaries(t)

	tmpDir := t.TempDir()
	musicDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(musicDir)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("fake-cover-bytes"))
	}))
	defer mockServer.Close()

	mockYandex := &mockFeatureYandexClient{
		err: resolver.ErrYandexLyricsNotFound,
	}
	mockLrcLib := &mockFeatureLRCLIB{
		err: fmt.Errorf("lrclib unavailable"),
	}

	lyricsSvc := lyrics.NewService(musicSb, mockLrcLib)
	lyricsSvc.SetYandexClient(mockYandex)

	svc := NewService(ServiceConfig{
		TmpSandbox:   tmpSb,
		MusicSandbox: musicSb,
		Lyrics:       lyricsSvc,
	})
	svc.client = mockServer.Client()

	job := &Job{
		ID:     "feat-lyrics-err-job",
		Status: StatusDownloading,
		Tracks: []resolver.ResolvedTrack{
			{
				TrackID:        "ya_66666_1",
				CatalogTrackID: "ya_66666",
				YandexTrackID:  66666,
				TrackNumber:    1,
				Title:          "Survivor Song",
				Artist:         "Survivor Artist",
				Album:          "Survivor Album",
				Duration:       180,
				AudioSourceURL: "https://music.youtube.com/watch?v=dummy",
			},
		},
	}

	rawDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "raw"), true)
	convertedDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "converted"), true)
	_ = safefs.MkdirAll(rawDir, 0755)
	_ = safefs.MkdirAll(convertedDir, 0755)

	meta := &resolver.ReleaseMetadata{
		AlbumTitle:  "Survivor Album",
		AlbumArtist: "Survivor Artist",
		CoverURL:    mockServer.URL + "/cover.jpg",
	}

	ok := svc.processSingleTrack(
		context.Background(),
		job,
		meta,
		1,
		job.Tracks[0],
		rawDir,
		convertedDir,
		nil,
		1,
	)

	// Invariant: audio download and conversion must succeed even if lyrics fail
	if !ok {
		t.Fatalf("expected processSingleTrack to return true even when lyrics fail")
	}

	if job.Tracks[0].Status != "ready" {
		t.Errorf("expected track status 'ready', got %q", job.Tracks[0].Status)
	}

	// Converted MP3 must exist
	mp3Path := filepath.Join(convertedDir, "01. Survivor Song.mp3")
	if info, err := safefs.Stat(mp3Path); err != nil || info.Size() == 0 {
		t.Fatalf("expected non-empty MP3 at %s", mp3Path)
	}

	// No lyrics companion should be written
	lrcPath := filepath.Join(convertedDir, "01. Survivor Song.lrc")
	txtPath := filepath.Join(convertedDir, "01. Survivor Song.txt")
	if _, err := safefs.Stat(lrcPath); !os.IsNotExist(err) {
		t.Errorf("unexpected .lrc companion when lyrics failed")
	}
	if _, err := safefs.Stat(txtPath); !os.IsNotExist(err) {
		t.Errorf("unexpected .txt companion when lyrics failed")
	}
}

func TestFeaturesDownloader_JobQueueRestoration_PreservesYandexIdentity(t *testing.T) {
	jobJSON := `{
		"id": "job-feat-yandex",
		"status": "QUEUED",
		"source": "yandex",
		"tracks": [
			{
				"track_id": "ya_12345_1",
				"catalog_track_id": "ya_12345",
				"yandex_track_id": 12345,
				"title": "Modern Track",
				"artist": "Modern Artist"
			},
			{
				"track_id": "ya_67890_2",
				"catalog_track_id": "ya_67890",
				"title": "Legacy Catalog Track",
				"artist": "Legacy Artist"
			},
			{
				"track_id": "ya_13579_1",
				"title": "Legacy Slot Track",
				"artist": "Legacy Artist"
			},
			{
				"track_id": "dz_24680",
				"catalog_track_id": "dz_24680",
				"title": "Deezer Track",
				"artist": "Deezer Artist"
			}
		]
	}`

	var j Job
	if err := json.Unmarshal([]byte(jobJSON), &j); err != nil {
		t.Fatalf("failed to unmarshal Job: %v", err)
	}
	if len(j.Tracks) != 4 {
		t.Fatalf("expected 4 tracks, got %d", len(j.Tracks))
	}

	// Track 0: Modern with explicit ID
	id0, ok0 := j.Tracks[0].GetYandexTrackID()
	if !ok0 || id0 != 12345 {
		t.Errorf("track 0: expected 12345, got %d (ok=%v)", id0, ok0)
	}

	// Track 1: Legacy with catalog_track_id
	id1, ok1 := j.Tracks[1].GetYandexTrackID()
	if !ok1 || id1 != 67890 {
		t.Errorf("track 1: expected 67890, got %d (ok=%v)", id1, ok1)
	}

	// Track 2: Legacy with slot track_id
	id2, ok2 := j.Tracks[2].GetYandexTrackID()
	if !ok2 || id2 != 13579 {
		t.Errorf("track 2: expected 13579, got %d (ok=%v)", id2, ok2)
	}

	// Track 3: Deezer track must not be treated as Yandex
	id3, ok3 := j.Tracks[3].GetYandexTrackID()
	if ok3 || id3 != 0 {
		t.Errorf("track 3: expected non-Yandex, got %d (ok=%v)", id3, ok3)
	}
}
