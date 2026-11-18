package scanner

import (
	"context"
	"database/sql"

	"music-manager/internal/db"
)

func getOrCreateArtist(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	cleanName := db.CleanArtistKey(name)
	if err := db.RegisterTrackedArtist(ctx, tx, name); err != nil {
		return 0, err
	}

	var id int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM artists WHERE name = ?", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	res, err := tx.ExecContext(ctx, "INSERT INTO artists (name, clean_name) VALUES (?, ?)", name, cleanName)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func getOrCreateAlbum(ctx context.Context, tx *sql.Tx, title string, artistID int64, albumArtistName string, year int, albumDir string, hasCover bool) (int64, error) {
	return db.UpsertLocalAlbum(ctx, tx, title, artistID, albumArtistName, year, albumDir, hasCover)
}
