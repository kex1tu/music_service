package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
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
	return s.switchJobTrackSource(ctx, "", trackID, targetURL, policy)
}

func (s *Service) switchJobTrackSource(ctx context.Context, jobID, trackID, targetURL string, policy resolver.VersionPolicy) (*SwitchVersionResult, error) {
	targetURL = strings.TrimSpace(targetURL)
	parsedURL, err := url.Parse(targetURL)
	if !resolver.ValidVersionPolicy(policy) || err != nil ||
		(parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || !resolver.IsAllowedURL(targetURL) {
		return nil, ErrInvalidURL
	}

	if jobID != "" {
		// Load by job ID when the task has not yet been materialized in memory.
		if _, ok := s.GetJob(jobID); !ok {
			return nil, ErrJobNotFound
		}
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()

	var job, draft *Job
	trackIndex := -1
	for id, candidate := range s.jobs {
		if jobID != "" && id != jobID {
			continue
		}
		snap := candidate.Snapshot()
		idx := slices.IndexFunc(snap.Tracks, func(track resolver.ResolvedTrack) bool {
			return track.TrackID == trackID
		})
		if idx < 0 {
			continue
		}
		if job != nil {
			return nil, errSelectionConflict
		}
		job, draft, trackIndex = candidate, snap, idx
	}
	if job == nil {
		return nil, ErrJobNotFound
	}
	if draft.CleanupPending != "" {
		return nil, errCleanupPending
	}
	if s.runningJobs[job.ID] {
		return nil, errSelectionConflict
	}
	previous := draft.Status
	switch previous {
	case StatusQueued, StatusResolving, StatusDownloading, StatusConverting, StatusTagging:
		return nil, errSelectionConflict
	}
	track := &draft.Tracks[trackIndex]
	for _, target := range draft.RedownloadTargets {
		if target.TrackID == trackID && (target.Confirmed || target.ResultHash != "") {
			return nil, errSelectionConflict
		}
	}
	selectManualSource(track, targetURL, policy)
	draft.manualTrackIndex = &trackIndex
	draft.resetForQueue("Выбранный источник ожидает очереди")
	if s.repo != nil {
		if err := s.repo.updateJobWithSelection(ctx, draft, previous); err != nil {
			return nil, fmt.Errorf("%w: %w", errQueueStorage, err)
		}
	}
	job.applyQueueSnapshot(draft)
	s.wakeWorker()

	return &SwitchVersionResult{TrackID: trackID, TargetURL: targetURL, Status: "manual"}, nil
}

func (s *Service) executeManualTrack(ctx context.Context, job, snapshot *Job) {
	index := *snapshot.manualTrackIndex
	if index < 0 || index >= len(snapshot.Tracks) {
		job.SetError("Выбранный трек отсутствует в сохранённом треклисте")
		return
	}
	var rawDir, convertedDir string
	var coverBytes []byte
	if s.tmpSandbox != nil {
		var err error
		rawDir, err = s.tmpSandbox.Resolve(filepath.Join(job.ID, "raw", "manual_"+generateUUID()), true)
		if err != nil {
			job.SetError("Недоступен временный каталог трека")
			return
		}
		convertedDir, err = s.tmpSandbox.Resolve(filepath.Join(job.ID, "converted"), true)
		if err != nil {
			job.SetError("Недоступен каталог подготовленных треков")
			return
		}
		for _, dir := range []string{rawDir, convertedDir} {
			if err := safefs.MkdirAll(dir, 0700); err != nil {
				job.SetError("Недоступен временный каталог трека")
				return
			}
		}
		defer safefs.RemoveAll(rawDir)
		if cover, err := s.tmpSandbox.Resolve(filepath.Join(job.ID, "converted", "cover.jpg"), false); err == nil {
			coverBytes, _ = safefs.ReadFile(cover)
		}
	}
	meta := snapshot.AlbumMetadata
	if meta.AlbumTitle == "" && snapshot.AlbumTitle != nil {
		meta.AlbumTitle = *snapshot.AlbumTitle
	}
	if meta.AlbumArtist == "" && snapshot.ArtistName != nil {
		meta.AlbumArtist = *snapshot.ArtistName
	}
	processed := s.processSingleTrack(ctx, job, &meta, index+1, snapshot.Tracks[index], rawDir, convertedDir, coverBytes, len(snapshot.Tracks))
	if ctx.Err() != nil {
		job.SetError("Загрузка отменена")
		return
	}
	if processed {
		job.mu.Lock()
		job.manualTrackIndex = nil
		job.mu.Unlock()
	}
	job.reconcileManualSources()
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

	var existing *Job
	if req.JobID != "" {
		var ok bool
		existing, ok = s.GetJob(req.JobID)
		if !ok {
			return nil, fmt.Errorf("задача %s не найдена", req.JobID)
		}
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()

	var draft *Job
	if existing != nil {
		if s.jobs[req.JobID] != existing {
			return nil, ErrJobNotFound
		}
		draft = existing.Snapshot()
		if draft.CleanupPending != "" {
			return nil, errCleanupPending
		}
		if s.runningJobs[existing.ID] {
			return nil, errSelectionConflict
		}
		if draft.selectionCorrupt {
			return nil, ErrCorruptJobSelection
		}
		if draft.IsRedownload && draft.Status != StatusWaitingSource {
			return nil, errSelectionConflict
		}
		switch draft.Status {
		case StatusDownloading, StatusConverting, StatusTagging, StatusResolving, StatusQueued:
			return nil, errSelectionConflict
		}
	} else {
		cleanURL := strings.TrimSpace(req.SourceURL)
		if !resolver.IsAllowedURL(cleanURL) || len(req.TargetTracks) == 0 {
			return nil, errors.New("нужны исходный треклист и допустимый источник")
		}
		if req.MissingOnly && req.TargetPath == "" {
			return nil, errors.New("укажите папку целевого релиза")
		}
		if req.TargetPath != "" {
			if s.musicSandbox == nil {
				return nil, ErrInvalidURL
			}
			full, err := s.musicSandbox.Resolve(req.TargetPath, req.MissingOnly || len(req.Backfill) > 0)
			if err != nil {
				return nil, fmt.Errorf("%w: недопустимый путь целевого альбома", sandbox.ErrPathTraversal)
			}
			relative, err := s.musicSandbox.ToRelative(full)
			if err != nil {
				return nil, err
			}
			req.TargetPath = db.AlbumRoot(relative)
		}
		if req.MissingOnly {
			req.Backfill = make([]db.BackfillSelection, len(req.TargetTracks))
			for i, track := range req.TargetTracks {
				position := db.ReleasePosition(track.DiscNumber, track.TrackNumber)
				id := strings.TrimPrefix(track.TrackID, "dz_")
				if id == track.TrackID {
					id = ""
				}
				req.Backfill[i] = db.BackfillSelection{TrackPosition: position, DeezerID: id}
			}
		}
		draft = &Job{ID: generateUUID(), Source: "release_picker", URL: cleanURL, Status: StatusQueued, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), UpdatedAt: time.Now().UTC(), Tracks: append([]resolver.ResolvedTrack(nil), req.TargetTracks...)}
		if len(req.Backfill) > 0 {
			if len(req.Backfill) != len(draft.Tracks) {
				return nil, errors.New("позиции докачки не соответствуют треклисту")
			}
			draft.IsBackfill = true
			draft.Backfill = &BackfillRequest{Positions: req.Backfill}
			if err := s.freezeBackfillSelection(ctx, req.TargetPath, draft.Backfill); err != nil {
				return nil, err
			}
		}
		for i := range draft.Tracks {
			draft.Tracks[i].TrackID = fmt.Sprintf("manual_%s_%d", draft.ID, i)
			if draft.IsBackfill {
				selection := draft.Backfill.Positions[i]
				if selection.DeezerID != "" {
					draft.Tracks[i].TrackID = "dz_" + selection.DeezerID
				}
			}
			draft.Tracks[i].AudioSourceURL = ""
			draft.Tracks[i].ConvertedPath = nil
		}
		draft.AlbumMetadata = resolver.ReleaseMetadata{AlbumTitle: draft.Tracks[0].Album, AlbumArtist: draft.Tracks[0].Artist, TotalTracks: len(draft.Tracks)}
		if req.TargetPath != "" {
			target := req.TargetPath
			draft.TargetAlbumPath = &target
		}
	}
	for idx := range req.TrackMapping {
		if idx >= len(draft.Tracks) {
			return nil, errors.New("индекс трека вне треклиста")
		}
	}
	for idx := range draft.Tracks {
		track := &draft.Tracks[idx]
		if audioURL, ok := req.TrackMapping[idx]; ok {
			selectManualSource(track, strings.TrimSpace(audioURL), req.VersionPolicy)
		} else {
			// Unmatched tracks wait for another explicit source choice.
			track.AudioSourceURL = ""
			track.VersionStatus = ""
			track.Status = "pending"
			track.Error = nil
		}
	}

	previous := draft.Status
	draft.manualTrackIndex = nil
	draft.TotalTracks = len(draft.Tracks)
	draft.resetForQueue("Источник релиза обновлен, перезапуск очереди")
	if s.repo != nil {
		var err error
		if existing == nil {
			err = s.repo.CreateJob(ctx, draft)
		} else {
			err = s.repo.updateJobWithSelection(ctx, draft, previous)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errQueueStorage, err)
		}
	}
	if s.tmpSandbox != nil {
		if rawDir, err := s.tmpSandbox.Resolve(filepath.Join(draft.ID, "raw"), false); err == nil {
			_ = safefs.RemoveAll(rawDir)
		}
	}
	if existing == nil {
		s.registerJobLocked(draft)
	} else {
		existing.applyQueueSnapshot(draft)
	}
	s.wakeWorker()

	return &SwitchReleaseSourceResult{
		JobID:        draft.ID,
		UpdatedCount: len(req.TrackMapping),
		TotalTracks:  len(draft.Tracks),
		Status:       "queued",
	}, nil
}
