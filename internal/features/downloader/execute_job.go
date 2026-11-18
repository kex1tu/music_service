package downloader

import (
	"context"
	"fmt"
	"path/filepath"

	"music-manager/internal/core/safefs"
	"music-manager/internal/services/resolver"
)

// executeJob запускает конвейер обработки задачи.
func (s *Service) executeJob(parentCtx context.Context, jobID string) {
	job, ok := s.GetJob(jobID)
	if !ok {
		return
	}

	ctx, cancel := context.WithCancel(parentCtx)
	job.SetCancelFunc(cancel)
	defer cancel()

	if s.waitIfPaused(ctx) != nil {
		return
	}

	if job.IsBackfill {
		s.executeBackfill(ctx, job)
	} else {
		s.executeRegular(ctx, job)
	}
}

func (s *Service) executeRegular(ctx context.Context, job *Job) {
	snapshot := job.Snapshot()
	hasManual := false
	for _, tr := range snapshot.Tracks {
		if tr.VersionStatus == "manual" {
			hasManual = true
			break
		}
	}
	job.SetStatus(StatusResolving)
	job.UpdateProgress(5.0, "Определение источника и метаданных")

	if s.resolver == nil && !hasManual {
		// Режим без внешнего резолвера (например, минимальные тесты)
		job.SetReady(job.CompletedTracks, job.TotalTracks)
		return
	}

	var meta *resolver.ReleaseMetadata
	var tracks []resolver.ResolvedTrack
	var err error
	if hasManual {
		meta = &snapshot.AlbumMetadata
		tracks = snapshot.Tracks
	} else {
		meta, tracks, err = s.resolveURL(ctx, job.URL, job.Source)
	}
	if err != nil {
		job.SetError(fmt.Sprintf("Ошибка разрешения: %v", err))
		return
	}

	if meta == nil {
		meta = &resolver.ReleaseMetadata{}
	}

	selectedIndices := job.GetSelectedTrackIndices()
	if len(selectedIndices) > 0 && !hasManual {
		var filteredTracks []resolver.ResolvedTrack
		for _, idx := range selectedIndices {
			if idx >= 0 && idx < len(tracks) {
				filteredTracks = append(filteredTracks, tracks[idx])
			}
		}
		tracks = filteredTracks
	}

	if len(tracks) == 0 {
		job.SetError("Не выбрано ни одного трека для загрузки")
		return
	}

	job.SetMetadata(*meta, tracks)
	job.UpdateProgress(10.0, "Метаданные получены, запуск загрузки")

	rawDir := ""
	convertedDir := ""
	if s.tmpSandbox != nil {
		var err error
		rawDir, err = s.tmpSandbox.Resolve(filepath.Join(job.ID, "raw"), true)
		if err != nil {
			job.SetError(fmt.Sprintf("Песочница: %v", err))
			return
		}
		convertedDir, err = s.tmpSandbox.Resolve(filepath.Join(job.ID, "converted"), true)
		if err != nil {
			job.SetError(fmt.Sprintf("Песочница: %v", err))
			return
		}
		_ = safefs.MkdirAll(rawDir, 0700)
		_ = safefs.MkdirAll(convertedDir, 0700)
	}

	s.processTracks(ctx, job, meta, tracks, rawDir, convertedDir)
}

func (s *Service) executeBackfill(ctx context.Context, job *Job) {
	job.SetStatus(StatusResolving)
	job.UpdateProgress(5.0, "Определение недостающих треков для дозагрузки")

	meta, tracks, err := s.resolveBackfill(ctx, job.Snapshot())
	if err != nil {
		job.SetError(err.Error())
		return
	}

	if meta == nil {
		meta = &resolver.ReleaseMetadata{}
	}
	if job.AlbumTitle != nil {
		meta.AlbumTitle = *job.AlbumTitle
	}
	if job.ArtistName != nil {
		meta.AlbumArtist = *job.ArtistName
	}

	if len(tracks) == 0 {
		job.SetError("Не удалось найти недостающие треки")
		return
	}

	job.SetMetadata(*meta, tracks)

	rawDir := ""
	convertedDir := ""
	if s.tmpSandbox != nil {
		var err error
		rawDir, err = s.tmpSandbox.Resolve(filepath.Join(job.ID, "raw"), true)
		if err != nil {
			job.SetError(fmt.Sprintf("Песочница: %v", err))
			return
		}
		convertedDir, err = s.tmpSandbox.Resolve(filepath.Join(job.ID, "converted"), true)
		if err != nil {
			job.SetError(fmt.Sprintf("Песочница: %v", err))
			return
		}
		_ = safefs.MkdirAll(rawDir, 0700)
		_ = safefs.MkdirAll(convertedDir, 0700)
	}

	s.processTracks(ctx, job, meta, tracks, rawDir, convertedDir)
}
