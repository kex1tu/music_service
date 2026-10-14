package downloader

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

func (s *Service) runBackfillPipeline(jobID, deezerAlbumID string, trackURLs []string, trackNumbers []int) {
	job, ok := s.jobs.Get(jobID)
	if !ok {
		return
	}

	parentCtx := s.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(parentCtx)
	job.SetCancelFunc(cancel)
	defer cancel()

	// Wait if queue is paused
	if err := s.waitIfPaused(ctx); err != nil {
		return
	}

	// Acquire concurrency slot
	select {
	case <-ctx.Done():
		return
	case s.sem <- struct{}{}:
	}
	defer func() { <-s.sem }()

	// Step 1: Resolving
	job.SetStatus(StatusResolving)
	job.UpdateProgress(5.0, "Определение недостающих треков и получение метаданных")

	var meta *resolver.ReleaseMetadata
	var tracks []resolver.ResolvedTrack

	if s.resolver != nil && deezerAlbumID == "" && job.AlbumTitle != nil && *job.AlbumTitle != "" && s.resolver.Deezer() != nil {
		art := ""
		if job.ArtistName != nil {
			art = *job.ArtistName
		}
		q := fmt.Sprintf(`artist:"%s" album:"%s"`, art, *job.AlbumTitle)
		albums, sErr := s.resolver.Deezer().SearchAlbums(ctx, q)
		if sErr != nil || len(albums) == 0 {
			albums, sErr = s.resolver.Deezer().SearchAlbums(ctx, fmt.Sprintf("%s %s", art, *job.AlbumTitle))
		}
		if sErr == nil && len(albums) > 0 {
			deezerAlbumID = fmt.Sprintf("%d", albums[0].ID)
		}
	}

	if s.resolver != nil && deezerAlbumID != "" {
		m, allTracks, err := s.resolver.Resolve(ctx, fmt.Sprintf("https://www.deezer.com/album/%s", deezerAlbumID), "deezer")
		if err == nil && len(allTracks) > 0 {
			meta = m
			reqNums := make(map[int]bool)
			for _, n := range trackNumbers {
				reqNums[n] = true
			}
			reqURLs := make(map[string]bool)
			for _, u := range trackURLs {
				reqURLs[strings.ToLower(strings.TrimSpace(u))] = true
			}

			if len(reqNums) > 0 || len(reqURLs) > 0 {
				for _, t := range allTracks {
					trackURL := fmt.Sprintf("https://www.deezer.com/track/%s", strings.TrimPrefix(t.TrackID, "dz_"))
					if reqNums[t.TrackNumber] || reqURLs[strings.ToLower(trackURL)] {
						tracks = append(tracks, t)
					}
				}
			} else {
				tracks = allTracks
			}
		}
	}

	if s.resolver != nil && len(tracks) == 0 && len(trackURLs) > 0 {
		for _, u := range trackURLs {
			m, trs, err := s.resolver.Resolve(ctx, u, "deezer")
			if err == nil && len(trs) > 0 {
				tracks = append(tracks, trs[0])
				if meta == nil {
					meta = m
				}
			}
		}
	}

	if len(tracks) == 0 && s.jobs != nil && s.jobs.Database() != nil && job.TargetAlbumPath != nil {
		rows, qErr := s.jobs.Database().Conn().QueryContext(ctx, `
			SELECT mt.title, mt.artist, mt.track_number, COALESCE(mt.expected_duration, 0), COALESCE(mt.deezer_url, '')
			FROM missing_tracks mt
			JOIN albums al ON mt.album_id = al.id
			WHERE (al.folder_path = ? OR al.folder_path = ?)
		`, *job.TargetAlbumPath, filepath.Clean(*job.TargetAlbumPath))
		if qErr == nil {
			for rows.Next() {
				var tit, art, dzURL string
				var num, dur int
				if err := rows.Scan(&tit, &art, &num, &dur, &dzURL); err == nil {
					reqNumMatch := len(trackNumbers) == 0
					for _, rn := range trackNumbers {
						if rn == num {
							reqNumMatch = true
							break
						}
					}
					if reqNumMatch {
						aTit := ""
						if job.AlbumTitle != nil {
							aTit = *job.AlbumTitle
						}
						aArt := ""
						if job.ArtistName != nil {
							aArt = *job.ArtistName
						}
						tracks = append(tracks, resolver.ResolvedTrack{
							TrackNumber:    num,
							Title:          tit,
							Artist:         art,
							Album:          aTit,
							AlbumArtist:    aArt,
							Duration:       dur,
							AudioSourceURL: dzURL,
							Status:         "pending",
						})
					}
				}
			}
			rows.Close()
		}
	}

	if len(tracks) == 0 && len(trackNumbers) > 0 {
		aTit := ""
		if job.AlbumTitle != nil {
			aTit = *job.AlbumTitle
		}
		aArt := ""
		if job.ArtistName != nil {
			aArt = *job.ArtistName
		}
		for _, num := range trackNumbers {
			tracks = append(tracks, resolver.ResolvedTrack{
				TrackNumber: num,
				Title:       fmt.Sprintf("%s - Track %02d", aTit, num),
				Artist:      aArt,
				Album:       aTit,
				AlbumArtist:  aArt,
				Status:      "pending",
			})
		}
	}

	if len(tracks) == 0 {
		s.failJob(job, "Не удалось разрешить недостающие треки для дозагрузки")
		return
	}

	if meta == nil {
		meta = &resolver.ReleaseMetadata{}
	}
	targetArtist := ""
	if job.ArtistName != nil && *job.ArtistName != "" {
		targetArtist = strings.TrimSpace(*job.ArtistName)
		meta.AlbumArtist = targetArtist
	}
	targetAlbum := ""
	if job.AlbumTitle != nil && *job.AlbumTitle != "" {
		targetAlbum = strings.TrimSpace(*job.AlbumTitle)
		meta.AlbumTitle = targetAlbum
	}

	for i := range tracks {
		if targetArtist != "" {
			if tracks[i].Artist == "" || tracks[i].Artist == meta.AlbumArtist || resolver.AreArtistsEquivalent(tracks[i].Artist, targetArtist) || resolver.AreArtistsEquivalent(tracks[i].AlbumArtist, targetArtist) {
				tracks[i].Artist = targetArtist
			} else if strings.HasPrefix(strings.ToLower(tracks[i].Artist), strings.ToLower(meta.AlbumArtist)) {
				tracks[i].Artist = strings.Replace(tracks[i].Artist, meta.AlbumArtist, targetArtist, 1)
			}
			tracks[i].AlbumArtist = targetArtist
		}
		if targetAlbum != "" && (tracks[i].Album == "" || resolver.CleanQueryText(tracks[i].Album) == resolver.CleanQueryText(targetAlbum)) {
			tracks[i].Album = targetAlbum
		}
	}

	// Create temporary working directories inside TMP_DIR sandbox
	rawDir, err := s.tmpSandbox.Resolve(filepath.Join(job.ID, "raw"), true)
	if err != nil {
		s.failJob(job, fmt.Sprintf("Sandbox error: %v", err))
		return
	}
	convertedDir, err := s.tmpSandbox.Resolve(filepath.Join(job.ID, "converted"), true)
	if err != nil {
		s.failJob(job, fmt.Sprintf("Sandbox error: %v", err))
		return
	}
	_ = os.MkdirAll(rawDir, 0755)
	_ = os.MkdirAll(convertedDir, 0755)

	s.processTracksPipeline(ctx, job, meta, tracks, rawDir, convertedDir)
}

