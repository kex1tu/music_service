package downloader

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"music-manager/internal/services/resolver"
)

// JobStatus represents the state of a download job.
type JobStatus string

const (
	StatusQueued      JobStatus = "QUEUED"
	StatusResolving   JobStatus = "RESOLVING"
	StatusDownloading JobStatus = "DOWNLOADING"
	StatusConverting  JobStatus = "CONVERTING"
	StatusTagging     JobStatus = "TAGGING"
	StatusReady       JobStatus = "READY"
	StatusError       JobStatus = "ERROR"
	StatusCancelled   JobStatus = "CANCELLED"
)

// DownloadJob represents an active or completed download task.
type DownloadJob struct {
	mu                sync.RWMutex
	ID                string                   `json:"id"`
	Status            JobStatus                `json:"status"`
	Source            string                   `json:"source"`
	URL               string                   `json:"url"`
	Progress          float64                  `json:"progress"`
	CurrentStep       string                   `json:"current_step"`
	TotalTracks       int                      `json:"total_tracks"`
	CompletedTracks   int                      `json:"completed_tracks"`
	Error             *string                  `json:"error,omitempty"`
	TargetAlbumPath   *string                  `json:"target_album_path,omitempty"`
	IsBackfill        bool                     `json:"is_backfill"`
	AlbumTitle        *string                  `json:"album_title,omitempty"`
	ArtistName        *string                  `json:"artist_name,omitempty"`
	CreatedAt         string                   `json:"created_at"`
	UpdatedAt         time.Time                `json:"-"`
	CoverRelativePath string                   `json:"-"`
	AlbumMetadata     resolver.ReleaseMetadata `json:"-"`
	Tracks            []resolver.ResolvedTrack `json:"-"`
	trackProgress     []float64                `json:"-"`
	cancelFunc        func()                   `json:"-"`
	onUpdate          func()                   `json:"-"`
}

// MarshalJSON safely marshals DownloadJob under a read lock to prevent data races.
func (j *DownloadJob) MarshalJSON() ([]byte, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()

	type Alias DownloadJob
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(j),
	})
}

// Snapshot creates an independent thread-safe copy of the job.
func (j *DownloadJob) Snapshot() DownloadJob {
	j.mu.RLock()
	defer j.mu.RUnlock()

	snap := DownloadJob{
		ID:                j.ID,
		Status:            j.Status,
		Source:            j.Source,
		URL:               j.URL,
		Progress:          j.Progress,
		CurrentStep:       j.CurrentStep,
		TotalTracks:       j.TotalTracks,
		CompletedTracks:   j.CompletedTracks,
		TargetAlbumPath:   j.TargetAlbumPath,
		IsBackfill:        j.IsBackfill,
		AlbumTitle:        j.AlbumTitle,
		ArtistName:        j.ArtistName,
		CreatedAt:         j.CreatedAt,
		UpdatedAt:         j.UpdatedAt,
		CoverRelativePath: j.CoverRelativePath,
		AlbumMetadata:     j.AlbumMetadata,
		cancelFunc:        j.cancelFunc,
	}
	if j.Error != nil {
		errCopy := *j.Error
		snap.Error = &errCopy
	}
	if len(j.Tracks) > 0 {
		snap.Tracks = make([]resolver.ResolvedTrack, len(j.Tracks))
		copy(snap.Tracks, j.Tracks)
	}
	return snap
}

func (j *DownloadJob) SetOnUpdate(fn func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.onUpdate = fn
}

