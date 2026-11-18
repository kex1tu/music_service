package audit

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"music-manager/internal/core/mediaexec"
	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/id3"
	"music-manager/internal/services/censorcheck"
)

type testTranscriber struct {
	calls   atomic.Int32
	block   atomic.Bool
	entered chan struct{}
}

func (c *testTranscriber) Identity() string { return "fixture:verbatim-v1" }
func (c *testTranscriber) Transcribe(ctx context.Context, b []byte) (censorcheck.Transcript, error) {
	c.calls.Add(1)
	if c.block.Load() {
		select {
		case c.entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return censorcheck.Transcript{}, ctx.Err()
	}
	return censorcheck.Transcript{Text: "тут лежит закладка под сосной", Provider: "fixture", Model: "fixture", Settings: c.Identity()}, nil
}

func auditFixture(t *testing.T) (*db.DB, *sandbox.PathSandbox) {
	t.Helper()
	if _, e := exec.LookPath("ffmpeg"); e != nil {
		t.Skip("ffmpeg unavailable")
	}
	dir := t.TempDir()
	sb, e := sandbox.NewPathSandbox(dir)
	if e != nil {
		t.Fatal(e)
	}
	database, e := db.Open(filepath.Join(dir, "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { database.Close() })
	p, e := sb.Resolve("track.mp3", false)
	if e != nil {
		t.Fatal(e)
	}
	cmd := mediaexec.CommandContext(context.Background(), "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=4", "-codec:a", "libmp3lame", "-y", p)
	if e = cmd.Run(); e != nil {
		t.Fatal(e)
	}
	lrc, e := sb.Resolve("track.lrc", false)
	if e != nil {
		t.Fatal(e)
	}
	if e = safefs.WriteFile(lrc, []byte("[00:00]тут лежит закладка под сосной"), 0600); e != nil {
		t.Fatal(e)
	}
	return database, sb
}
func awaitCensorState(t *testing.T, a *CensorshipAudit, id, state string) {
	t.Helper()
	end := time.Now().Add(8 * time.Second)
	for time.Now().Before(end) {
		var got string
		if e := a.db.QueryRow("SELECT state FROM censorship_runs WHERE id=?", id).Scan(&got); e == nil && got == state {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run did not reach %s", state)
}
func TestCensorshipQueueEvidenceCacheAndReadOnly(t *testing.T) {
	database, sb := auditFixture(t)
	cloud := &testTranscriber{}
	a, e := NewCensorshipAudit(database.Conn(), sb, map[string]censorcheck.Transcriber{"gemini": cloud}, 1200)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	full, _ := sb.Resolve("track.mp3", false)
	before, _ := safefs.ReadFile(full)
	id, e := a.Start(context.Background(), CensorshipOptions{Paths: []string{"track.mp3"}, Provider: "gemini"})
	if e != nil {
		t.Fatal(e)
	}
	awaitCensorState(t, a, id, "completed")
	items, e := a.Items(context.Background(), id, "", 100, 0)
	if e != nil || len(items) != 1 || items[0].Verdict != "checked_words_preserved" {
		t.Fatalf("items=%+v err=%v", items, e)
	}
	if cloud.calls.Load() != 1 {
		t.Fatalf("cloud calls %d", cloud.calls.Load())
	}
	after, _ := safefs.ReadFile(full)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("audit modified audio")
	}
	if e = a.Decide(context.Background(), items[0].ID, "confirmed_censored", "manual fixture"); e != nil {
		t.Fatal(e)
	}
	// Changing ID3 must neither invalidate the audio digest nor repeat transcription.
	if e = id3.WriteTags(full, id3.TrackTags{Title: "New title", Artist: "Fixture"}, nil); e != nil {
		t.Fatal(e)
	}
	id, e = a.Start(context.Background(), CensorshipOptions{Paths: []string{"track.mp3"}, Provider: "gemini"})
	if e != nil {
		t.Fatal(e)
	}
	awaitCensorState(t, a, id, "completed")
	if cloud.calls.Load() != 1 {
		t.Fatalf("tags repeated cloud call: %d", cloud.calls.Load())
	}
	if _, e = database.Conn().Exec(`UPDATE censorship_cache SET result_json=json_remove(result_json,'$.audio_hash')`); e != nil {
		t.Fatal(e)
	}
	id, e = a.Start(context.Background(), CensorshipOptions{Paths: []string{"track.mp3"}, Provider: "gemini"})
	if e != nil {
		t.Fatal(e)
	}
	awaitCensorState(t, a, id, "completed")
	fresh, e := a.Items(context.Background(), id, "", 100, 0)
	if e != nil || len(fresh) != 1 || fresh[0].Result.AudioHash == "" || cloud.calls.Load() != 1 {
		t.Fatalf("legacy result cache was not rebuilt from audio and cached transcription: %+v err=%v calls=%d", fresh, e, cloud.calls.Load())
	}
	if _, e = a.Start(context.Background(), CensorshipOptions{Paths: []string{"../../escape.mp3"}}); e == nil {
		t.Fatal("path traversal accepted")
	}
	// An existing symlink outside the sandbox must also be rejected.
	if e = os.Symlink("/etc/passwd", filepath.Join(sb.Root(), "escape.mp3")); e != nil {
		t.Fatal(e)
	}
	if _, e = a.Start(context.Background(), CensorshipOptions{Paths: []string{"escape.mp3"}}); e == nil {
		t.Fatal("symlink accepted")
	}
}
func TestCensorshipPauseResumeAndQuota(t *testing.T) {
	database, sb := auditFixture(t)
	cloud := &testTranscriber{entered: make(chan struct{}, 1)}
	cloud.block.Store(true)
	a, e := NewCensorshipAudit(database.Conn(), sb, map[string]censorcheck.Transcriber{"gemini": cloud}, 1200)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	id, e := a.Start(context.Background(), CensorshipOptions{Paths: []string{"track.mp3"}, Provider: "gemini"})
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-cloud.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("cloud not reached")
	}
	if e = a.Control(context.Background(), id, "pause"); e != nil {
		t.Fatal(e)
	}
	awaitCensorState(t, a, id, "paused")
	cloud.block.Store(false)
	if e = a.Control(context.Background(), id, "resume"); e != nil {
		t.Fatal(e)
	}
	awaitCensorState(t, a, id, "completed")
	if e = a.Control(context.Background(), id, "pause"); !errors.Is(e, ErrAuditState) {
		t.Fatalf("completed job paused: %v", e)
	}
	// Different content/model without budget must wait before any external call.
	_, e = database.Conn().Exec("DELETE FROM censorship_cache; DELETE FROM censorship_transcripts; UPDATE censorship_cloud_usage SET seconds=1200")
	if e != nil {
		t.Fatal(e)
	}
	before := cloud.calls.Load()
	id, e = a.Start(context.Background(), CensorshipOptions{Paths: []string{"track.mp3"}, Provider: "gemini"})
	if e != nil {
		t.Fatal(e)
	}
	awaitCensorState(t, a, id, "waiting")
	if cloud.calls.Load() != before {
		t.Fatal("cloud called beyond local limit")
	}
	if e = a.Control(context.Background(), id, "cancel"); e != nil {
		t.Fatal(e)
	}
	awaitCensorState(t, a, id, "cancelled")
}
func TestWordComparisonIsNotCensorshipProof(t *testing.T) {
	for _, tc := range []struct{ expected, heard, want string }{{"тут лежит закладка под сосной", "тут лежит закладка под сосной", "words_preserved"}, {"тут лежит закладка под сосной", "тут лежит подарок под сосной", "possible_word_change"}, {"тут лежит закладка под сосной", "", "unresolved_transcript"}, {"тут лежит закладка под сосной", "совсем другая песня", "unresolved_transcript"}} {
		if got := compareWords(tc.expected, tc.heard); got != tc.want {
			t.Fatalf("got %s want %s", got, tc.want)
		}
	}
}

func TestCensorshipRestartRecoversLocalButWaitsForCloud(t *testing.T) {
	database, sb := auditFixture(t)
	for _, q := range []string{
		`INSERT INTO censorship_runs(id,state,provider,options_json) VALUES('local-restart','running','local','{}'),('cloud-restart','running','gemini','{}')`,
		`INSERT INTO censorship_items(run_id,file_path,state,attempt_token) VALUES('local-restart','track.mp3','running','old'),('cloud-restart','track.mp3','running','old')`,
	} {
		if _, e := database.Conn().Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	a, e := NewCensorshipAudit(database.Conn(), sb, nil, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	awaitCensorState(t, a, "local-restart", "completed")
	awaitCensorState(t, a, "cloud-restart", "waiting")
	items, e := a.Items(context.Background(), "cloud-restart", "", 100, 0)
	if e != nil || len(items) != 1 || items[0].State != "queued" {
		t.Fatalf("cloud auto-retried after restart: %+v %v", items, e)
	}
}

func TestReferenceComparisonRejectsUniformMasteringChange(t *testing.T) {
	a, b := make([]byte, 16000), make([]byte, 16000)
	for i := 0; i < len(a); i += 2 {
		v := int16(1000 + (i/1600)*500)
		a[i] = byte(v)
		a[i+1] = byte(uint16(v) >> 8)
		v *= 2
		b[i] = byte(v)
		b[i+1] = byte(uint16(v) >> 8)
	}
	if alignedDifference(a, b) {
		t.Fatal("uniform gain treated as an edit")
	}
}
