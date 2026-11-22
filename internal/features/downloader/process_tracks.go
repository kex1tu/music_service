package downloader

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/censorcheck"
	"music-manager/internal/services/downloadmedia"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/resolver"
)

func (s *Service) processTracks(
	ctx context.Context,
	job *Job,
	meta *resolver.ReleaseMetadata,
	tracks []resolver.ResolvedTrack,
	rawDir, convertedDir string,
) {
	total := len(tracks)
	if total == 0 {
		job.SetReady(0, 0)
		return
	}

	// Загружаем обложку
	var coverBytes []byte
	if meta.CoverURL != "" {
		if cBytes, err := s.downloadCover(ctx, meta.CoverURL); err == nil && len(cBytes) > 0 {
			coverBytes = cBytes
			if rawDir != "" {
				_ = safefs.WriteFile(filepath.Join(rawDir, "cover.jpg"), cBytes, 0644)
			}
			if convertedDir != "" {
				_ = safefs.WriteFile(filepath.Join(convertedDir, "cover.jpg"), cBytes, 0644)
				job.SetCoverPath(filepath.Join(job.ID, "converted", "cover.jpg"))
			}
		} else if ctx.Err() != nil {
			job.SetError("Загрузка отменена")
			return
		} else if err != nil {
			log.Printf("[Downloader] Job %s: failed to download album cover, continuing without cover", job.ID)
		}
	}

	completedCount := 0
	hasErrors := false

	// Alternative sources require an explicit user choice.
	// Параллельная обработка треков альбома с ограничением
	concurrency := runtime.NumCPU() / 2
	if concurrency < 2 {
		concurrency = 2
	}
	if concurrency > 4 {
		concurrency = 4
	}
	sem := make(chan struct{}, concurrency)

	var wg sync.WaitGroup
	var mu sync.Mutex

	for idx := range tracks {
		if ctx.Err() != nil {
			job.SetError("Загрузка отменена")
			break
		}

		wg.Add(1)
		go func(trackIdx int, t resolver.ResolvedTrack) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			success := s.processSingleTrack(ctx, job, meta, trackIdx, t, rawDir, convertedDir, coverBytes, total)
			mu.Lock()
			if success {
				completedCount++
				job.IncrementCompleted(total)
			} else {
				hasErrors = true
			}
			mu.Unlock()
		}(idx+1, tracks[idx])
	}

	wg.Wait()

	if ctx.Err() != nil {
		job.SetError("Загрузка отменена")
		return
	}

	if completedCount == 0 {
		for _, tr := range job.Snapshot().Tracks {
			if tr.Status == "waiting_source" {
				job.SetStepAndStatus("Выберите источник вручную", StatusWaitingSource)
				return
			}
		}
	}
	if completedCount == total {
		job.SetReady(completedCount, total)
	} else if completedCount > 0 {
		job.SetPartialReady(completedCount, total)
	} else if hasErrors {
		job.SetError("Ни один трек не удалось загрузить")
	}
}

