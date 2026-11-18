package downloader

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"music-manager/internal/core/safefs"
	"music-manager/internal/services/resolver"
)

// executeJob запускает конвейер обработки задачи.
func (s *Service) executeJob(ctx context.Context, job *Job) {
	snap := job.Snapshot()
	if snap.manualTrackIndex != nil {
		s.executeManualTrack(ctx, job, snap)
	} else if job.IsBackfill {
		s.executeBackfill(ctx, job)
	} else {
		s.executeRegular(ctx, job)
	}
}

func (s *Service) executeRegular(ctx context.Context, job *Job) {
	snapshot := job.Snapshot()
	hasManual := hasManualSource(snapshot.Tracks)
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

	selectedIndices := snapshot.SelectedTrackIndices
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
	if snapshot.SkipExisting && !hasManual && s.repo != nil {
		tracks = slices.DeleteFunc(tracks, func(track resolver.ResolvedTrack) bool {
			existing, err := s.repo.CheckTrackInLibrary(ctx, track.Title, track.Artist)
			return err == nil && existing != nil
		})
		if len(tracks) == 0 {
			job.SetReady(0, 0)
			return
		}
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
