package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Each index is the source user_version; entries are immutable after release.
var migrations = [...]func(context.Context, *sql.Conn) error{
	migrateUnversioned,
	migrateTracksAlbumIndex,
	migrateLibraryIndex,
	migrateReleaseModel,
	migrateArtistIdentity,
	migrateAlbumArtists,
	migrateTrackSources,
	migrateDownloadQueue,
	migrateOAuthAndRadarSettings,
	migrateRadarReleaseSources,
	migrateRemoveRadio,
	migrateCensorshipHistory,
	migrateAuditConsistency,
}

func (db *DB) migrate() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	conn, err := db.conn.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	// The release migration rebuilds parent tables. Keep foreign-key actions
	// disabled on this pinned connection and validate every version before commit.
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("отключение каскадов на время миграции: %w", err)
	}
	defer func() {
		if _, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON"); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	// Reserve the writer before inspecting the schema so concurrent opens
	// cannot classify the same unversioned database and race to change it.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("начало транзакции миграций: %w", err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")

	var version int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("чтение PRAGMA user_version: %w", err)
	}
	if version < 0 || version > len(migrations) {
		return fmt.Errorf("неподдерживаемая версия схемы user_version=%d (максимальная версия %d)", version, len(migrations))
	}
	if version < 7 {
		if err := db.backupBeforeTrackSources(ctx, conn); err != nil {
			return fmt.Errorf("резервная копия SQLite перед миграцией 7: %w", err)
		}
	}
	for ; version < len(migrations); version++ {
		if err := migrations[version](ctx, conn); err != nil {
			return fmt.Errorf("миграция %d → %d: %w", version, version+1, err)
		}
		if err := checkForeignKeys(ctx, conn); err != nil {
			return fmt.Errorf("миграция %d → %d: %w", version, version+1, err)
		}
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version+1)); err != nil {
			return fmt.Errorf("запись PRAGMA user_version: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("фиксация миграций SQLite: %w", err)
	}
	return nil
}

func (db *DB) backupBeforeTrackSources(ctx context.Context, writer *sql.Conn) error {
	var existing bool
	if err := writer.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name = 'artists')").Scan(&existing); err != nil || !existing {
		return err
	}
	var seq int
	var schema, path string
	if err := writer.QueryRowContext(ctx, "SELECT seq, name, file FROM pragma_database_list WHERE name = 'main'").Scan(&seq, &schema, &path); err != nil || path == "" {
		return err
	}
	backup, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".pre-v7-*.sqlite")
	if err != nil {
		return err
	}
	if err := backup.Close(); err != nil {
		_ = os.Remove(backup.Name())
		return err
	}
	// A separate WAL reader sees the consistent pre-migration state while the
	// writer reserved by BEGIN IMMEDIATE prevents changes to that state.
	source, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?mode=ro")
	if err != nil {
		_ = os.Remove(backup.Name())
		return err
	}
	defer source.Close()
	if _, err := source.ExecContext(ctx, "VACUUM INTO ?", backup.Name()); err != nil {
		_ = os.Remove(backup.Name())
		return err
	}
	return nil
}

func checkForeignKeys(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var key int
		if err := rows.Scan(&table, &rowID, &parent, &key); err != nil {
			return fmt.Errorf("foreign_key_check: %w", err)
		}
		return fmt.Errorf("foreign_key_check: нарушена ссылка %s (rowid %v, FK %d) → %s", table, rowID, key, parent)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	return nil
}
