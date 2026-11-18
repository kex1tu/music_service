package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed schema.sql
var schemaV1 string

func migrateUnversioned(ctx context.Context, conn *sql.Conn) error {
	layout, err := identifyUnversioned(ctx, conn)
	if err != nil {
		return err
	}

	if layout.addAlbumYouTubeID {
		if _, err := conn.ExecContext(ctx, "ALTER TABLE albums ADD COLUMN youtube_id TEXT"); err != nil {
			return fmt.Errorf("добавление albums.youtube_id: %w", err)
		}
	}
	if layout.addTrackPin {
		if _, err := conn.ExecContext(ctx, "ALTER TABLE tracks ADD COLUMN pinned_source_url TEXT"); err != nil {
			return fmt.Errorf("добавление tracks.pinned_source_url: %w", err)
		}
	}
	if layout.rebuildMissing {
		if layout.addMissingUpdatedAt {
			// SQLite permits adding a nullable column to a populated table.
			// The target table below restores DEFAULT CURRENT_TIMESTAMP.
			if _, err := conn.ExecContext(ctx, "ALTER TABLE missing_tracks ADD COLUMN updated_at DATETIME"); err != nil {
				return fmt.Errorf("добавление missing_tracks.updated_at: %w", err)
			}
		}
		if _, err := conn.ExecContext(ctx, `UPDATE missing_tracks
			SET updated_at = COALESCE(NULLIF(created_at, ''), CURRENT_TIMESTAMP)
			WHERE updated_at IS NULL OR updated_at = ''`); err != nil {
			return fmt.Errorf("заполнение missing_tracks.updated_at: %w", err)
		}
		if layout.addMissingPin {
			if _, err := conn.ExecContext(ctx, "ALTER TABLE missing_tracks ADD COLUMN pinned_source_url TEXT"); err != nil {
				return fmt.Errorf("добавление missing_tracks.pinned_source_url: %w", err)
			}
		}
		if _, err := conn.ExecContext(ctx, "ALTER TABLE missing_tracks RENAME TO missing_tracks_v0"); err != nil {
			return fmt.Errorf("подготовка переноса missing_tracks: %w", err)
		}
	}
	if layout.rebuildAudit {
		if _, err := conn.ExecContext(ctx, "ALTER TABLE audit_issues RENAME TO audit_issues_v0"); err != nil {
			return fmt.Errorf("подготовка переноса audit_issues: %w", err)
		}
	}

	if _, err := conn.ExecContext(ctx, schemaV1); err != nil {
		return fmt.Errorf("создание таблиц и индексов: %w", err)
	}

	if layout.rebuildAudit {
		const columns = "id, issue_type, entity_type, entity_id, file_path, details, is_resolved, is_auto_fixed, created_at, updated_at"
		// Preserve the entity reference even when no path was stored. Keep the
		// original details verbatim inside the diagnostic JSON.
		const source = `SELECT id, issue_type, entity_type, entity_id, NULL,
			json_object('description', description, 'severity', severity,
				'suggested_action', suggested_action, 'details', details),
			is_resolved, is_auto_fixed, created_at, created_at
			FROM audit_issues_v0`
		if err := copyMigrationRows(ctx, conn, "audit_issues", columns, source, layout.auditIndexes); err != nil {
			return err
		}
	}
	if layout.rebuildMissing {
		const columns = `id, album_id, title, artist, track_number, disc_number, expected_duration,
			deezer_id, deezer_url, spotify_id, genius_url, isrc, pinned_source_url, status, created_at, updated_at`
		if err := copyMigrationRows(ctx, conn, "missing_tracks", columns,
			"SELECT "+columns+" FROM missing_tracks_v0", layout.missingIndexes); err != nil {
			return err
		}
	}
	return nil
}

func copyMigrationRows(ctx context.Context, conn *sql.Conn, table, columns, source string, indexes []string) error {
	// Table names and SQL projections come exclusively from migration 1.
	oldTable := table + "_v0"
	if _, err := conn.ExecContext(ctx, "INSERT INTO "+table+" ("+columns+") "+source); err != nil {
		return fmt.Errorf("перенос %s: %w", table, err)
	}

	var same bool
	err := conn.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM "+table+
		") = (SELECT count(*) FROM "+oldTable+") AND NOT EXISTS ("+source+
		" EXCEPT SELECT "+columns+" FROM "+table+")").Scan(&same)
	if err != nil {
		return fmt.Errorf("проверка переноса %s: %w", table, err)
	}
	if !same {
		return fmt.Errorf("проверка переноса %s: строки исходной и целевой таблиц различаются", table)
	}

	if err := preserveSequence(ctx, conn, oldTable, table); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "DROP TABLE "+oldTable); err != nil {
		return fmt.Errorf("завершение переноса %s: %w", table, err)
	}
	for _, ddl := range indexes {
		if _, err := conn.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("восстановление индекса %s: %w", table, err)
		}
	}
	return nil
}

func preserveSequence(ctx context.Context, conn *sql.Conn, oldTable, table string) error {
	// Keep the AUTOINCREMENT high-water mark, even if its last rows were removed.
	if _, err := conn.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = max(seq,
			COALESCE((SELECT seq FROM sqlite_sequence WHERE name = ?), 0)) WHERE name = ?`, oldTable, table); err != nil {
		return fmt.Errorf("перенос счётчика ID %s: %w", table, err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO sqlite_sequence(name, seq)
		SELECT ?, seq FROM sqlite_sequence WHERE name = ?
			AND NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = ?);`,
		table, oldTable, table); err != nil {
		return fmt.Errorf("перенос счётчика ID %s: %w", table, err)
	}
	return nil
}