func (j *DownloadJob) notifyUpdate() {
	j.mu.RLock()
	fn := j.onUpdate
	j.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

func (j *DownloadJob) UpdateProgress(progress float64, step string) {
	j.mu.Lock()
	if progress > j.Progress {
		j.Progress = progress
	}
	j.CurrentStep = step
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

func (j *DownloadJob) UpdateTrackProgress(trackIdx int, pct float64, step string) {
	j.mu.Lock()
	if trackIdx >= 0 && trackIdx < len(j.trackProgress) {
		if pct > j.trackProgress[trackIdx] {
			j.trackProgress[trackIdx] = pct
		}
	}
	sum := 0.0
	for _, p := range j.trackProgress {
		sum += p
	}
	n := float64(len(j.trackProgress))
	if n == 0 {
		n = 1
	}
	calcProgress := 10.0 + (sum/n)*0.85
	if calcProgress > j.Progress {
		j.Progress = calcProgress
	}
	if j.Progress > 99.0 && j.Status != StatusReady {
		j.Progress = 99.0
	}
	if len(j.Tracks) > 1 {
		j.CurrentStep = fmt.Sprintf("Загрузка треков (%d/%d готово, %.0f%%)", j.CompletedTracks, len(j.Tracks), j.Progress)
	} else {
		j.CurrentStep = step
	}
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()

	j.notifyUpdate()
}

func (j *DownloadJob) SetStatus(status JobStatus) {
	j.mu.Lock()
	j.Status = status
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

func (j *DownloadJob) SetError(errMsg string) {
	j.mu.Lock()
	j.Status = StatusError
	j.Error = &errMsg
	j.CurrentStep = errMsg
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

func (j *DownloadJob) SetCurrentStep(step string) {
	j.mu.Lock()
	if len(j.Tracks) > 1 && (j.Status == StatusDownloading || j.Status == StatusConverting || j.Status == StatusTagging) {
		j.CurrentStep = fmt.Sprintf("Загрузка треков (%d/%d готово, %.0f%%)", j.CompletedTracks, len(j.Tracks), j.Progress)
	} else {
		j.CurrentStep = step
	}
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

func (j *DownloadJob) SetStepAndStatus(step string, status JobStatus) {
	j.mu.Lock()
	j.Status = status
	if len(j.Tracks) > 1 && (status == StatusDownloading || status == StatusConverting || status == StatusTagging) {
		j.CurrentStep = fmt.Sprintf("Обработка треков (%d/%d готово, %.0f%%)", j.CompletedTracks, len(j.Tracks), j.Progress)
	} else {
		j.CurrentStep = step
	}
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

func (j *DownloadJob) SetReady(completedTracks, total int) {
	j.mu.Lock()
	j.Status = StatusReady
	j.Progress = 100.0
	j.CurrentStep = fmt.Sprintf("Готово к проверке тегов (%d/%d скачано)", completedTracks, total)
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

func (j *DownloadJob) IncrementCompleted(total int) {
	j.mu.Lock()
	j.CompletedTracks++
	calcProgress := 10.0 + (float64(j.CompletedTracks)/float64(total))*85.0
	if calcProgress > j.Progress {
		j.Progress = calcProgress
	}
	if total > 1 {
		j.CurrentStep = fmt.Sprintf("Загрузка треков (%d/%d готово, %.0f%%)", j.CompletedTracks, total, j.Progress)
	}
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

func (j *DownloadJob) SetMetadata(meta resolver.ReleaseMetadata, tracks []resolver.ResolvedTrack) {
	j.mu.Lock()
	j.AlbumMetadata = meta
	j.Tracks = tracks
	j.TotalTracks = len(tracks)
	j.trackProgress = make([]float64, len(tracks))
	if meta.AlbumTitle != "" {
		aTitle := meta.AlbumTitle
		j.AlbumTitle = &aTitle
	}
	if meta.AlbumArtist != "" {
		aArtist := meta.AlbumArtist
		j.ArtistName = &aArtist
	}
	j.UpdatedAt = time.Now().UTC()
	j.mu.Unlock()
	j.notifyUpdate()
}

func (j *DownloadJob) SetCoverPath(relCover string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.CoverRelativePath = relCover
	j.UpdatedAt = time.Now().UTC()
}

func (j *DownloadJob) SetCancelFunc(cancel func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cancelFunc = cancel
}

func (j *DownloadJob) Cancel() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cancelFunc != nil {
		j.cancelFunc()
	}
}

func (j *DownloadJob) UpdateTrack(index int, updateFn func(t *resolver.ResolvedTrack)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if index >= 0 && index < len(j.Tracks) {
		updateFn(&j.Tracks[index])
	}
}
