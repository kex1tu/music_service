package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
)

// SwitchTrackVersion выполняет ручную смену источника трека.
func (s *Service) SwitchTrackVersion(ctx context.Context, trackID, targetURL string, policies ...resolver.VersionPolicy) (*SwitchVersionResult, error) {
	policy := resolver.PolicyRelease
	if len(policies) > 0 {
		policy = policies[0]
	}
	if !resolver.ValidVersionPolicy(policy) {
		return nil, ErrInvalidURL
	}
	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		return nil, fmt.Errorf("%w: target_url cannot be empty", ErrInvalidURL)
	}
	if strings.HasPrefix(targetURL, "-") {
		return nil, fmt.Errorf("%w: target_url cannot start with '-'", ErrInvalidURL)
	}

	parsedURL, pErr := url.Parse(targetURL)
	if pErr != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return nil, fmt.Errorf("%w: invalid url scheme", ErrInvalidURL)
	}

	if !resolver.IsAllowedURL(targetURL) {
		return nil, fmt.Errorf("%w: domain %q is not allowed", ErrInvalidURL, parsedURL.Hostname())
	}

	var matchedJob *Job
	var trackIdx int = -1
	var matchedTrack *resolver.ResolvedTrack

	s.jobsMu.Lock()
	for _, j := range s.jobs {
		snapshot := j.Snapshot()
		for i := range snapshot.Tracks {
			if snapshot.Tracks[i].TrackID == trackID {
				matchedTrack = &snapshot.Tracks[i]
				matchedJob = j
				trackIdx = i
				break
			}
		}
		if matchedTrack != nil {
			break
		}
	}
	s.jobsMu.Unlock()

	// Фиксируем пин в репозитории
	if s.repo != nil {
		_ = s.repo.SavePinnedSource(ctx, PinnedTrackSourceDTO{
			CacheKey:   trackID,
			PinnedURL:  targetURL,
			SourceType: "manual",
			UpdatedAt:  time.Now().UTC(),
		})

		if matchedTrack != nil {
			isrc := ""
			if matchedTrack.ISRC != nil {
				isrc = *matchedTrack.ISRC
			}
			cacheKey := resolver.BuildCacheKey(isrc, matchedTrack.Artist, matchedTrack.Title)
			_ = s.repo.SavePinnedSource(ctx, PinnedTrackSourceDTO{
				CacheKey:   cacheKey,
				PinnedURL:  targetURL,
				SourceType: "manual",
				UpdatedAt:  time.Now().UTC(),
			})
		}
	}

	if matchedJob != nil && trackIdx >= 0 {
		matchedTrack.AudioSourceURL = targetURL
		matchedTrack.VersionStatus = "manual"
		matchedTrack.VersionPolicy = policy
		matchedJob.UpdateTrack(trackIdx, func(tr *resolver.ResolvedTrack) {
			tr.AudioSourceURL = targetURL
			tr.VersionStatus = "manual"
			tr.VersionPolicy = policy
		})

		// Запускаем асинхронную дозагрузку и конвертацию выбранного трека
		go func() {
			var rawDir, convertedDir string
			if s.tmpSandbox != nil {
				rawDir, _ = s.tmpSandbox.Resolve(filepath.Join(matchedJob.ID, "raw"), true)
				convertedDir, _ = s.tmpSandbox.Resolve(filepath.Join(matchedJob.ID, "converted"), true)
			}
			var coverBytes []byte
			if convertedDir != "" {
				coverBytes, _ = safefs.ReadFile(filepath.Join(convertedDir, "cover.jpg"))
			}
			snapshot := matchedJob.Snapshot()
			meta := snapshot.AlbumMetadata
			if meta.AlbumTitle == "" && snapshot.AlbumTitle != nil {
				meta.AlbumTitle = *snapshot.AlbumTitle
			}
			if meta.AlbumArtist == "" && snapshot.ArtistName != nil {
				meta.AlbumArtist = *snapshot.ArtistName
			}
			workCtx := s.ctx
			if workCtx == nil {
				workCtx = context.Background()
			}
			s.processSingleTrack(workCtx, matchedJob, &meta, trackIdx+1, *matchedTrack, rawDir, convertedDir, coverBytes, len(snapshot.Tracks))
			matchedJob.reconcileManualSources()
		}()
	}

	return &SwitchVersionResult{
		TrackID:   trackID,
		TargetURL: targetURL,
		Status:    "manual",
	}, nil
}

