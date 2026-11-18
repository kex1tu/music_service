package audit

import (
	"context"
	"fmt"
	"time"
)

func (s *Service) GetMissingLyrics(ctx context.Context, limit int) ([]MissingLyricsItem, error) {
	return s.repo.GetTracksMissingLyrics(ctx, limit)
}

func (s *Service) StartBatchLyrics(ctx context.Context) (BatchProgress, error) {
	s.lyricsMu.Lock()
	if s.lyricsState.IsRunning {
		state := s.lyricsState
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
	s.lyricsCancel = cancel
	s.lyricsState = BatchProgress{
		IsRunning: true,
		Total:     len(tracks),
		Current:   0,
		Found:     0,
		Failed:    0,
	}
	s.lyricsMu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.lyricsMu.Lock()
			s.lyricsState.IsRunning = false
			s.lyricsState.CurrentTrack = ""
			s.lyricsMu.Unlock()
		}()

		for i, tr := range tracks {
			select {
			case <-workerCtx.Done():
				return
			default:
			}

			s.lyricsMu.Lock()
			s.lyricsState.Current = i + 1
			s.lyricsState.CurrentTrack = fmt.Sprintf("%s - %s", tr.Artist, tr.Title)
			s.lyricsMu.Unlock()

			if s.lyrics != nil {
				res, err := s.lyrics.SearchLyrics(workerCtx, tr.Artist, tr.Title, tr.Album, tr.Duration)
				if err == nil && res != nil && res.Lyrics != "" {
					saveErr := s.lyrics.SaveLyrics(tr.FilePath, res.Lyrics, "auto")
					if saveErr == nil {
						kind := "txt"
						if res.Synced {
							kind = "lrc"
						}
						_ = s.repo.SetTrackLyricsStatusAndResolve(workerCtx, tr.FilePath, kind)

						s.lyricsMu.Lock()
						s.lyricsState.Found++
						s.lyricsMu.Unlock()
					} else {
						s.lyricsMu.Lock()
						s.lyricsState.Failed++
						s.lyricsMu.Unlock()
					}
				} else {
					s.lyricsMu.Lock()
					s.lyricsState.Failed++
					s.lyricsMu.Unlock()
				}
			}

			time.Sleep(100 * time.Millisecond)
		}
	}()

	s.lyricsMu.RLock()
	defer s.lyricsMu.RUnlock()
	return s.lyricsState, nil
}

func (s *Service) GetBatchLyricsProgress() BatchProgress {
	s.lyricsMu.RLock()
	defer s.lyricsMu.RUnlock()
	return s.lyricsState
}

func (s *Service) CancelBatchLyrics() bool {
	s.lyricsMu.Lock()
	defer s.lyricsMu.Unlock()
	if s.lyricsCancel != nil && s.lyricsState.IsRunning {
		s.lyricsCancel()
		s.lyricsState.IsRunning = false
		return true
	}
	return false
}
