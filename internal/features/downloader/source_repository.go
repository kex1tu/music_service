package downloader

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"music-manager/internal/services/resolver"
)

type sourceSelectionPayload struct {
	Tracks   []resolver.ResolvedTrack `json:"tracks"`
	Metadata resolver.ReleaseMetadata `json:"metadata"`
}

func (r *Repository) saveSourceSelections(ctx context.Context, snapshot *Job) error {
	if len(snapshot.Tracks) == 0 {
		return nil
	}
	raw, e := json.Marshal(sourceSelectionPayload{snapshot.Tracks, snapshot.AlbumMetadata})
	if e != nil {
		return e
	}
	_, e = r.db.ExecContext(ctx, "INSERT INTO download_source_selections(job_id,payload_json) VALUES(?,?) ON CONFLICT(job_id) DO UPDATE SET payload_json=excluded.payload_json", snapshot.ID, string(raw))
	return e
}
func (r *Repository) loadSourceSelections(ctx context.Context, j *Job) error {
	var raw string
	e := r.db.QueryRowContext(ctx, "SELECT payload_json FROM download_source_selections WHERE job_id=?", j.ID).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var p sourceSelectionPayload
	if e = json.Unmarshal([]byte(raw), &p); e != nil {
		return e
	}
	j.Tracks = p.Tracks
	j.AlbumMetadata = p.Metadata
	return nil
}
func (r *Repository) loadSelectionsForList(ctx context.Context, list []*Job) ([]*Job, error) {
	for _, j := range list {
		if e := r.loadSourceSelections(ctx, j); e != nil {
			return nil, e
		}
	}
	return list, nil
}
