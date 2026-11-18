package downloader

import (
	"slices"
	"time"

	"music-manager/internal/services/resolver"
)

func hasManualSource(tracks []resolver.ResolvedTrack) bool {
	return slices.ContainsFunc(tracks, func(track resolver.ResolvedTrack) bool {
		return track.VersionStatus == "manual"
	})
}

func selectManualSource(track *resolver.ResolvedTrack, sourceURL string, policy resolver.VersionPolicy) {
	track.AudioSourceURL = sourceURL
	track.VersionStatus = "manual"
	track.VersionPolicy = policy
	track.Status = "pending"
	track.Error = nil
}

func pinForTrack(track resolver.ResolvedTrack) PinnedTrackSourceDTO {
	var isrc string
	if track.ISRC != nil {
		isrc = *track.ISRC
	}
	return PinnedTrackSourceDTO{
		CacheKey:   resolver.BuildCacheKey(isrc, track.Artist, track.Title),
		ArtistName: track.Artist,
		TrackTitle: track.Title,
		ISRC:       track.ISRC,
		PinnedURL:  track.AudioSourceURL,
		SourceType: "manual",
		Duration:   track.Duration,
	}
}

// Recompute under one lock: concurrent manual track downloads cannot overwrite
// a completed job with a stale partial count.
func (j *Job) reconcileManualSources() {
	j.mu.Lock()
	ready, processing := 0, false
	for _, tr := range j.Tracks {
		if tr.Status == "ready" {
			ready++
		}
		if tr.Status == "downloading" || tr.Status == "converting" || tr.Status == "tagging" {
			processing = true
		}
	}
	j.CompletedTracks = ready
	j.TotalTracks = len(j.Tracks)
	j.Error = nil
	j.UpdatedAt = time.Now().UTC()
	switch {
	case len(j.Tracks) > 0 && ready == len(j.Tracks):
		j.Status = StatusReady
		j.Progress = 100
		j.CurrentStep = "Выбранные источники загружены"
	case processing:
		j.Status = StatusDownloading
	case ready > 0:
		j.Status = StatusPartialReady
		j.CurrentStep = "Часть треков готова; выберите источники остальных"
	default:
		j.Status = StatusWaitingSource
		j.CurrentStep = "Требуется ручной выбор источника"
	}
	j.mu.Unlock()
	j.notifyUpdate()
}
