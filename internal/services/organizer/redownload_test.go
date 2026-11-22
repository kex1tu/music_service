package organizer_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"music-manager/internal/core/safefs"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
	"music-manager/internal/testkit"
)

func TestRedownloadResumesCompanionImport(t *testing.T) {
	const lyrics = "[00:01.00]saved lyrics"
	const notes = "plain text notes"
	for _, tc := range []struct {
		name, stagedLyrics, publishedLyrics string
		wantConflict                        bool
	}{
		{name: "audio only", stagedLyrics: lyrics},
		{name: "one companion published", publishedLyrics: lyrics},
		{name: "one companion copied", stagedLyrics: lyrics, publishedLyrics: lyrics},
		{name: "conflicting companion", stagedLyrics: lyrics, publishedLyrics: "different lyrics", wantConflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testkit.NewEnv(t)
			const staged = "job/converted/01. Track.mp3"
			const dest = "Album/01. Track.mp3"
			tags := id3.TrackTags{Title: "Track", Artist: "Band", Album: "Album", TrackNumber: 1}
			fullStage, err := testkit.CreateDummyMP3WithTags(env.TmpSandbox, staged, tags, nil)
			if err != nil {
				t.Fatal(err)
			}
			fullDest := env.CreateDummyMP3WithTags(dest, tags, nil)
			write := func(audio, ext, content string) {
				t.Helper()
				if content != "" {
					if err := safefs.WriteFile(strings.TrimSuffix(audio, ".mp3")+ext, []byte(content), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			check := func(audio, ext, want string) {
				t.Helper()
				data, err := safefs.ReadFile(strings.TrimSuffix(audio, ".mp3") + ext)
				if want == "" && errors.Is(err, os.ErrNotExist) {
					return
				}
				if want == "" || err != nil || string(data) != want {
					t.Fatalf("%s %s = %q, want %q: %v", audio, ext, data, want, err)
				}
			}
			write(fullStage, ".lrc", tc.stagedLyrics)
			write(fullStage, ".txt", notes)
			write(fullDest, ".lrc", tc.publishedLyrics)
			hash, err := organizer.FileSHA256(fullDest)
			if err != nil {
				t.Fatal(err)
			}

			s := organizer.NewService(env.MusicSandbox, env.TmpSandbox)
			unexpected := func() error { return errors.New("the MP3 has already been published") }
			got, err := s.ApplyRedownload(t.Context(), organizer.RedownloadImport{
				StagedPath: staged, ResultPath: dest, ResultHash: hash,
			}, unexpected, func(string, string) error { return unexpected() })
			if tc.wantConflict {
				if err == nil || !strings.Contains(err.Error(), "companion destination already exists") {
					t.Fatalf("expected a sidecar conflict, got %q, %v", got, err)
				}
				check(fullStage, ".lrc", tc.stagedLyrics)
				check(fullStage, ".txt", notes)
				check(fullDest, ".lrc", tc.publishedLyrics)
				return
			}
			if err != nil || got != dest {
				t.Fatalf("retry returned %q, %v", got, err)
			}
			check(fullDest, ".lrc", lyrics)
			check(fullDest, ".txt", notes)
			check(fullStage, ".lrc", "")
			check(fullStage, ".txt", "")
		})
	}
}
