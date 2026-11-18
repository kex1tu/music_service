package downloader

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"music-manager/internal/services/resolver"
)

var ErrCorruptJobSelection = errors.New("сохранённый треклист задачи повреждён")

type sourceSelectionPayload struct {
	Tracks               []resolver.ResolvedTrack `json:"tracks"`
	Metadata             resolver.ReleaseMetadata `json:"metadata"`
	SelectedTrackIndices []int                    `json:"selected_track_indices,omitempty"`
	SkipExisting         bool                     `json:"skip_existing,omitempty"`
	ManualTrackIndex     *int                     `json:"manual_track_index,omitempty"`
	BackfillTrackNumbers []int                    `json:"backfill_track_numbers,omitempty"`
	BackfillTrackURLs    []string                 `json:"backfill_track_urls,omitempty"`
}

func saveSourceSelection(ctx context.Context, tx *sql.Tx, job *Job) error {
	raw, err := json.Marshal(sourceSelectionPayload{
		Tracks: job.Tracks, Metadata: job.AlbumMetadata,
		SelectedTrackIndices: job.SelectedTrackIndices, SkipExisting: job.SkipExisting,
		ManualTrackIndex: job.manualTrackIndex, BackfillTrackNumbers: job.backfillTrackNumbers, BackfillTrackURLs: job.backfillTrackURLs,
	})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO download_source_selections(job_id,payload_json) VALUES(?,?)
		ON CONFLICT(job_id) DO UPDATE SET payload_json=excluded.payload_json`, job.ID, string(raw))
	return err
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
	job.SelectedTrackIndices = selection.SelectedTrackIndices
	job.SkipExisting = selection.SkipExisting
	job.manualTrackIndex = selection.ManualTrackIndex
	job.backfillTrackNumbers = selection.BackfillTrackNumbers
	job.backfillTrackURLs = selection.BackfillTrackURLs
}
