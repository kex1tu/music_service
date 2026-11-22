package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/db"
	"music-manager/internal/services/censorcheck"
)

func TestCensorshipRepairsTranscriptAndReusesLegacy(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := t.Context()
	cloud := &testTranscriber{}
	a := &CensorshipAudit{db: database.Conn(), clouds: map[string]censorcheck.Transcriber{"fixture": cloud}, dailySeconds: 10}
	pcm := make([]byte, censorcheck.SampleRate*2)
	keyFor := func(pcm []byte, legacy bool) string {
		hash := sha256.Sum256(pcm)
		audio := hex.EncodeToString(hash[:])
		if legacy {
			return digest(audio, cloud.Identity(), "censorship-v1")
		}
		return digest("transcript-v1", audio, cloud.Identity())
	}
	insert := func(key, raw string) {
		t.Helper()
		if _, err := a.db.ExecContext(ctx, "INSERT INTO censorship_transcripts VALUES(?,?,CURRENT_TIMESTAMP)", key, raw); err != nil {
			t.Fatal(err)
		}
	}
	insert(keyFor(pcm, false), `{"text":123}`)
	for range 2 {
		if tr, err := a.transcribe(ctx, "fixture", pcm); err != nil || tr.Text == "" {
			t.Fatalf("transcription = %+v, err = %v", tr, err)
		}
	}
	pcm[0] = 1
	insert(keyFor(pcm, false), `{}`)
	insert(keyFor(pcm, true), `{"text":"","provider":"fixture","model":"fixture","settings":"fixture:verbatim-v1"}`)
	for range 2 {
		if tr, err := a.transcribe(ctx, "fixture", pcm); err != nil || tr.Text != "" || tr.Provider != "fixture" {
			t.Fatalf("legacy transcript = %+v, err = %v", tr, err)
		}
	}
	quota, err := a.quota(ctx, []string{"fixture"}, time.Now())
	if err != nil || len(quota) != 1 || quota[0].Reserved != 1 || quota[0].Remaining != 9 || cloud.calls.Load() != 1 {
		t.Fatalf("quota = %+v, calls = %d, err = %v", quota, cloud.calls.Load(), err)
	}
}