func (s *Service) processSingleTrack(
	ctx context.Context,
	job *Job,
	meta *resolver.ReleaseMetadata,
	trackIdx int,
	track resolver.ResolvedTrack,
	rawDir, convertedDir string,
	coverBytes []byte,
	total int,
) bool {
	streamURL := track.AudioSourceURL

	if streamURL == "" || strings.Contains(streamURL, "deezer.com") {
		errStr := "Выберите источник трека или релиза вручную"
		job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) { tr.Status = "waiting_source"; tr.Error = &errStr })
		return false
	}

	job.SetStepAndStatus(fmt.Sprintf("Скачивание трека %d/%d: %s", trackIdx, total, track.Title), StatusDownloading)

	outTemplate := filepath.Join(rawDir, fmt.Sprintf("%02d_track.%%(ext)s", trackIdx))
	rawAudioFile, sourcePage, err := downloadmedia.DownloadAndValidateAudioSource(ctx, s.cookiesPath, streamURL, outTemplate, track.Duration, func(pct float64) {
		job.UpdateTrackProgress(trackIdx-1, pct, fmt.Sprintf("Скачивание %d/%d: %.0f%%", trackIdx, total, pct))
	})
	// Never replace a user-selected source after failure.
	if err != nil {
		errStr := err.Error()
		job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
			tr.Status = "waiting_source"
			tr.Error = &errStr
		})
		return false
	}

	// Анализ аудио-качества (telemetry)
	quality := s.analyzeAudioQuality(ctx, rawAudioFile)
	job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
		tr.AudioQuality = quality
	})

	// Конвертация в MP3 320 kbps CBR
	job.SetStepAndStatus(fmt.Sprintf("Конвертация трека %d/%d: %s в MP3 320k", trackIdx, total, track.Title), StatusConverting)
	cleanTitle := id3.CleanTitle(track.Title, fmt.Sprintf("Track_%02d", trackIdx))
	trackPrefix := fmt.Sprintf("%02d. ", track.TrackNumber)
	if (job.IsBackfill || job.IsRedownload) && track.DiscNumber > 0 {
		trackPrefix = fmt.Sprintf("%02d-%02d. ", track.DiscNumber, track.TrackNumber)
	}
	if job.IsRedownload && track.TrackNumber <= 0 {
		trackPrefix = fmt.Sprintf("track-%02d. ", trackIdx)
	}
	maxTitleBytes := 255 - len(trackPrefix) - len(".mp3")
	if maxTitleBytes <= 0 {
		maxTitleBytes = 1
	}
	safeTitle := sandbox.SanitizeFilenameWithBudget(cleanTitle, fmt.Sprintf("Track_%02d", trackIdx), maxTitleBytes)
	mp3Filename := fmt.Sprintf("%s%s.mp3", trackPrefix, safeTitle)
	mp3Path := filepath.Join(convertedDir, mp3Filename)

	if err := s.transcodeToMP3(ctx, rawAudioFile, mp3Path); err != nil {
		errStr := err.Error()
		job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
			tr.Status = "error"
			tr.Error = &errStr
		})
		return false
	}

	// Запись ID3v2.4 тегов
	job.SetStepAndStatus(fmt.Sprintf("Запись ID3v2.4 тегов %d/%d: %s", trackIdx, total, track.Title), StatusTagging)

	art := strings.TrimSpace(track.Artist)
	albArt := strings.TrimSpace(track.AlbumArtist)
	alb := strings.TrimSpace(track.Album)
	if alb == "" {
		if meta.IsPlaylist {
			alb = track.Title
		} else {
			alb = meta.AlbumTitle
		}
	}
	if albArt == "" {
		if !meta.IsPlaylist {
			albArt = meta.AlbumArtist
		}
	}
	if albArt == "" && art != "" {
		albArt = art
	}

	tagData := id3.TrackTags{
		SourceURL:   sourcePage,
		Path:        mp3Path,
		Title:       track.Title,
		Artist:      art,
		Album:       alb,
		AlbumArtist: albArt,
		TrackNumber: track.TrackNumber,
		DiscNumber:  track.DiscNumber,
		Duration:    float64(track.Duration),
	}
	if track.ISRC != nil {
		tagData.ISRC = *track.ISRC
	}
	if track.TrackTotal != nil {
		tagData.TrackTotal = *track.TrackTotal
	} else if total > 0 {
		tagData.TrackTotal = total
	}
	if track.DiscTotal != nil {
		tagData.DiscTotal = *track.DiscTotal
	}
	if track.Year != nil {
		tagData.Year = *track.Year
	} else if meta.Year > 0 {
		tagData.Year = meta.Year
	}
	if track.Genre != nil {
		tagData.Genre = id3.CanonicalizeGenre(*track.Genre)
	} else if meta.Genre != "" {
		tagData.Genre = id3.CanonicalizeGenre(meta.Genre)
	}

	_, syncedTracks := id3.SyncAlbumArtistAndTrackArtists(albArt, []id3.TrackTags{tagData})
	tagData = syncedTracks[0]

	// Индивидуальная обложка трека для плейлистов или релизов с разными обложками
	trackCoverBytes := coverBytes
	if track.CoverURL != nil && *track.CoverURL != "" && (meta.IsPlaylist || len(coverBytes) == 0) {
		if tcBytes, err := s.downloadCover(ctx, *track.CoverURL); err == nil && len(tcBytes) > 0 {
			trackCoverBytes = tcBytes
		} else if ctx.Err() != nil {
			return false
		} else if err != nil {
			log.Printf("[Downloader] Job %s: failed to download track cover, continuing without track cover", job.ID)
		}
	}

	var cover *id3.CoverInfo
	if len(trackCoverBytes) > 0 {
		mime := "image/jpeg"
		if bytes.HasPrefix(trackCoverBytes, []byte("\x89PNG")) {
			mime = "image/png"
		}
		cover = &id3.CoverInfo{
			MimeType: mime,
			Data:     trackCoverBytes,
		}
	}

	if err := id3.WriteTags(mp3Path, tagData, cover); err != nil {
		message := err.Error()
		job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
			tr.Status, tr.Error = "error", &message
		})
		return false
	}

	// Поиск текста песни через LRCLIB (синхронизированного .lrc или обычного .txt)
	// ВАЖНО: Тексты песен сохраняются строго во внешние файлы рядом с треком, никогда в ID3-тег
	var spots []censorcheck.QuickListenSpot
	if s.lyrics != nil {
		primaryArt := resolver.ExtractPrimaryArtist(track.Artist)
		lyricsAlbum := alb
		if lyricsAlbum == "" && !meta.IsPlaylist {
			lyricsAlbum = meta.AlbumTitle
		}
		var searchOpts []lyrics.SearchOption
		if yandexID, ok := track.GetYandexTrackID(); ok {
			searchOpts = append(searchOpts, lyrics.WithYandexTrackID(yandexID))
		}
		lrcRes, _ := s.lyrics.SearchLyricsWithOptions(ctx, primaryArt, track.Title, lyricsAlbum, track.Duration, searchOpts...)
		if (lrcRes == nil || lrcRes.Lyrics == "") && primaryArt != track.Artist && ctx.Err() == nil {
			lrcRes, _ = s.lyrics.SearchLyrics(ctx, track.Artist, track.Title, lyricsAlbum, track.Duration)
		}
		if lrcRes != nil && lrcRes.Lyrics != "" {
			if err := lyrics.SaveDownloadedLyrics(s.tmpSandbox, mp3Path, lrcRes); err != nil {
				log.Printf("[Downloader] Failed to save lyrics companion: %v", err)
			}
			if lrcRes.Synced {
				spots = censorcheck.ExtractSpots(lrcRes.Lyrics, 3)
			}
		}
	}

	job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
		tr.Status = "ready"
		convPath := filepath.Join(job.ID, "converted", mp3Filename)
		tr.ConvertedPath = &convPath
		if len(spots) > 0 {
			tr.QuickListenSpots = spots
		}
	})

	return true
}
