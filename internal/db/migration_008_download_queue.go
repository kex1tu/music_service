package db

import (
	"context"
	"database/sql"
	"strings"
)

func migrateDownloadQueue(ctx context.Context, conn *sql.Conn) error {
	var trigger string
	if err := conn.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE name = 'link_new_artist'").Scan(&trigger); err != nil {
		return err
	}
	parts := strings.Split(trigger, ";")
	keep := parts[:0]
	for _, part := range parts {
		if !strings.Contains(part, "UPDATE soundcloud_uncensored_cache ") && !strings.Contains(part, "UPDATE download_jobs ") {
			keep = append(keep, part)
		}
	}
	if _, err := conn.ExecContext(ctx, `
		DROP TRIGGER link_new_artist;
		ALTER TABLE radar_releases ADD COLUMN auto_download_enqueued_at DATETIME;
		ALTER TABLE download_jobs ADD COLUMN selection_json TEXT;
		UPDATE download_jobs SET selection_json = (
			SELECT payload_json FROM download_source_selections WHERE job_id = download_jobs.id
		);
		DROP TABLE download_source_selections;
		DROP TABLE soundcloud_uncensored_cache;
	`); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, strings.Join(keep, ";"))
	return err
}
