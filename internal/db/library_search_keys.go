package db

import (
	"context"
	"database/sql"
	"fmt"
)

// migrateLibrarySearchKeys materializes the same Unicode keys used for queries.
func migrateLibrarySearchKeys(ctx context.Context, conn *sql.Conn) error {
	for _, table := range []string{"albums", "tracks", "radar_releases"} {
		var ready int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type = 'trigger' AND name = ?", table+"_search_insert").Scan(&ready); err != nil {
			return err
		}
		if ready > 0 {
			continue
		}
		for _, col := range []string{"search_title", "search_clean_title"} {
			var exists int
			if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, col).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				if _, err := conn.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s TEXT", table, col)); err != nil {
					return err
				}
			}
		}
		for _, event := range []struct{ suffix, ddl string }{{"insert", "INSERT"}, {"update", "UPDATE OF title, clean_title"}} {
			query := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %[1]s_search_%[2]s
				AFTER %[3]s ON %[1]s BEGIN
					UPDATE %[1]s SET search_title = music_search_key(new.title),
						search_clean_title = music_search_key(new.clean_title)
					WHERE id = new.id;
				END`, table, event.suffix, event.ddl)
			if _, err := conn.ExecContext(ctx, query); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(`UPDATE %[1]s SET
			search_title = music_search_key(title), search_clean_title = music_search_key(clean_title);
			CREATE INDEX IF NOT EXISTS idx_%[1]s_search_title ON %[1]s(search_title)`, table)); err != nil {
			return err
		}
	}
	return nil
}
