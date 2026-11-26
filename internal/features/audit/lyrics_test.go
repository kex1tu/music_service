package audit_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"music-manager/internal/features/audit"
	"music-manager/internal/services/lyrics"
)

type batchLyricsRepo struct{ audit.AuditRepository }

func (batchLyricsRepo) GetTracksForLyricsFetch(context.Context) ([]audit.TrackCandidateDTO, error) {
	return []audit.TrackCandidateDTO{{Artist: "Artist", Title: "Song", FilePath: "song.mp3"}}, nil
}

func (batchLyricsRepo) SetTrackLyricsStatusAndResolve(context.Context, string, string) error {
	return nil
}

type gatedBatchLyrics struct {
	started chan chan struct{}
	done    <-chan struct{}
}

func (m gatedBatchLyrics) SearchLyrics(ctx context.Context, _, _, _ string, _ int) (*lyrics.LyricsSearchResult, error) {
	release := make(chan struct{})
	select {
	case m.started <- release:
	case <-m.done:
		return nil, context.Canceled
	}

	// Even after cancellation, control when the first lookup returns.
	select {
	case <-release:
	case <-m.done:
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &lyrics.LyricsSearchResult{Lyrics: "line"}, nil
}

func (gatedBatchLyrics) SaveLyrics(string, string, string) error { return nil }

func TestBatchLyricsImmediateStartAfterCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		done := make(chan struct{})
		started := make(chan chan struct{}, 2)
		service := audit.NewService(audit.ServiceConfig{
			Repo:   batchLyricsRepo{},
			Lyrics: gatedBatchLyrics{started: started, done: done},
		})
		defer func() {
			close(done)
			if err := service.Close(); err != nil {
				t.Error(err)
			}
		}()

		start := func() chan struct{} {
			t.Helper()
			if _, err := service.StartBatchLyrics(t.Context()); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			select {
			case release := <-started:
				return release
			default:
				t.Fatal("worker did not start its lyrics lookup")
				return nil
			}
		}

		first := start()
		if !service.CancelBatchLyrics() {
			t.Fatal("first worker was not cancelled")
		}
		second := start()
		want := audit.BatchProgress{IsRunning: true, Total: 1, Current: 1, CurrentTrack: "Artist - Song"}

		close(first)
		// Advance the fake clock past the worker's per-track pacing delay.
		time.Sleep(time.Second)
		synctest.Wait()
		if got := service.GetBatchLyricsProgress(); got != want {
			t.Fatalf("first worker changed the second worker's progress: got %+v, want %+v", got, want)
		}

		close(second)
		time.Sleep(time.Second)
		synctest.Wait()
		want.IsRunning = false
		want.CurrentTrack = ""
		want.Found = 1
		if got := service.GetBatchLyricsProgress(); got != want {
			t.Fatalf("second worker completed with progress %+v, want %+v", got, want)
		}
	})
}
