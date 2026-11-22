package organizer_test

import (
	"bytes"
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

func TestRedownloadPreservesOriginalEditedAfterReceipt(t *testing.T) {
	env := testkit.NewEnv(t)
	const destination = "Album/01. Track.mp3"
	const staged = "job/converted/01. Track.mp3"
	tags := id3.TrackTags{Title: "Track", Artist: "Band", Album: "Album", TrackNumber: 1}
	full := env.CreateDummyMP3WithTags(destination, tags, nil)
	originalHash, err := organizer.FileSHA256(full)
	if err != nil {
		t.Fatal(err)
	}
	tags.SourceURL = "https://soundcloud.com/band/replacement"
	fullStage, err := testkit.CreateDummyMP3WithTags(env.TmpSandbox, staged, tags, nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeStage, err := safefs.ReadFile(fullStage)
	if err != nil {
		t.Fatal(err)
	}

	var edited []byte
	service := organizer.NewService(env.MusicSandbox, env.TmpSandbox)
	result, err := service.ApplyRedownload(t.Context(), organizer.RedownloadImport{
		StagedPath: staged, OriginalPath: destination, OriginalHash: originalHash,
	}, func() error { return nil }, func(path, hash string) error {
		if path != destination || hash == "" {
			t.Fatalf("incorrect receipt: path=%q hash=%q", path, hash)
		}
		tags.Title, tags.SourceURL = "Edited while confirming", ""
		if err := id3.WriteTags(full, tags, nil); err != nil {
			return err
		}
		var err error
		edited, err = safefs.ReadFile(full)
		return err
	})
	if len(edited) == 0 || result != "" || err == nil || !strings.Contains(err.Error(), "исходный файл изменился во время замены") {
		t.Fatalf("expected conflict after saving receipt, got %q, %v", result, err)
	}
	if got, err := safefs.ReadFile(full); err != nil || !bytes.Equal(got, edited) {
		t.Fatalf("confirmation overwrote the edited original: %v", err)
	}
	if got, err := safefs.ReadFile(fullStage); err != nil || !bytes.Equal(got, beforeStage) {
		t.Fatalf("confirmation changed the staged file: %v", err)
	}
}
