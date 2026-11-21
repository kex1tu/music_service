package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
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
