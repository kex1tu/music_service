package downloader

import (
	"encoding/json"
	"errors"

	"music-manager/internal/services/resolver"
)

var ErrCorruptJobSelection = errors.New("сохранённый треклист задачи повреждён")

type sourceSelectionPayload struct {
	Tracks               []resolver.ResolvedTrack `json:"tracks"`
	Metadata             resolver.ReleaseMetadata `json:"metadata"`
	CoverRelativePath    string                   `json:"cover_relative_path,omitempty"`
	SelectedTrackIndices []int                    `json:"selected_track_indices,omitempty"`
	SkipExisting         bool                     `json:"skip_existing,omitempty"`
	ManualTrackIndex     *int                     `json:"manual_track_index,omitempty"`
	BackfillTrackNumbers []int                    `json:"backfill_track_numbers,omitempty"`
	BackfillTrackURLs    []string                 `json:"backfill_track_urls,omitempty"`
	RedownloadTargets    []RedownloadTarget       `json:"redownload_targets,omitempty"`
}

func marshalSourceSelection(job *Job) ([]byte, error) {
	return json.Marshal(sourceSelectionPayload{
		Tracks: job.Tracks, Metadata: job.AlbumMetadata, CoverRelativePath: job.CoverRelativePath,
		SelectedTrackIndices: job.SelectedTrackIndices, SkipExisting: job.SkipExisting,
		ManualTrackIndex: job.manualTrackIndex, BackfillTrackNumbers: job.backfillTrackNumbers, BackfillTrackURLs: job.backfillTrackURLs,
		RedownloadTargets: job.RedownloadTargets,
	})
}

func loadSourceSelection(job *Job, raw string) {
	var selection *sourceSelectionPayload
	if err := json.Unmarshal([]byte(raw), &selection); err != nil || selection == nil {
		message := ErrCorruptJobSelection.Error()
		job.Status = StatusError
		job.CurrentStep = message
		job.Error = &message
		job.selectionCorrupt = true
		return
	}
	job.Tracks = selection.Tracks
	job.AlbumMetadata = selection.Metadata
	job.CoverRelativePath = selection.CoverRelativePath
	job.SelectedTrackIndices = selection.SelectedTrackIndices
	job.SkipExisting = selection.SkipExisting
	job.manualTrackIndex = selection.ManualTrackIndex
	job.backfillTrackNumbers = selection.BackfillTrackNumbers
	job.backfillTrackURLs = selection.BackfillTrackURLs
	job.RedownloadTargets = selection.RedownloadTargets
	job.IsRedownload = len(selection.RedownloadTargets) > 0
}
