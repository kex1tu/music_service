package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// PrepareOfflineSnapshot merges committed WAL transactions into the existing
// database so the deploy script can copy or replace one standalone file.
// The service must be stopped before calling it and until the operation ends.
func PrepareOfflineSnapshot(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("an absolute database path is required")
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw&_pragma=busy_timeout(5000)"}).String()
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	var mode string
	queryErr := conn.QueryRowContext(ctx, "PRAGMA journal_mode = DELETE").Scan(&mode)
	closeErr := conn.Close()
	if queryErr != nil {
		return fmt.Errorf("checkpoint SQLite database for snapshot: %w", queryErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close SQLite snapshot connection: %w", closeErr)
	}
	if !strings.EqualFold(mode, "delete") {
		return fmt.Errorf("SQLite database is still in journal mode %q", mode)
	}
	return nil
}
