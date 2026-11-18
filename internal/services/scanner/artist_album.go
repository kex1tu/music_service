package scanner

import (
	"context"
	"database/sql"
	"strings"

	"music-manager/internal/id3"
)

func getOrCreateArtist(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	cleanName := strings.ToLower(name)
	// Автоматический мониторинг артистов медиатеки: регистрация в tracked_artists с сохранением пользовательских настроек
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO tracked_artists (name, clean_name, is_followed) VALUES (?, ?, 1) ON CONFLICT(name) DO NOTHING;",
		name, cleanName,
	); err != nil {
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
	var id int64
	var err error

	// 1. If albumDir is non-empty, check if an album is already registered for this folder
	if albumDir != "" {
		err = tx.QueryRowContext(ctx, "SELECT id FROM albums WHERE folder_path = ? LIMIT 1", albumDir).Scan(&id)
	} else {
		err = sql.ErrNoRows
	}

	// 2. If not found by folder_path, find by (title, artist_id)
	if err == sql.ErrNoRows {
		err = tx.QueryRowContext(ctx, "SELECT id FROM albums WHERE title = ? AND artist_id = ? LIMIT 1", title, artistID).Scan(&id)
	}

	if err == nil {
		// Existing album found, update folder_path if needed and backfill cover/metadata
		if albumDir != "" {
			if _, err := tx.ExecContext(ctx, `
				UPDATE albums SET
					folder_path = CASE WHEN folder_path = '' THEN ? ELSE folder_path END,
					has_cover = CASE WHEN ? THEN 1 ELSE has_cover END,
					year = CASE WHEN year = 0 OR year IS NULL THEN ? ELSE year END,
					album_artist = CASE WHEN album_artist = '' OR album_artist IS NULL THEN ? ELSE album_artist END
				WHERE id = ?
			`, albumDir, hasCover, year, albumArtistName, id); err != nil {
				return 0, err
			}
		}
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	cleanTitle := id3.CleanReleaseTitle(title)
	if cleanTitle == "" {
		cleanTitle = strings.ToLower(title)
	}
	res, err := tx.ExecContext(ctx,
		"INSERT INTO albums (title, clean_title, artist_id, album_artist, year, folder_path, has_cover) VALUES (?, ?, ?, ?, ?, ?, ?)",
		title, cleanTitle, artistID, albumArtistName, year, albumDir, hasCover,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
