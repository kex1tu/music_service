package audit_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"music-manager/internal/features/audit"
	"music-manager/internal/services/scanner"
)

func TestAudit_AutomaticDuplicateCleanupPreservesRecordingsAndLyrics(t *testing.T) {
	env, _, _ := setupAuditTest(t)
	const keep = "Album/01. Intro"
	const copy = "Album/01. Intro (1)"
	const third = "Album/01. Intro (2)"

	first := env.CreateDummyMP3(keep+".mp3", "Intro", "Artist", "Album")
	second := env.CreateDummyMP3(copy+".mp3", "Intro", "Artist", "Album")
	firstLRC := env.CreateDummyFile(env.MusicSandbox, keep+".lrc", []byte("[00:01] first"))
	secondLRC := env.CreateDummyFile(env.MusicSandbox, copy+".lrc", []byte("[00:02] second"))

	audio, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	different := append([]byte(nil), audio...)
	different[len(different)-1] ^= 1
	if err := os.WriteFile(second, different, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.NewScanner(env.MusicSandbox, env.DB, nil).ScanAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	fix := func(endpoint string, payload any, deleted, review int) {
		t.Helper()
		resp := env.PostJSON(endpoint, payload)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected status %d: %s", resp.StatusCode, env.ReadBody(resp))
		}
		var result audit.FixDuplicatesResponse
		env.DecodeJSON(resp, &result)
		if !result.Success || result.DeletedCount != deleted || result.ReviewGroups != review {
			t.Fatalf("unexpected cleanup result: %+v", result)
		}
	}
	requirePresent := func(paths ...string) {
		t.Helper()
		for _, path := range paths {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("%s must remain: %v", path, err)
			}
		}
	}
	fix("/files/audit/duplicates/fix-all", nil, 0, 1)
	requirePresent(first, second, firstLRC, secondLRC)

	// Even identical MP3s stay together if any pair of sidecars conflicts.
	if err := os.WriteFile(second, audio, 0644); err != nil {
		t.Fatal(err)
	}
	thirdAudio := env.CreateDummyMP3(third+".mp3", "Intro", "Artist", "Album")
	thirdText := env.CreateDummyFile(env.MusicSandbox, third+".txt", []byte("only plain lyrics"))
	if _, err := scanner.NewScanner(env.MusicSandbox, env.DB, nil).ScanAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	group := audit.FixDuplicatesRequest{GroupKey: "Album::intro"}
	fix("/files/audit/duplicates/fix", group, 0, 1)
	requirePresent(first, second, firstLRC, secondLRC, thirdAudio, thirdText)

	if err := os.WriteFile(secondLRC, []byte("[00:01] first"), 0644); err != nil {
		t.Fatal(err)
	}
	fix("/files/audit/duplicates/fix", group, 2, 0)
	requirePresent(first, firstLRC)
	for _, path := range []string{second, secondLRC, thirdAudio, thirdText} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s should be gone: %v", path, err)
		}
	}
	merged, err := os.ReadFile(strings.TrimSuffix(first, ".mp3") + ".txt")
	if err != nil || string(merged) != "only plain lyrics" {
		t.Fatalf("unique text was not preserved: %q, %v", merged, err)
	}
}