func TestCensorshipHistoryAndRetention(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := t.Context()
	a := &CensorshipAudit{db: database.Conn()}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := a.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 60; i++ {
		exec("INSERT INTO censorship_runs(id,options_json,created_at) VALUES (?,'{}','2026-11-20 12:00:00')", fmt.Sprintf("run-%02d", i))
	}
	runs, cursor, err := a.Runs(ctx, 25, "")
	if err != nil || len(runs) != 25 || cursor == "" {
		t.Fatalf("first page = %d, cursor = %q, err = %v", len(runs), cursor, err)
	}
	exec("INSERT INTO censorship_runs(id,options_json,created_at) VALUES ('new','{}','2026-11-21 12:00:00')")
	for cursor != "" {
		page, next, err := a.Runs(ctx, 25, cursor)
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, page...)
		cursor = next
	}
	if len(runs) != 60 {
		t.Fatalf("run count = %d, want 60", len(runs))
	}
	for i, run := range runs {
		if run.ID != fmt.Sprintf("run-%02d", 59-i) {
			t.Fatalf("run %d = %s", i, run.ID)
		}
	}
	if run, err := a.Run(ctx, "run-00"); err != nil || run.ID != "run-00" {
		t.Fatalf("previous run = %+v, err = %v", run, err)
	}
	exec(`INSERT INTO censorship_items(id,run_id,file_path,state,audio_hash)
		VALUES (1,'run-00','old.mp3','done','same'),(2,'run-01','new.mp3','done','same'),
		       (3,'run-01','other.mp3','queued','same')`)
	if err := a.Decide(ctx, 1, "cleared", "checked"); err != nil {
		t.Fatal(err)
	}
	if err := a.Decide(ctx, 3, "cleared", "too early"); !errors.Is(err, ErrAuditState) {
		t.Fatalf("accepted early decision: %v", err)
	}
	note := "before analysis"
	if err := a.saveDecision(ctx, 3, nil, &note); err != nil {
		t.Fatal(err)
	}
	items, err := a.Items(ctx, "run-01", "", 100, 0)
	if err != nil || len(items) != 2 || items[0].Decision != "" || items[0].PreviousDecision == nil ||
		items[0].PreviousDecision.RunID != "run-00" || items[1].Note != note {
		t.Fatalf("history = %+v, err = %v", items, err)
	}

	now := time.Now()
	cutoff := now.Add(-90 * 24 * time.Hour).Unix()
	exec("INSERT INTO censorship_cache(cache_key,result_json,last_used_at) VALUES ('expired','{}',?),('keep','{}',?)", cutoff-1, cutoff)
	exec("INSERT INTO censorship_transcripts(cache_key,transcript_json) VALUES ('paid','{}')")
	exec("INSERT INTO censorship_cloud_usage(provider,day,seconds) VALUES ('fixture','2025-01-01',1)")
	if err := a.pruneCensorshipCache(ctx, now); err != nil {
		t.Fatal(err)
	}
	var results, paid, usage, history int
	err = a.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM censorship_cache WHERE cache_key='keep'),
		(SELECT count(*) FROM censorship_transcripts),(SELECT count(*) FROM censorship_cloud_usage),
		(SELECT count(*) FROM censorship_items WHERE manual_note!='')`).Scan(&results, &paid, &usage, &history)
	if err != nil || results != 1 || paid != 1 || usage != 1 || history != 2 {
		t.Fatalf("after cleanup: results=%d paid=%d usage=%d notes=%d, err = %v", results, paid, usage, history, err)
	}
	if err := a.db.QueryRowContext(ctx, "SELECT count(*) FROM censorship_cache").Scan(&results); err != nil || results != 1 {
		t.Fatalf("expired result still present: %d, %v", results, err)
	}
}

func TestCensorshipNormalizesReferenceAndFinishesRunIndependently(t *testing.T) {
	database, sb := auditFixture(t)
	ctx := t.Context()
	a := &CensorshipAudit{db: database.Conn(), sb: sb, ctx: ctx, wake: make(chan struct{}, 1)}
	first, err := a.Start(ctx, CensorshipOptions{
		Paths: []string{"./track.mp3"}, References: map[string]string{"./track.mp3": "./track.mp3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Start(ctx, CensorshipOptions{
		Paths: []string{"track.mp3"}, References: map[string]string{"wrong.mp3": "track.mp3"},
	}); !errors.Is(err, errCensorshipInput) {
		t.Fatalf("unmatched reference: %v", err)
	}
	second, err := a.Start(ctx, CensorshipOptions{Paths: []string{"track.mp3"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, "UPDATE censorship_runs SET created_at='2000-01-01 00:00:00' WHERE id=?", first); err != nil {
		t.Fatal(err)
	}
	items, err := a.Items(ctx, first, "", 100, 0)
	if err != nil || len(items) != 1 || items[0].Reference != "track.mp3" {
		t.Fatalf("normalized items = %+v, err = %v", items, err)
	}
	worked, err := a.step()
	if err != nil || !worked {
		t.Fatalf("step = %v, err = %v", worked, err)
	}
	for run, state := range map[string]string{first: "completed", second: "running"} {
		got, err := a.Run(ctx, run)
		if err != nil || got.State != state {
			t.Fatalf("run %s: got %+v, want %s, err = %v", run, got, state, err)
		}
	}

	if _, err := a.db.ExecContext(ctx, `UPDATE censorship_items SET state='error',verdict='suspected',
		result_json='{"evidence":[{"kind":"possible_mute"}]}',manual_note='keep' WHERE run_id=?`, first); err != nil {
		t.Fatal(err)
	}
	if err := a.Control(ctx, first, "retry"); err != nil {
		t.Fatal(err)
	}
	items, err = a.Items(ctx, first, "not_checked", 100, 0)
	if err != nil || len(items) != 1 || items[0].State != "queued" || len(items[0].Result.Evidence) != 0 || items[0].Note != "keep" {
		t.Fatalf("retry items = %+v, err = %v", items, err)
	}
}

func TestCensorshipWindowsUseAudioDurationInsteadOfTLEN(t *testing.T) {
	database, sb := auditFixture(t)
	ctx := t.Context()
	cloud := &testTranscriber{}
	a := &CensorshipAudit{db: database.Conn(), sb: sb, dailySeconds: 60, clouds: map[string]censorcheck.Transcriber{"fixture": cloud}}
	track, err := sb.Resolve("track.mp3", false)
	if err != nil {
		t.Fatal(err)
	}
	lyrics, err := sb.Resolve("track.lrc", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := safefs.WriteFile(lyrics, []byte("[00:05]тут лежит закладка под сосной"), 0600); err != nil {
		t.Fatal(err)
	}
	audio, err := safefs.ReadFile(track)
	if err != nil {
		t.Fatal(err)
	}
	if len(audio) >= 10 && string(audio[:3]) == "ID3" {
		size := 0
		for _, b := range audio[6:10] {
			size = size*128 + int(b)
		}
		audio = audio[10+size:]
	}
	for _, tlen := range []string{"500", "4000"} {
		frame := append([]byte("TLEN"), 0, 0, 0, byte(len(tlen)+1), 0, 0, 3)
		frame = append(frame, tlen...)
		header := []byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, byte(len(frame))}
		if err := safefs.WriteFile(track, append(append(header, frame...), audio...), 0600); err != nil {
			t.Fatal(err)
		}
		result, err := a.analyze(ctx, "track.mp3", "", "fixture")
		if err != nil || result.TotalWindows != 1 || result.Verdict != "checked_words_preserved" {
			t.Fatalf("TLEN=%s: result=%+v, err=%v", tlen, result, err)
		}
	}
	var results int
	err = a.db.QueryRowContext(ctx, "SELECT count(*) FROM censorship_cache").Scan(&results)
	if err != nil || results != 1 || cloud.calls.Load() != 1 {
		t.Fatalf("results=%d, calls=%d, err=%v", results, cloud.calls.Load(), err)
	}
}
