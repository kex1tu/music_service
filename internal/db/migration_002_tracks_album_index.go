package db

import (
	"context"
	"database/sql"
)

func migrateTracksAlbumIndex(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "CREATE INDEX idx_tracks_album ON tracks(album_id)")
	return err
}
