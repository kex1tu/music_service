package filemanager

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/httputil"
	"music-manager/internal/db"
	"music-manager/internal/features/downloader"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
)

type redownloadResult struct {
	TrackID string `json:"track_id"`
	Path    string `json:"path,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (m *Module) ConfirmRedownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TrackIDs []string `json:"track_ids"`
	}
	if err := httputil.DecodeJSON(r, &req); err != nil || len(req.TrackIDs) == 0 {
		httputil.RespondError(w, http.StatusBadRequest, "Выберите готовые дорожки")
		return
	}
	results, err := m.service.confirmRedownload(r.Context(), chi.URLParam(r, "jobID"), req.TrackIDs)
	if err != nil {
		httputil.RespondError(w, http.StatusConflict, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Service) confirmRedownload(ctx context.Context, jobID string, ids []string) ([]redownloadResult, error) {
	if s.redownloadQueue == nil || s.redownloadOrganizer == nil {
		return nil, errors.New("подтверждение замены недоступно")
	}
	results := make([]redownloadResult, 0, len(ids))
	err := s.redownloadQueue.ConfirmRedownload(ctx, jobID, func(job *downloader.Job, save func() error) error {
		for _, id := range ids {
			result := redownloadResult{TrackID: id}
			i := slices.IndexFunc(job.RedownloadTargets, func(t downloader.RedownloadTarget) bool { return t.TrackID == id })
			j := slices.IndexFunc(job.Tracks, func(t resolver.ResolvedTrack) bool { return t.TrackID == id })
			if i < 0 || j < 0 {
				result.Error = "Дорожка не относится к задаче"
			} else {
				path, err := s.confirmRedownloadTrack(ctx, job, &job.RedownloadTargets[i], job.Tracks[j], save)
				result.Path = path
				if err != nil {
					result.Error = err.Error()
				}
			}
			results = append(results, result)
		}
		return nil
	})
	return results, err
}

func (s *Service) confirmRedownloadTrack(ctx context.Context, job *downloader.Job, target *downloader.RedownloadTarget, track resolver.ResolvedTrack, save func() error) (string, error) {
	if target.Confirmed {
		return target.ResultPath, nil
	}
	if target.Conflict {
		return "", errors.New("в позиции несколько файлов или файл не соответствует треклисту")
	}
	if track.Status != "ready" || track.ConvertedPath == nil {
		return "", errors.New("дорожка ещё не готова")
	}
	folder := ""
	if job.TargetAlbumPath != nil {
		folder = *job.TargetAlbumPath
	}
	verify := func() error {
		repo, ok := s.repo.(redownloadLibrary)
		if !ok || track.TrackNumber <= 0 {
			return nil
		}
		files, err := repo.redownloadPositionFiles(ctx, folder, db.ReleasePosition(track.DiscNumber, track.TrackNumber))
		if err != nil {
			return err
		}
		if len(files) > 1 || (len(files) == 1 && files[0] != target.LibraryPath) {
			return errors.New("файл в этой позиции изменился")
		}
		return nil
	}
	receipt := func(path, hash string) error {
		oldPath, oldHash := target.ResultPath, target.ResultHash
		target.ResultPath, target.ResultHash = path, hash
		if err := save(); err != nil {
			target.ResultPath, target.ResultHash = oldPath, oldHash
			return err
		}
		return nil
	}
	path, err := s.redownloadOrganizer.ApplyRedownload(ctx, organizer.RedownloadImport{
		StagedPath: *track.ConvertedPath, AlbumPath: folder,
		OriginalPath: target.LibraryPath, OriginalHash: target.OriginalHash,
		ResultPath: target.ResultPath, ResultHash: target.ResultHash,
	}, verify, receipt)
	if err != nil {
		return "", err
	}
	target.Confirmed = true
	if err := save(); err != nil {
		target.Confirmed = false
		return "", err
	}
	return path, nil
}
