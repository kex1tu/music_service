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

type mockDownloaderYandexClient struct {
	lyrics string
	synced bool
	err    error
	calls  int32
	lastID int64
}

func (m *mockDownloaderYandexClient) GetTrackLyrics(ctx context.Context, trackID int64) (string, bool, error) {
	atomic.AddInt32(&m.calls, 1)
	m.lastID = trackID
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	return m.lyrics, m.synced, m.err
}

type mockDownloaderLRCLIB struct {
	result *lyrics.LyricsSearchResult
	err    error
	calls  int32
}

func (m *mockDownloaderLRCLIB) Search(ctx context.Context, artist, title, album string, duration int) (*lyrics.LyricsSearchResult, error) {
	atomic.AddInt32(&m.calls, 1)
	return m.result, m.err
}

// setupMockMediaBinaries sets up fake yt-dlp (compiled ELF binary), deno, ffmpeg and ffprobe executables in a temp directory
// and prepends it to PATH so real downloads/conversions don't hit external networks or require heavy toolchains.
func setupMockMediaBinaries(t *testing.T) string {
	t.Helper()
	fakeDir := t.TempDir()

	// 1. Compile a real Linux ELF binary for yt-dlp to satisfy mediaexec.ValidateRuntime
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

	// 2. Deno script for ValidateRuntime
	denoScript := "#!/bin/sh\nprintf 'deno 2.9.8\\n'\n"
	if err := os.WriteFile(filepath.Join(fakeDir, "deno"), []byte(denoScript), 0755); err != nil {
		t.Fatalf("failed to write fake deno: %v", err)
	}

	// 3. Fake ffmpeg: supports aspectralstats and transcoding
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
	// 4. Fake ffprobe: returns format and audio stream json
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

func TestPipeline_YandexLyrics_LRC_And_NoID3Lyrics(t *testing.T) {
	setupMockMediaBinaries(t)

	tmpDir := t.TempDir()
	musicDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(musicDir)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("fake-cover-bytes"))
	}))
	defer mockServer.Close()

	svc := NewService(context.Background(), NewJobManager(nil), nil, tmpSb, musicSb, "", 1)
	svc.client = mockServer.Client()
	defer svc.Close()

	mockYandex := &mockDownloaderYandexClient{
		lyrics: "[00:01.00] Official Yandex Synced Line 1\n[00:05.00] Official Yandex Synced Line 2",
		synced: true,
	}
	mockLrcLib := &mockDownloaderLRCLIB{
		result: &lyrics.LyricsSearchResult{
			Lyrics: "[00:10.00] LRCLIB lyrics",
			Synced: true,
			Source: "lrclib",
		},
	}

	lyricsSvc := lyrics.NewService(musicSb, mockLrcLib)
	lyricsSvc.SetYandexClient(mockYandex)
	svc.SetLyricsService(lyricsSvc)

	job := &DownloadJob{
		ID:     "yandex-lyrics-lrc-job",
		Status: StatusResolving,
	}

	rawDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "raw"), true)
	convertedDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "converted"), true)
	_ = safefs.MkdirAll(rawDir, 0755)
	_ = safefs.MkdirAll(convertedDir, 0755)

	track := resolver.ResolvedTrack{
		TrackID:        "ya_98765_1",
		CatalogTrackID: "ya_98765",
		YandexTrackID:  98765,
		TrackNumber:    1,
		Title:          "Official Hit",
		Artist:         "Yandex Artist",
		Album:          "Official Album",
		Duration:       180,
		AudioSourceURL: "https://music.youtube.com/watch?v=dummy",
	}

	meta := &resolver.ReleaseMetadata{
		AlbumTitle:  "Official Album",
		AlbumArtist: "Yandex Artist",
		CoverURL:    mockServer.URL + "/cover.jpg",
	}

	svc.processTracksPipeline(context.Background(), job, meta, []resolver.ResolvedTrack{track}, rawDir, convertedDir)

	if job.Status != StatusReady {
		var trErr string
		if len(job.Tracks) > 0 && job.Tracks[0].Error != nil {
			trErr = *job.Tracks[0].Error
		}
		t.Fatalf("expected job status READY, got %s (job err: %v, track err: %s)", job.Status, job.Error, trErr)
	}

	// 1. Verify Yandex client was called with exact ID
	if atomic.LoadInt32(&mockYandex.calls) != 1 {
		t.Errorf("expected 1 call to Yandex client, got %d", mockYandex.calls)
	}
	if mockYandex.lastID != 98765 {
		t.Errorf("expected Yandex track ID 98765, got %d", mockYandex.lastID)
	}
	// LRCLIB should not have been called because Yandex succeeded
	if atomic.LoadInt32(&mockLrcLib.calls) != 0 {
		t.Errorf("expected 0 calls to LRCLIB fallback, got %d", mockLrcLib.calls)
	}

	// 2. Verify companion .lrc file exists in convertedDir
	expectedLrcPath := filepath.Join(convertedDir, "01. Official Hit.lrc")
	lrcContent, err := safefs.ReadFile(expectedLrcPath)
	if err != nil {
		t.Fatalf("expected .lrc companion file at %s, error: %v", expectedLrcPath, err)
	}
	if string(lrcContent) != mockYandex.lyrics {
		t.Errorf("expected .lrc content %q, got %q", mockYandex.lyrics, string(lrcContent))
	}

	// 3. Verify no .txt file was created
	txtPath := filepath.Join(convertedDir, "01. Official Hit.txt")
	if _, err := safefs.Stat(txtPath); !os.IsNotExist(err) {
		t.Errorf("did not expect .txt file when synced LRC lyrics exist")
	}

	// 4. Verify MP3 ID3 tags: MUST NOT contain USLT or SYLT frames
	mp3Path := filepath.Join(convertedDir, "01. Official Hit.mp3")
	rawFrames, err := id3.ReadRawFrames(mp3Path)
	if err != nil {
		t.Fatalf("failed to read raw ID3 frames from %s: %v", mp3Path, err)
	}
	if len(rawFrames) == 0 {
		t.Fatalf("expected non-zero ID3 frames in generated MP3")
	}
	for _, frame := range rawFrames {
		if frame.ID == "USLT" || frame.ID == "SYLT" {
			t.Errorf("FORBIDDEN lyrics frame %q found in MP3 tags! Lyrics must remain external only", frame.ID)
		}
	}

	// 5. Verify companion relocation with organizer.MoveTrackWithCompanions
	destMP3 := filepath.Join(musicDir, "Yandex Artist", "Official Album", "01. Official Hit.mp3")
	_ = os.MkdirAll(filepath.Dir(destMP3), 0755)
	actualDest, err := organizer.MoveTrackWithCompanions(mp3Path, destMP3, false)
	if err != nil {
		t.Fatalf("MoveTrackWithCompanions failed: %v", err)
	}
	if actualDest != destMP3 {
		t.Errorf("expected dest %q, got %q", destMP3, actualDest)
	}

	destLRC := filepath.Join(musicDir, "Yandex Artist", "Official Album", "01. Official Hit.lrc")
	movedLrcContent, err := safefs.ReadFile(destLRC)
	if err != nil {
		t.Fatalf("expected moved .lrc at %s, error: %v", destLRC, err)
	}
	if string(movedLrcContent) != mockYandex.lyrics {
		t.Errorf("moved .lrc content mismatch: got %q", string(movedLrcContent))
	}
}