// SwitchReleaseSource привязывает выбранные аудиоисточники кандидатов ко всем трекам задачи
// и немедленно перезапускает процесс загрузки.
func (s *Service) SwitchReleaseSource(ctx context.Context, req SwitchReleaseSourceRequest) (*SwitchReleaseSourceResult, error) {
	if req.VersionPolicy == "" {
		req.VersionPolicy = resolver.PolicyRelease
	}
	if !resolver.ValidVersionPolicy(req.VersionPolicy) {
		return nil, ErrInvalidURL
	}
	if len(req.TrackMapping) == 0 {
		return nil, errors.New("выберите хотя бы один источник")
	}
	for idx, raw := range req.TrackMapping {
		if idx < 0 || !resolver.IsAllowedURL(raw) {
			return nil, ErrInvalidURL
		}
	}
	req.JobID = strings.TrimSpace(req.JobID)
	if req.JobID == "" && strings.TrimSpace(req.SourceURL) == "" {
		return nil, errors.New("необходимо указать job_id или source_url")
	}

	var job *Job
	if req.JobID != "" {
		var ok bool
		job, ok = s.GetJob(req.JobID)
		if !ok {
			return nil, fmt.Errorf("задача %s не найдена", req.JobID)
		}
		switch job.Snapshot().Status {
		case StatusDownloading, StatusConverting, StatusTagging, StatusResolving, StatusQueued:
			return nil, errors.New("дождитесь завершения текущей обработки")
		}
	} else if strings.TrimSpace(req.SourceURL) != "" {
		cleanURL := strings.TrimSpace(req.SourceURL)
		if !resolver.IsAllowedURL(cleanURL) || len(req.TargetTracks) == 0 {
			return nil, errors.New("нужны исходный треклист и допустимый источник")
		}
		if req.TargetPath != "" {
			if s.musicSandbox == nil {
				return nil, ErrInvalidURL
			}
			if _, e := s.musicSandbox.Resolve(req.TargetPath, false); e != nil {
				return nil, fmt.Errorf("%w: недопустимый путь целевого альбома", sandbox.ErrPathTraversal)
			}
		}
		job = &Job{ID: generateUUID(), Source: "release_picker", URL: cleanURL, Status: StatusQueued, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), UpdatedAt: time.Now().UTC(), Tracks: append([]resolver.ResolvedTrack(nil), req.TargetTracks...)}
		if req.Backfill != nil {
			job.IsBackfill = true
			job.Backfill = &BackfillRequest{Positions: []db.BackfillSelection{*req.Backfill}}
		}
		for i := range job.Tracks {
			job.Tracks[i].TrackID = fmt.Sprintf("manual_%s_%d", job.ID, i)
			if req.Backfill != nil && req.Backfill.DeezerID != "" {
				job.Tracks[i].TrackID = "dz_" + req.Backfill.DeezerID
			}
			job.Tracks[i].AudioSourceURL = ""
			job.Tracks[i].ConvertedPath = nil
		}
		job.AlbumMetadata = resolver.ReleaseMetadata{AlbumTitle: job.Tracks[0].Album, AlbumArtist: job.Tracks[0].Artist, TotalTracks: len(job.Tracks)}
		if req.TargetPath != "" {
			target := db.AlbumRoot(filepath.ToSlash(req.TargetPath))
			job.TargetAlbumPath = &target
		}
	}
	for idx := range req.TrackMapping {
		if idx >= len(job.Snapshot().Tracks) {
			return nil, errors.New("индекс трека вне треклиста")
		}
	}
	if req.JobID == "" {
		if s.repo != nil {
			if e := s.repo.CreateJob(ctx, job); e != nil {
				return nil, e
			}
		}
		s.AddJob(job)
	}

	updatedCount := 0
	totalTracks := 0

	job.mu.Lock()
	totalTracks = len(job.Tracks)
	for idx := range job.Tracks {
		if audioURL, ok := req.TrackMapping[idx]; ok && strings.TrimSpace(audioURL) != "" {
			trimmedURL := strings.TrimSpace(audioURL)
			job.Tracks[idx].AudioSourceURL = trimmedURL
			job.Tracks[idx].VersionStatus = "manual"
			job.Tracks[idx].VersionPolicy = req.VersionPolicy
			job.Tracks[idx].Status = "pending"
			job.Tracks[idx].Error = nil
			updatedCount++

			if s.repo != nil {
				_ = s.repo.SavePinnedSource(ctx, PinnedTrackSourceDTO{
					CacheKey:   job.Tracks[idx].TrackID,
					PinnedURL:  trimmedURL,
					SourceType: "manual",
					UpdatedAt:  time.Now().UTC(),
				})
			}
		} else {
			// Unmatched tracks wait for another explicit source choice.
			job.Tracks[idx].AudioSourceURL = ""
			job.Tracks[idx].Status = "pending"
			job.Tracks[idx].Error = nil
		}
	}

	job.Status = StatusQueued
	job.Progress = 0.0
	job.CompletedTracks = 0
	job.CurrentStep = "Источник релиза обновлен, перезапуск очереди"
	job.Error = nil
	job.UpdatedAt = time.Now().UTC()
	job.mu.Unlock()

	job.notifyUpdate()

	if s.repo != nil {
		_ = s.repo.SaveJob(ctx, job)
	}

	// Очищаем предыдущие временные скачанные файлы
	if s.tmpSandbox != nil {
		if rawDir, err := s.tmpSandbox.Resolve(filepath.Join(job.ID, "raw"), false); err == nil {
			_ = safefs.RemoveAll(rawDir)
		}
	}

	select {
	case s.workQueue <- job.ID:
	default:
	}

	return &SwitchReleaseSourceResult{
		JobID:        job.ID,
		UpdatedCount: updatedCount,
		TotalTracks:  totalTracks,
		Status:       "queued",
	}, nil
}