func (s *Service) runPipeline(jobID string) {
	job, ok := s.jobs.Get(jobID)
	if !ok {
		return
	}

	parentCtx := s.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(parentCtx)
	job.SetCancelFunc(cancel)
	defer cancel()

	// Wait if queue is paused
	if err := s.waitIfPaused(ctx); err != nil {
		return
	}

	// Acquire concurrency slot
	select {
	case <-ctx.Done():
		return
	case s.sem <- struct{}{}:
	}
	defer func() { <-s.sem }()

	// Step 1: Resolving
	job.SetStatus(StatusResolving)
	job.UpdateProgress(5.0, "Определение источника и получение метаданных")

	meta, tracks, err := s.resolver.Resolve(ctx, job.URL, job.Source)
	if err != nil {
		s.failJob(job, fmt.Sprintf("Ошибка разрешения метаданных: %v", err))
		return
	}

	// Create temporary working directories inside TMP_DIR sandbox
	rawDir, err := s.tmpSandbox.Resolve(filepath.Join(job.ID, "raw"), true)
	if err != nil {
		s.failJob(job, fmt.Sprintf("Sandbox error: %v", err))
		return
	}
	convertedDir, err := s.tmpSandbox.Resolve(filepath.Join(job.ID, "converted"), true)
	if err != nil {
		s.failJob(job, fmt.Sprintf("Sandbox error: %v", err))
		return
	}
	_ = os.MkdirAll(rawDir, 0755)
	_ = os.MkdirAll(convertedDir, 0755)

	s.processTracksPipeline(ctx, job, meta, tracks, rawDir, convertedDir)
}