func TestPipeline_YandexLyrics_PlainText_SavedAsTXT(t *testing.T) {
	setupMockMediaBinaries(t)

	tmpDir := t.TempDir()
	musicDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(musicDir)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("fake-cover-bytes"))
	}))
	defer mockServer.Close()

	svc := NewService(context.Background(), NewJobManager(nil), nil, tmpSb, musicSb, "", 1)
	svc.client = mockServer.Client()
	defer svc.Close()

	mockYandex := &mockDownloaderYandexClient{
		lyrics: "Plain text lyrics line 1\nPlain text lyrics line 2",
		synced: false,
	}

	lyricsSvc := lyrics.NewService(musicSb)
	lyricsSvc.SetYandexClient(mockYandex)
	svc.SetLyricsService(lyricsSvc)

	job := &DownloadJob{
		ID:     "yandex-lyrics-txt-job",
		Status: StatusResolving,
	}

	rawDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "raw"), true)
	convertedDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "converted"), true)
	_ = safefs.MkdirAll(rawDir, 0755)
	_ = safefs.MkdirAll(convertedDir, 0755)

	track := resolver.ResolvedTrack{
		TrackID:        "ya_54321_1",
		CatalogTrackID: "ya_54321",
		YandexTrackID:  54321,
		TrackNumber:    1,
		Title:          "Plain Song",
		Artist:         "Yandex Singer",
		Album:          "Acoustic Album",
		Duration:       180,
		AudioSourceURL: "https://music.youtube.com/watch?v=dummy",
	}

	meta := &resolver.ReleaseMetadata{
		AlbumTitle:  "Acoustic Album",
		AlbumArtist: "Yandex Singer",
		CoverURL:    mockServer.URL + "/cover.jpg",
	}

	svc.processTracksPipeline(context.Background(), job, meta, []resolver.ResolvedTrack{track}, rawDir, convertedDir)

	if job.Status != StatusReady {
		t.Fatalf("expected job status READY, got %s", job.Status)
	}

	// 1. Verify companion .txt exists
	txtPath := filepath.Join(convertedDir, "01. Plain Song.txt")
	txtContent, err := safefs.ReadFile(txtPath)
	if err != nil {
		t.Fatalf("expected .txt companion file at %s: %v", txtPath, err)
	}
	if string(txtContent) != mockYandex.lyrics {
		t.Errorf("expected .txt content %q, got %q", mockYandex.lyrics, string(txtContent))
	}

	// 2. Verify no .lrc exists
	lrcPath := filepath.Join(convertedDir, "01. Plain Song.lrc")
	if _, err := safefs.Stat(lrcPath); !os.IsNotExist(err) {
		t.Errorf("did not expect .lrc when plain text lyrics received")
	}

	// 3. Confirm NO USLT/SYLT in MP3
	mp3Path := filepath.Join(convertedDir, "01. Plain Song.mp3")
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

