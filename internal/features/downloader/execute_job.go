package downloader

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"music-manager/internal/core/safefs"
	"music-manager/internal/id3"
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

	var meta *resolver.ReleaseMetadata
	var tracks []resolver.ResolvedTrack

	if s.repo != nil && job.TargetAlbumPath != nil && *job.TargetAlbumPath != "" {
		missing, err := s.repo.GetMissingTracksForAlbum(ctx, *job.TargetAlbumPath)
		if err == nil {
			for _, m := range missing {
				tracks = append(tracks, resolver.ResolvedTrack{
					TrackNumber:    m.TrackNumber,
					Title:          m.Title,
					Artist:         m.Artist,
					Duration:       m.ExpectedDuration,
					AudioSourceURL: m.DeezerURL,
					Status:         "pending",
				})
			}
		}
	}

	if len(tracks) == 0 && s.resolver != nil {
		deezerURL := job.URL
		if deezerURL == "" && job.AlbumTitle != nil && *job.AlbumTitle != "" && s.resolver.Deezer() != nil {
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
				deezerURL = fmt.Sprintf("https://www.deezer.com/album/%d", albums[0].ID)
			}
		}

		if deezerURL != "" {
			m, allTracks, err := s.resolver.Resolve(ctx, deezerURL, "deezer")
			if err == nil && m != nil {
				meta = m
				existingNumbers := make(map[int]bool)
				if s.musicSandbox != nil && job.TargetAlbumPath != nil && *job.TargetAlbumPath != "" {
					if fullPath, err := s.musicSandbox.Resolve(*job.TargetAlbumPath, true); err == nil {
						if entries, err := safefs.ReadDir(fullPath); err == nil {
							for _, e := range entries {
								if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp3") {
									itemFull := filepath.Join(fullPath, e.Name())
									if t, err := id3.ReadTags(itemFull, e.Name()); err == nil && t.TrackNumber > 0 {
										existingNumbers[t.TrackNumber] = true
									}
								}
							}
						}
					}
				}

				for _, t := range allTracks {
					if !existingNumbers[t.TrackNumber] {
						tracks = append(tracks, t)
					}
				}
			}
		}
	}

	snapshot := job.Snapshot()
	if len(snapshot.backfillTrackNumbers) > 0 {
		tracks = slices.DeleteFunc(tracks, func(track resolver.ResolvedTrack) bool {
			return !slices.Contains(snapshot.backfillTrackNumbers, track.TrackNumber)
		})
		if len(snapshot.backfillTrackURLs) == len(snapshot.backfillTrackNumbers) {
			for i := range tracks {
				index := slices.Index(snapshot.backfillTrackNumbers, tracks[i].TrackNumber)
				if source := snapshot.backfillTrackURLs[index]; source != "" && resolver.IsAllowedURL(source) {
					tracks[i].AudioSourceURL = source
				}
			}
		}
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