func (s *Service) processTracksPipeline(
	ctx context.Context,
	job *DownloadJob,
	meta *resolver.ReleaseMetadata,
	tracks []resolver.ResolvedTrack,
	rawDir, convertedDir string,
) {
	if meta == nil {
		meta = &resolver.ReleaseMetadata{}
	}

	if len(tracks) == 1 {
		t := &tracks[0]
		// 1. Artists / Performers: release album artist must receive all track artists
		if strings.TrimSpace(meta.AlbumArtist) == "" || meta.AlbumArtist != t.Artist {
			if strings.TrimSpace(t.Artist) != "" {
				meta.AlbumArtist = t.Artist
			}
		}
		if strings.TrimSpace(t.AlbumArtist) == "" || t.AlbumArtist != t.Artist {
			if strings.TrimSpace(t.Artist) != "" {
				t.AlbumArtist = t.Artist
			}
		}

		// 2. Genre: transfer between track and release
		if strings.TrimSpace(meta.Genre) == "" && t.Genre != nil && strings.TrimSpace(*t.Genre) != "" {
			meta.Genre = strings.TrimSpace(*t.Genre)
		} else if strings.TrimSpace(meta.Genre) != "" && (t.Genre == nil || strings.TrimSpace(*t.Genre) == "") {
			g := strings.TrimSpace(meta.Genre)
			t.Genre = &g
		}

		// 3. Year: transfer between track and release
		if meta.Year == 0 && t.Year != nil && *t.Year > 0 {
			meta.Year = *t.Year
		} else if meta.Year > 0 && (t.Year == nil || *t.Year == 0) {
			y := meta.Year
			t.Year = &y
		}

		// 4. Album title: if empty or "single"/"singles", release and track album become track title
		if strings.TrimSpace(meta.AlbumTitle) == "" || strings.EqualFold(meta.AlbumTitle, "single") || strings.EqualFold(meta.AlbumTitle, "singles") {
			if strings.TrimSpace(t.Title) != "" {
				meta.AlbumTitle = strings.TrimSpace(t.Title)
				t.Album = strings.TrimSpace(t.Title)
			}
		} else if strings.TrimSpace(t.Album) == "" || strings.EqualFold(t.Album, "single") || strings.EqualFold(t.Album, "singles") {
			t.Album = meta.AlbumTitle
		}
	}

	job.SetMetadata(*meta, tracks)

	// Download album cover if available
	var coverBytes []byte
	if meta.CoverURL != "" {
		cBytes, cErr := s.downloadCover(ctx, meta.CoverURL)
		if cErr == nil && len(cBytes) > 0 {
			coverBytes = cBytes
			_ = os.WriteFile(filepath.Join(rawDir, "cover.jpg"), coverBytes, 0644)
			_ = os.WriteFile(filepath.Join(convertedDir, "cover.jpg"), coverBytes, 0644)
			relCover, _ := s.tmpSandbox.ToRelative(filepath.Join(convertedDir, "cover.jpg"))
			job.SetCoverPath(relCover)
		}
	}

	total := len(tracks)
	if total == 0 {
		s.failJob(job, "Релиз не содержит доступных аудиотреков")
		return
	}

	// Process each track
	var wg sync.WaitGroup
	maxConcurrentTracks := runtime.NumCPU() / 2
	if maxConcurrentTracks < 2 {
		maxConcurrentTracks = 2
	} else if maxConcurrentTracks > 4 {
		maxConcurrentTracks = 4
	}
	trackSem := make(chan struct{}, maxConcurrentTracks)

trackLoop:
	for i := range tracks {
		idx := i + 1
		t := &tracks[i]

		select {
		case <-ctx.Done():
			break trackLoop
		case trackSem <- struct{}{}:
		}

		wg.Add(1)
		go func(trackIdx int, track *resolver.ResolvedTrack) {
			defer wg.Done()
			defer func() { <-trackSem }()

			select {
			case <-ctx.Done():
				return
			default:
			}

			// If audio stream URL is not yet resolved (e.g. Deezer album), match to YouTube
			if track.AudioSourceURL == "" || strings.Contains(track.AudioSourceURL, "deezer.com") {
				job.SetCurrentStep(fmt.Sprintf("Поиск аудиопотока %d/%d: %s", trackIdx, total, track.Title))

				if s.resolver == nil || s.resolver.Matcher() == nil {
					errStr := "audio matcher not available"
					job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
						tr.Status = "error"
						tr.Error = &errStr
					})
					return
				}

				trackISRC := ""
				if track.ISRC != nil {
					trackISRC = *track.ISRC
				}
				matchRes, mErr := s.resolver.Matcher().FindBestMatchWithDetails(ctx, track.Title, track.Artist, trackISRC, track.Duration)
				if mErr != nil {
					errStr := "Источник не найден: требуется ручной выбор"
					job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
						tr.Status = "waiting_source"
						tr.Error = &errStr
					})
					return
				}
				job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
					tr.AudioSourceURL = matchRes.URL
					tr.VersionStatus = matchRes.Status
					tr.Candidates = matchRes.Candidates
				})
				track.AudioSourceURL = matchRes.URL
				track.VersionStatus = matchRes.Status
				track.Candidates = matchRes.Candidates
			}

			// Download stream with yt-dlp
			rawOutTemplate := filepath.Join(rawDir, fmt.Sprintf("%02d.%%(ext)s", trackIdx))
			job.SetStepAndStatus(fmt.Sprintf("Загрузка трека %d/%d: %s", trackIdx, total, track.Title), StatusDownloading)

			rawAudioFile, dErr := s.downloadRawStream(ctx, track.AudioSourceURL, rawOutTemplate, func(p float64) {
				job.UpdateTrackProgress(trackIdx-1, p, fmt.Sprintf("Загрузка трека %d/%d: %s (%.0f%%)", trackIdx, total, track.Title, p))
			})
			if dErr != nil && s.resolver != nil && s.resolver.Matcher() != nil {
				isSC := strings.Contains(track.AudioSourceURL, "soundcloud.com")
				isYT := strings.Contains(track.AudioSourceURL, "youtube.com") || strings.Contains(track.AudioSourceURL, "youtu.be")

				if isSC {
					// Обработка 404/410 для SoundCloud:
					trackISRC := ""
					if track.ISRC != nil {
						trackISRC = *track.ISRC
					}
					cacheKey := resolver.BuildCacheKey(trackISRC, track.Artist, track.Title)
					if s.Database() != nil {
						invEntry, invErr := s.Database().InvalidateSoundCloudURL(ctx, cacheKey, track.AudioSourceURL)
						if invErr == nil && invEntry != nil {
							if invEntry.IsManual {
								// Ручная ссылка: статус 'link_dead', НЕ подменять ссылку автоматически
								job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
									tr.VersionStatus = "link_dead"
								})
								track.VersionStatus = "link_dead"
							} else if invEntry.SelectedURL != "" {
								// Автоматическая ссылка: переключаемся на следующего кандидата
								job.SetCurrentStep(fmt.Sprintf("Ротация кандидата SoundCloud %d/%d: %s", trackIdx, total, track.Title))
								job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
									tr.AudioSourceURL = invEntry.SelectedURL
									tr.VersionStatus = invEntry.Status
									tr.Candidates = invEntry.Candidates
								})
								track.AudioSourceURL = invEntry.SelectedURL
								track.VersionStatus = invEntry.Status
								track.Candidates = invEntry.Candidates
								rawAudioFile, dErr = s.downloadRawStream(ctx, invEntry.SelectedURL, rawOutTemplate, func(p float64) {
									job.UpdateTrackProgress(trackIdx-1, p, fmt.Sprintf("Загрузка трека %d/%d: %s (%.0f%%)", trackIdx, total, track.Title, p))
								})
							}
						}
					}

					// Если SoundCloud всё ещё выдаёт ошибку — прозрачный фоллбэк на YouTube
					if dErr != nil {
						job.SetCurrentStep(fmt.Sprintf("Резервный поиск YouTube %d/%d: %s", trackIdx, total, track.Title))
						if ytURL, ytErr := s.resolver.Matcher().FindYouTubeMatch(ctx, track.Title, track.Artist, track.Duration); ytErr == nil && ytURL != "" {
							job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
								tr.AudioSourceURL = ytURL
								tr.VersionStatus = ""
							})
							track.AudioSourceURL = ytURL
							rawAudioFile, dErr = s.downloadRawStream(ctx, ytURL, rawOutTemplate, func(p float64) {
								job.UpdateTrackProgress(trackIdx-1, p, fmt.Sprintf("Загрузка трека %d/%d: %s (%.0f%%)", trackIdx, total, track.Title, p))
							})
						}
					}
				} else if isYT {
					// Fallback from YouTube (e.g. age restriction / DRM) to SoundCloud
					job.SetCurrentStep(fmt.Sprintf("Резервный поиск SoundCloud %d/%d: %s", trackIdx, total, track.Title))
					if scURL, scErr := s.resolver.Matcher().FindSoundCloudMatch(ctx, track.Title, track.Artist, track.Duration); scErr == nil && scURL != "" {
						job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
							tr.AudioSourceURL = scURL
						})
						track.AudioSourceURL = scURL
						rawAudioFile, dErr = s.downloadRawStream(ctx, scURL, rawOutTemplate, func(p float64) {
							job.UpdateTrackProgress(trackIdx-1, p, fmt.Sprintf("Загрузка трека %d/%d: %s (%.0f%%)", trackIdx, total, track.Title, p))
						})
					}
				}
			}
			if dErr != nil {
				errStr := dErr.Error()
				job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
					tr.Status = "waiting_source"
					tr.Error = &errStr
				})
				return
			}
			defer os.Remove(rawAudioFile)

			// Analyze audio quality telemetry (codec, bitrate, cutoff frequency)
			qualityTelemetry := s.analyzeAudioQuality(ctx, rawAudioFile)
			job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
				tr.AudioQuality = qualityTelemetry
			})
			track.AudioQuality = qualityTelemetry

			// Transcode to MP3 320 kbps CBR
			job.SetStepAndStatus(fmt.Sprintf("Конвертация трека %d/%d: %s в MP3 320k", trackIdx, total, track.Title), StatusConverting)

			cleanTitle := id3.CleanTitle(track.Title, fmt.Sprintf("Track_%02d", trackIdx))
			safeTitle := sandbox.SanitizeFilename(cleanTitle, fmt.Sprintf("Track_%02d", trackIdx))
			mp3Filename := fmt.Sprintf("%02d. %s.mp3", track.TrackNumber, safeTitle)
			mp3Path := filepath.Join(convertedDir, mp3Filename)

			if cErr := s.transcodeToMP3(ctx, rawAudioFile, mp3Path); cErr != nil {
				errStr := cErr.Error()
				job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
					tr.Status = "error"
					tr.Error = &errStr
				})
				return
			}

			// Write ID3v2.4 tags
			job.SetStepAndStatus(fmt.Sprintf("Запись ID3v2.4 тегов %d/%d: %s", trackIdx, total, track.Title), StatusTagging)

			art := strings.TrimSpace(track.Artist)
			albArt := strings.TrimSpace(track.AlbumArtist)
			alb := strings.TrimSpace(track.Album)
			if (alb == "" || strings.EqualFold(alb, "single") || strings.EqualFold(alb, "singles")) && strings.TrimSpace(track.Title) != "" {
				alb = strings.TrimSpace(track.Title)
			}
			if total == 1 && albArt == "" && art != "" {
				albArt = art
			}

			if job.IsBackfill && job.ArtistName != nil && *job.ArtistName != "" {
				targetArt := strings.TrimSpace(*job.ArtistName)
				if art == "" || resolver.AreArtistsEquivalent(art, targetArt) {
					art = targetArt
				}
				albArt = targetArt
			}
			if job.IsBackfill && job.AlbumTitle != nil && *job.AlbumTitle != "" {
				alb = strings.TrimSpace(*job.AlbumTitle)
			}

			tagData := id3.TrackTags{
				Path:        mp3Path,
				Title:       track.Title,
				Artist:      art,
				Album:       alb,
				AlbumArtist: albArt,
				TrackNumber: track.TrackNumber,
				DiscNumber:  track.DiscNumber,
				Duration:    float64(track.Duration),
			}
			if track.TrackTotal != nil {
				tagData.TrackTotal = *track.TrackTotal
			}
			if track.DiscTotal != nil {
				tagData.DiscTotal = *track.DiscTotal
			}
			if track.Year != nil {
				tagData.Year = *track.Year
			}
			if track.Genre != nil {
				tagData.Genre = *track.Genre
			}

			var cover *id3.CoverInfo
			if len(coverBytes) > 0 {
				mime := "image/jpeg"
				if bytes.HasPrefix(coverBytes, []byte("\x89PNG")) {
					mime = "image/png"
				}
				cover = &id3.CoverInfo{
					MimeType: mime,
					Data:     coverBytes,
				}
			}

			_ = id3.WriteTags(mp3Path, tagData, cover)

			relConverted, _ := s.tmpSandbox.ToRelative(mp3Path)
			job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
				tr.Status = "ready"
				tr.ConvertedPath = &relConverted
			})

			job.IncrementCompleted(total)

		}(idx, t)
	}

	wg.Wait()

	// Final step: READY or ERROR
	snap := job.Snapshot()
	if snap.CompletedTracks == 0 && total > 0 {
		var errDetails []string
		for _, tr := range snap.Tracks {
			if tr.Error != nil && *tr.Error != "" {
				errDetails = append(errDetails, fmt.Sprintf("%s: %s", tr.Title, *tr.Error))
			}
		}
		errMsg := "Ни один из треков релиза не был успешно загружен"
		if len(errDetails) > 0 {
			errMsg = fmt.Sprintf("%s (%s)", errMsg, strings.Join(errDetails, "; "))
		}
		s.failJob(job, errMsg)
		return
	}

	if snap.CompletedTracks < total {
		job.SetPartialReady(snap.CompletedTracks, total)
	} else {
		job.SetReady(snap.CompletedTracks, total)
	}
}

func (s *Service) failJob(job *DownloadJob, errMsg string) {
	job.SetError(errMsg)
	log.Printf("[Downloader] Job %s failed: %s", job.ID, errMsg)
}