func TestPipeline_LyricsProviderError_DoesNotAbortAudio(t *testing.T) {
	setupMockMediaBinaries(t)

	tmpDir := t.TempDir()
	musicDir := t.TempDir()
	tmpSb, _ := sandbox.NewPathSandbox(tmpDir)
	musicSb, _ := sandbox.NewPathSandbox(musicDir)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("fake-cover-bytes"))
	}))
	defer mockServer.Close()

	svc := NewService(context.Background(), NewJobManager(nil), nil, tmpSb, musicSb, "", 1)
	svc.client = mockServer.Client()
	defer svc.Close()

	// Both Yandex and LRCLIB return errors
	mockYandex := &mockDownloaderYandexClient{
		err: resolver.ErrYandexLyricsNotFound,
	}
	mockLrcLib := &mockDownloaderLRCLIB{
		err: fmt.Errorf("lrclib network timeout"),
	}

	lyricsSvc := lyrics.NewService(musicSb, mockLrcLib)
	lyricsSvc.SetYandexClient(mockYandex)
	svc.SetLyricsService(lyricsSvc)

	job := &DownloadJob{
		ID:     "lyrics-failure-job",
		Status: StatusResolving,
	}

	rawDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "raw"), true)
	convertedDir, _ := tmpSb.Resolve(filepath.Join(job.ID, "converted"), true)
	_ = safefs.MkdirAll(rawDir, 0755)
	_ = safefs.MkdirAll(convertedDir, 0755)

	track := resolver.ResolvedTrack{
		TrackID:        "ya_11111_1",
		CatalogTrackID: "ya_11111",
		YandexTrackID:  11111,
		TrackNumber:    1,
		Title:          "Resilient Song",
		Artist:         "Resilient Artist",
		Album:          "Resilient Album",
		Duration:       180,
		AudioSourceURL: "https://music.youtube.com/watch?v=dummy",
	}

	meta := &resolver.ReleaseMetadata{
		AlbumTitle:  "Resilient Album",
		AlbumArtist: "Resilient Artist",
		CoverURL:    mockServer.URL + "/cover.jpg",
	}

	svc.processTracksPipeline(context.Background(), job, meta, []resolver.ResolvedTrack{track}, rawDir, convertedDir)

	// Invariant: audio download and conversion must succeed even if lyrics fail
	if job.Status != StatusReady {
		t.Fatalf("expected job status READY, got %s (error: %v)", job.Status, job.Error)
	}

	// Converted MP3 must exist
	mp3Path := filepath.Join(convertedDir, "01. Resilient Song.mp3")
	if info, err := safefs.Stat(mp3Path); err != nil || info.Size() == 0 {
		t.Fatalf("expected non-empty MP3 at %s", mp3Path)
	}

	// No lyrics companion should be written
	lrcPath := filepath.Join(convertedDir, "01. Resilient Song.lrc")
	txtPath := filepath.Join(convertedDir, "01. Resilient Song.txt")
	if _, err := safefs.Stat(lrcPath); !os.IsNotExist(err) {
		t.Errorf("unexpected .lrc companion when lyrics failed")
	}
	if _, err := safefs.Stat(txtPath); !os.IsNotExist(err) {
		t.Errorf("unexpected .txt companion when lyrics failed")
	}
}

