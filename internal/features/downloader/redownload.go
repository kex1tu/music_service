package downloader

import (
	"context"
	"errors"
	"slices"
	"time"

	"music-manager/internal/services/resolver"
)

type RedownloadTarget struct {
	TrackID         string `json:"track_id"`
	LibraryPath     string `json:"library_path,omitempty"`
	OriginalHash    string `json:"original_hash,omitempty"`
	AlbumID         int64  `json:"album_id,omitempty"`
	CatalogRevision int64  `json:"catalog_revision,omitempty"`
	ResultPath      string `json:"result_path,omitempty"`
	ResultHash      string `json:"result_hash,omitempty"`
	Confirmed       bool   `json:"confirmed,omitempty"`
	Conflict        bool   `json:"conflict,omitempty"`
}

func (s *Service) PrepareRedownload(ctx context.Context, folder string, tracks []resolver.ResolvedTrack, targets []RedownloadTarget, run bool) (*Job, error) {
	if len(tracks) == 0 || len(tracks) != len(targets) {
		return nil, errors.New("нет дорожек для повторной загрузки")
	}
	now := time.Now().UTC()
	job := &Job{
		ID: generateUUID(), Source: "redownload", Status: StatusWaitingSource, CurrentStep: "Выберите источник",
		CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now, IsRedownload: true,
		Tracks: tracks, TotalTracks: len(tracks), RedownloadTargets: targets,
		AlbumTitle: &tracks[0].Album, ArtistName: &tracks[0].AlbumArtist,
	}
	if folder != "" {
		job.TargetAlbumPath = &folder
	}
	job.AlbumMetadata = resolver.ReleaseMetadata{AlbumTitle: tracks[0].Album, AlbumArtist: tracks[0].AlbumArtist, TotalTracks: len(tracks)}
	if run {
		job.URL = tracks[0].AudioSourceURL
		job.resetForQueue("Повторная загрузка ожидает очереди")
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.repo != nil {
		if err := s.repo.CreateJob(ctx, job); err != nil {
			return nil, err
		}
	}
	s.registerJobLocked(job)
	if run {
		s.wakeWorker()
	}
	return job.Snapshot(), nil
}

// ConfirmRedownload serializes import with deletion, retry and source changes.
// Saving a receipt before publishing the file makes confirmation recoverable.
func (s *Service) ConfirmRedownload(ctx context.Context, id string, apply func(*Job, func() error) error) error {
	if _, ok := s.GetJob(id); !ok {
		return ErrJobNotFound
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	job := s.jobs[id]
	if job == nil {
		return ErrJobNotFound
	}
	draft := job.Snapshot()
	if !draft.IsRedownload || (draft.Status != StatusReady && draft.Status != StatusPartialReady) {
		return errSelectionConflict
	}
	return apply(draft, func() error {
		if s.repo != nil {
			if err := s.repo.updateJobWithSelection(ctx, draft, draft.Status); err != nil {
				return err
			}
		}
		job.mu.Lock()
		job.RedownloadTargets = slices.Clone(draft.RedownloadTargets)
		job.mu.Unlock()
		return nil
	})
}
