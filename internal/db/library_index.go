package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"
)

//go:embed library_index.sql
var libraryIndexSchema string

func initializeLibraryIndex(conn *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, libraryIndexSchema); err != nil {
		return err
	}
	return tx.Commit()
}

type libraryTrackQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// DeleteLibraryTracks removes indexed tracks and updates only their related entities.
func DeleteLibraryTracks(ctx context.Context, conn libraryTrackQuerier, path string, directory bool) (int64, error) {
	if tx, ok := conn.(*sql.Tx); ok {
		return DeleteLibraryPathTx(ctx, tx, path, directory)
	}
	pool, ok := conn.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return 0, fmt.Errorf("library deletion requires a SQLite transaction")
	}
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n, err := DeleteLibraryPathTx(ctx, tx, path, directory)
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// DeleteLibraryPathTx applies the path deletion in the caller's write transaction.
func DeleteLibraryPathTx(ctx context.Context, tx *sql.Tx, path string, directory bool) (int64, error) {
	// '/' sorts immediately before '0' in BINARY collation. The range includes
	// only descendants of this exact directory, even with SQL wildcard characters.
	where := "file_path = ?"
	args := []any{path}
	if directory {
		where += " OR (file_path >= ? AND file_path < ?)"
		args = append(args, path+"/", path+"0")
	}
	return deleteLibraryTracks(ctx, tx, where, args...)
}

// DeleteIndexedTrack conditionally removes the same row observed by the scanner.
func DeleteIndexedTrack(ctx context.Context, conn *sql.DB, id int64, path string) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := deleteLibraryTracks(ctx, tx, "id = ? AND file_path = ?", id, path); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteLibraryTracks(ctx context.Context, tx *sql.Tx, where string, args ...any) (int64, error) {
	rows, err := tx.QueryContext(ctx, "DELETE FROM tracks WHERE "+where+" RETURNING id, album_id, artist_id", args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var ids []int64
	albums := make(map[int64]struct{})
	artists := make(map[int64]struct{})
	for rows.Next() {
		var id, albumID, artistID int64
		if err := rows.Scan(&id, &albumID, &artistID); err != nil {
			return 0, err
		}
		ids = append(ids, id)
		albums[albumID] = struct{}{}
		artists[artistID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, id := range ids {
		if err := deleteLibraryIssues(ctx, tx, "track", id); err != nil {
			return 0, err
		}
	}
	for albumID := range albums {
		if err := RefreshLibraryAlbum(ctx, tx, albumID); err != nil {
			return 0, err
		}
	}
	for artistID := range artists {
		if err := PruneLibraryArtist(ctx, tx, artistID); err != nil {
			return 0, err
		}
	}
	return int64(len(ids)), nil
}

func deleteLibraryIssues(ctx context.Context, tx *sql.Tx, entityType string, id int64) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM audit_issues WHERE entity_type = ? AND entity_id = ?", entityType, id)
	return err
}

// PruneLibraryArtist removes an artist only when both of its library relationships are empty.
func PruneLibraryArtist(ctx context.Context, tx *sql.Tx, id int64) error {
	var deleted int64
	err := tx.QueryRowContext(ctx, `
		DELETE FROM artists WHERE id = ?
		AND NOT EXISTS (SELECT 1 FROM albums WHERE artist_id = artists.id)
		AND NOT EXISTS (SELECT 1 FROM tracks WHERE artist_id = artists.id)
		RETURNING id
	`, id).Scan(&deleted)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return deleteLibraryIssues(ctx, tx, "artist", deleted)
}

// RefreshLibraryAlbum prunes empty albums or updates their aggregate fields.
func RefreshLibraryAlbum(ctx context.Context, tx *sql.Tx, id int64) error {
	var artistID int64
	err := tx.QueryRowContext(ctx, `
		DELETE FROM albums WHERE id = ?
		AND NOT EXISTS (SELECT 1 FROM tracks WHERE album_id = albums.id)
		RETURNING artist_id
	`, id).Scan(&artistID)
	if err == nil {
		if err := deleteLibraryIssues(ctx, tx, "album", id); err != nil {
			return err
		}
		return PruneLibraryArtist(ctx, tx, artistID)
	}
	if err != sql.ErrNoRows {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		WITH summary AS (
			SELECT COUNT(*) AS present, COALESCE(MAX(track_total), 0) AS tagged_total
			FROM tracks WHERE album_id = ?
		)
		UPDATE albums SET
			present_tracks_count = (SELECT present FROM summary),
			total_tracks = MAX(COALESCE(total_tracks, 0), (SELECT tagged_total FROM summary)),
			is_complete = NOT EXISTS (SELECT 1 FROM missing_tracks WHERE album_id = albums.id)
				AND MAX(COALESCE(total_tracks, 0), (SELECT tagged_total FROM summary)) <= MAX(1, (SELECT present FROM summary)),
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, id, id)
	return err
}

// PrepareLibraryTrackUpdate locates the previous relationships and, for a
// known move, transfers the original row to the new path before its UPSERT.
func PrepareLibraryTrackUpdate(ctx context.Context, tx *sql.Tx, oldPath, newPath string, merged bool) (albumID, artistID int64, err error) {
	if oldPath == "" || oldPath == newPath {
		// Acquire the SQLite writer before reading rows. A deferred transaction
		// that starts with a read cannot upgrade after another writer commits.
		_, err = tx.ExecContext(ctx, "UPDATE tracks SET file_path = file_path WHERE 0")
		if err != nil {
			return 0, 0, err
		}
	} else {
		if merged {
			// The destination is the surviving file. Transfer the source row
			// only when the destination did not already have an identity.
			_, err = deleteLibraryTracks(ctx, tx,
				"file_path = ? AND EXISTS (SELECT 1 FROM tracks WHERE file_path = ?)", oldPath, newPath)
		} else {
			_, err = deleteLibraryTracks(ctx, tx,
				"file_path = ? AND EXISTS (SELECT 1 FROM tracks WHERE file_path = ?)", newPath, oldPath)
		}
		if err != nil {
			return 0, 0, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE tracks SET file_path = ? WHERE file_path = ?", newPath, oldPath); err != nil {
			return 0, 0, err
		}
	}
	err = tx.QueryRowContext(ctx, "SELECT album_id, artist_id FROM tracks WHERE file_path = ?", newPath).Scan(&albumID, &artistID)
	if err == sql.ErrNoRows {
		err = nil
	}
	return albumID, artistID, err
}