func TestDownloadJob_QueueRestoration_PreservesYandexIdentity(t *testing.T) {
	// 1. Modern job with explicit YandexTrackID
	jobJSON := `{
		"id": "job-modern-yandex",
		"status": "QUEUED",
		"source": "yandex",
		"url": "https://music.yandex.ru/album/1000/track/2000",
		"tracks": [
			{
				"track_id": "ya_2000_1",
				"catalog_track_id": "ya_2000",
				"yandex_track_id": 2000,
				"title": "Modern Track",
				"artist": "Modern Artist",
				"duration": 200
			}
		]
	}`

	var job1 DownloadJob
	if err := json.Unmarshal([]byte(jobJSON), &job1); err != nil {
		t.Fatalf("failed to unmarshal modern job: %v", err)
	}
	if len(job1.Tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(job1.Tracks))
	}
	id, ok := job1.Tracks[0].GetYandexTrackID()
	if !ok || id != 2000 {
		t.Errorf("expected YandexTrackID 2000, got %d (ok=%v)", id, ok)
	}

	// 2. Legacy job with only catalog_track_id (yandex_track_id omitted)
	legacyCatalogJSON := `{
		"id": "job-legacy-catalog",
		"status": "QUEUED",
		"source": "yandex",
		"tracks": [
			{
				"track_id": "ya_3000_1",
				"catalog_track_id": "ya_3000",
				"title": "Legacy Catalog Track",
				"artist": "Legacy Artist"
			}
		]
	}`

	var job2 DownloadJob
	if err := json.Unmarshal([]byte(legacyCatalogJSON), &job2); err != nil {
		t.Fatalf("failed to unmarshal legacy catalog job: %v", err)
	}
	id2, ok2 := job2.Tracks[0].GetYandexTrackID()
	if !ok2 || id2 != 3000 {
		t.Errorf("expected YandexTrackID 3000 extracted from catalog_track_id, got %d (ok=%v)", id2, ok2)
	}

	// 3. Legacy job with only track_id having slot suffix
	legacySlotJSON := `{
		"id": "job-legacy-slot",
		"status": "QUEUED",
		"source": "yandex",
		"tracks": [
			{
				"track_id": "ya_4000_3",
				"title": "Legacy Slot Track",
				"artist": "Legacy Artist"
			}
		]
	}`

	var job3 DownloadJob
	if err := json.Unmarshal([]byte(legacySlotJSON), &job3); err != nil {
		t.Fatalf("failed to unmarshal legacy slot job: %v", err)
	}
	id3, ok3 := job3.Tracks[0].GetYandexTrackID()
	if !ok3 || id3 != 4000 {
		t.Errorf("expected YandexTrackID 4000 extracted from track_id, got %d (ok=%v)", id3, ok3)
	}

	// 4. Foreign catalog track (Deezer / YouTube) must never be treated as Yandex
	foreignJSON := `{
		"id": "job-foreign",
		"status": "QUEUED",
		"source": "deezer",
		"tracks": [
			{
				"track_id": "dz_5000",
				"catalog_track_id": "dz_5000",
				"title": "Deezer Track",
				"artist": "Deezer Artist"
			},
			{
				"track_id": "yt_dQw4w9WgXcQ",
				"title": "YouTube Track",
				"artist": "YouTube Artist"
			},
			{
				"track_id": "5000",
				"catalog_track_id": "5000",
				"title": "Raw Numeric Track",
				"artist": "Numeric Artist"
			}
		]
	}`

	var job4 DownloadJob
	if err := json.Unmarshal([]byte(foreignJSON), &job4); err != nil {
		t.Fatalf("failed to unmarshal foreign job: %v", err)
	}
	for i, tr := range job4.Tracks {
		if foreignID, isYa := tr.GetYandexTrackID(); isYa {
			t.Errorf("track %d (%q): expected NOT Yandex, but got YandexTrackID %d", i, tr.TrackID, foreignID)
		}
	}
}
