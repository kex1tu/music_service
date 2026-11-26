package audit

import (
	"context"
	"fmt"
	"music-manager/internal/services/lyrics"
	"time"
)

func (s *Service) GetMissingLyrics(ctx context.Context, limit int) ([]MissingLyricsItem, error) {
	return s.repo.GetTracksMissingLyrics(ctx, limit)
}

func (s *Service) StartBatchLyrics(ctx context.Context) (BatchProgress, error) {
	s.lyricsMu.Lock()
	if err := s.ctx.Err(); err != nil {
		s.lyricsMu.Unlock()
		return BatchProgress{}, err
	}
	if s.lyricsState.IsRunning {
		state := *s.lyricsState
		s.lyricsMu.Unlock()
		return state, ErrBatchAlreadyRunning
	}

	tracks, err := s.repo.GetTracksForLyricsFetch(ctx)
	if err != nil {
		s.lyricsMu.Unlock()
		return BatchProgress{}, err
	}

	if len(tracks) == 0 {
		s.lyricsMu.Unlock()
		return BatchProgress{IsRunning: false}, nil
	}

	workerCtx, cancel := context.WithCancel(s.ctx)
	// Each run owns its progress so a cancelled worker can unwind after a restart.
	progress := &BatchProgress{IsRunning: true, Total: len(tracks)}
	s.lyricsState = progress
	s.lyricsCancel = cancel
	s.wg.Add(1)
	s.lyricsMu.Unlock()
	go func() {
		defer s.wg.Done()
		defer func() {
			cancel()
			s.lyricsMu.Lock()
			progress.IsRunning = false
			progress.CurrentTrack = ""
			if s.lyricsState == progress {
				s.lyricsCancel = nil
			}
			s.lyricsMu.Unlock()
		}()

		for i, tr := range tracks {
			select {
			case <-workerCtx.Done():
				return
			default:
			}

			s.lyricsMu.Lock()
			progress.Current = i + 1
			progress.CurrentTrack = fmt.Sprintf("%s - %s", tr.Artist, tr.Title)
			s.lyricsMu.Unlock()

			if s.lyrics != nil {
				res, err := s.lyrics.SearchLyrics(workerCtx, tr.Artist, tr.Title, tr.Album, tr.Duration)
				if workerCtx.Err() != nil {
					return
				}
				found := err == nil && res != nil && res.Lyrics != "" &&
					s.saveBatchTrackLyrics(workerCtx, tr.FilePath, res) == nil
				s.lyricsMu.Lock()
				if found {
					progress.Found++
				} else {
					progress.Failed++
				}
				s.lyricsMu.Unlock()
			}

			select {
			case <-workerCtx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()

	s.lyricsMu.RLock()
	defer s.lyricsMu.RUnlock()
	return *progress, nil
}

func (s *Service) GetBatchLyricsProgress() BatchProgress {
	s.lyricsMu.RLock()
	defer s.lyricsMu.RUnlock()
	return *s.lyricsState
}

func (s *Service) CancelBatchLyrics() bool {
	s.lyricsMu.Lock()
	defer s.lyricsMu.Unlock()
	if s.lyricsCancel != nil && s.lyricsState.IsRunning {
		s.lyricsCancel()
		s.lyricsState.IsRunning = false
		s.lyricsState.CurrentTrack = ""
		return true
	}
	return false
}

func (s *Service) saveBatchTrackLyrics(ctx context.Context, path string, result *lyrics.LyricsSearchResult) error {
	save := func() error { return s.lyrics.SaveLyrics(path, result.Lyrics, "auto") }
	if s.trackIndex != nil {
		full, err := s.musicSandbox.Resolve(path, false)
		if err != nil {
			return err
		}
		return s.trackIndex.ChangeTrack(ctx, full, full, false, save)
	}
	if err := save(); err != nil {
		return err
	}
	kind := "txt"
	if result.Synced {
		kind = "lrc"
	}
	return s.repo.SetTrackLyricsStatusAndResolve(ctx, path, kind)
}
