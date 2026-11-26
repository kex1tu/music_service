package audit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"music-manager/internal/core/mediaexec"
	"music-manager/internal/core/safefs"
	"music-manager/internal/id3"
)

func TestCensorshipAudioRejectsReplacedTrack(t *testing.T) {
	database, sb := auditFixture(t)
	ctx := context.Background()
	a, err := NewCensorshipAudit(database.Conn(), sb, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	full, err := sb.Resolve("track.mp3", false)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := sb.Resolve("reference.mp3", false)
	if err != nil {
		t.Fatal(err)
	}
	writeAudioFixture(t, ref, 660)
	tags := id3.TrackTags{Title: "Fixture recording", Artist: "Fixture artist"}
	for _, file := range []string{full, ref} {
		if err := id3.WriteTags(file, tags, nil); err != nil {
			t.Fatal(err)
		}
	}
	options := CensorshipOptions{Paths: []string{"track.mp3"}, References: map[string]string{"track.mp3": "reference.mp3"}}
	run, err := a.Start(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	awaitCensorState(t, a, run, "completed")
	items, err := a.Items(ctx, run, "", 100, 0)
	if err != nil || len(items) != 1 || items[0].State != "done" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	item := items[0]
	if item.Result.AudioHash == "" || item.Result.ReferenceAudioHash == "" || item.Result.AudioHash == item.Result.ReferenceAudioHash {
		t.Fatalf("missing or incorrect evidence digests: %+v", item.Result)
	}
	if err := a.Decide(ctx, item.ID, "uncertain", "historical note"); err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	NewModule(&Service{censorship: a}).registerCensorshipRoutes(router)
	url := fmt.Sprintf("/censorship-items/%d/audio", item.ID)
	listen := func(method, params string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, url+params, nil)
		if method == http.MethodGet {
			req.Header.Set("Range", "bytes=0-127")
		}
		router.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := listen(http.MethodGet, ""); got != http.StatusPartialContent {
		t.Fatalf("original audio: HTTP %d", got)
	}
	if got := listen(http.MethodHead, "?reference=true"); got != http.StatusOK {
		t.Fatalf("original reference: HTTP %d", got)
	}
	tags.Title = "A metadata-only change"
	if err := id3.WriteTags(full, tags, nil); err != nil {
		t.Fatal(err)
	}
	if got := listen(http.MethodGet, ""); got != http.StatusPartialContent {
		t.Fatalf("same audio with new tags: HTTP %d", got)
	}

	writeAudioFixture(t, ref, 990)
	if got := listen(http.MethodGet, "?reference=true"); got != http.StatusConflict {
		t.Errorf("replaced reference: HTTP %d", got)
	}
	if got := listen(http.MethodGet, ""); got != http.StatusPartialContent {
		t.Errorf("replaced reference blocked the original track: HTTP %d", got)
	}
	writeAudioFixture(t, full, 880)
	if got := listen(http.MethodGet, ""); got != http.StatusConflict {
		t.Errorf("replaced audio: HTTP %d", got)
	}
	newRun, err := a.Start(ctx, CensorshipOptions{Paths: []string{"track.mp3"}})
	if err != nil {
		t.Fatal(err)
	}
	awaitCensorState(t, a, newRun, "completed")
	replacement, err := a.Items(ctx, newRun, "", 100, 0)
	if err != nil || len(replacement) != 1 || replacement[0].Result.AudioHash == "" || replacement[0].Result.AudioHash == item.Result.AudioHash || replacement[0].Note != "" {
		t.Fatalf("replacement was not analyzed separately: %+v err=%v", replacement, err)
	}
	items, err = a.Items(ctx, run, "", 100, 0)
	if err != nil || len(items) != 1 || items[0].Verdict != item.Verdict || items[0].Decision != "uncertain" || items[0].Note != "historical note" {
		t.Fatalf("historical result changed: %+v err=%v", items, err)
	}

	// Historical rows lack digests. The endpoint must reject them without
	// adopting the replacement's digest or changing the decision.
	if _, err := database.Conn().Exec(`UPDATE censorship_items SET result_json=json_remove(result_json,'$.audio_hash','$.reference_audio_hash') WHERE id=?`, item.ID); err != nil {
		t.Fatal(err)
	}
	if got := listen(http.MethodHead, ""); got != http.StatusConflict {
		t.Errorf("legacy evidence: HTTP %d", got)
	}
}

func TestCensorshipAudioRelinkChecksBothRoles(t *testing.T) {
	database, sb := auditFixture(t)
	ctx := t.Context()
	audit, err := NewCensorshipAudit(database.Conn(), sb, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer audit.Close()

	resolve := func(path string) string {
		t.Helper()
		full, err := sb.Resolve(path, false)
		if err != nil {
			t.Fatal(err)
		}
		return full
	}
	const target = "track.mp3"
	const reference = "reference.mp3"
	const renamedTarget = "renamed track.mp3"
	const renamedReference = "renamed reference.mp3"
	writeAudioFixture(t, resolve(reference), 660)
	for _, path := range []string{target, reference} {
		if err := id3.WriteTags(resolve(path), id3.TrackTags{Title: "Recording", Artist: "Artist"}, nil); err != nil {
			t.Fatal(err)
		}
	}

	run, err := audit.Start(ctx, CensorshipOptions{
		Paths: []string{target}, References: map[string]string{target: reference},
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitCensorState(t, audit, run, "completed")
	readItem := func(t *testing.T) CensorshipItem {
		t.Helper()
		items, err := audit.Items(ctx, run, "", 100, 0)
		if err != nil || len(items) != 1 || items[0].State != "done" {
			t.Fatalf("items=%+v err=%v", items, err)
		}
		return items[0]
	}
	item := readItem(t)
	if item.Result.AudioHash == "" || item.Result.ReferenceAudioHash == "" || item.Result.AudioHash == item.Result.ReferenceAudioHash {
		t.Fatal("fixture must contain two different, verified audio recordings")
	}
	if err := audit.Decide(ctx, item.ID, "uncertain", "historical note"); err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	NewModule(&Service{censorship: audit}).registerCensorshipRoutes(router)
	url := fmt.Sprintf("/censorship-items/%d/relink", item.ID)
	currentTarget, currentReference := target, reference
	for _, tc := range []struct{ role, original, renamed, wrong string }{
		{"target", target, renamedTarget, reference},
		{"reference", reference, renamedReference, renamedTarget},
	} {
		t.Run(tc.role, func(t *testing.T) {
			if err := safefs.Rename(resolve(tc.original), resolve(tc.renamed)); err != nil {
				t.Fatal(err)
			}
			if err := id3.WriteTags(resolve(tc.renamed), id3.TrackTags{Title: "Changed tags"}, nil); err != nil {
				t.Fatal(err)
			}
			relink := func(path string, want int) {
				t.Helper()
				body := strings.NewReader(fmt.Sprintf(`{"role":%q,"path":%q}`, tc.role, path))
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, url, body))
				if rec.Code != want {
					t.Fatalf("relink %s to %q: HTTP %d, want %d: %s", tc.role, path, rec.Code, want, rec.Body)
				}
			}
			relink(tc.renamed, http.StatusOK)
			if tc.role == "target" {
				currentTarget = tc.renamed
			} else {
				currentReference = tc.renamed
			}
			assertBinding := func() {
				t.Helper()
				got := readItem(t)
				if got.Path != target || got.Reference != reference || got.CurrentPath != currentTarget ||
					got.CurrentReference != currentReference || got.Decision != "uncertain" ||
					got.Note != "historical note" || !got.DecisionVerified || got.Verdict != item.Verdict {
					t.Fatalf("unexpected binding or history: %+v", got)
				}
			}
			assertBinding()
			relink(tc.wrong, http.StatusConflict)
			assertBinding()
		})
	}
}

func writeAudioFixture(t *testing.T, fullPath string, frequency int) {
	t.Helper()
	input := fmt.Sprintf("sine=frequency=%d:duration=4", frequency)
	cmd := mediaexec.CommandContext(context.Background(), "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", input, "-codec:a", "libmp3lame", "-y", fullPath)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}
