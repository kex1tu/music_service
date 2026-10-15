package scanner

import (
	"context"
	"database/sql"
	"strings"
)

func (s *Scanner) getOrCreateArtist(name string) (int64, error) {
	cleanName := strings.ToLower(name)
	// Автоматический мониторинг артистов медиатеки: регистрация в tracked_artists с сохранением пользовательских настроек
	_, _ = s.database.Conn().Exec(
		"INSERT INTO tracked_artists (name, clean_name, is_followed) VALUES (?, ?, 1) ON CONFLICT(name) DO NOTHING;",
		name, cleanName,
	)

	var id int64
	err := s.database.Conn().QueryRow("SELECT id FROM artists WHERE name = ?", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	res, err := s.database.Conn().Exec("INSERT INTO artists (name, clean_name) VALUES (?, ?)", name, cleanName)
	if err != nil {
		// Concurrent insert race check
		if err2 := s.database.Conn().QueryRow("SELECT id FROM artists WHERE name = ?", name).Scan(&id); err2 == nil {
			return id, nil
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Scanner) getOrCreateAlbum(title string, artistID int64, albumArtistName string, year int, albumDir string, hasCover bool) (int64, error) {
	var id int64
	var err error

	// 1. If albumDir is non-empty, check if an album is already registered for this folder
	if albumDir != "" {
		err = s.database.Conn().QueryRow("SELECT id FROM albums WHERE folder_path = ? LIMIT 1", albumDir).Scan(&id)
	} else {
		err = sql.ErrNoRows
	}

	// 2. If not found by folder_path, find by (title, artist_id)
	if err == sql.ErrNoRows {
		err = s.database.Conn().QueryRow("SELECT id FROM albums WHERE title = ? AND artist_id = ? LIMIT 1", title, artistID).Scan(&id)
	}

	if err == nil {
		// Existing album found, update folder_path if needed and backfill cover/metadata
		if albumDir != "" {
			_, _ = s.database.Conn().Exec(`
				UPDATE albums SET
					folder_path = CASE WHEN folder_path = '' THEN ? ELSE folder_path END,
					has_cover = CASE WHEN ? THEN 1 ELSE has_cover END,
					year = CASE WHEN year = 0 OR year IS NULL THEN ? ELSE year END,
					album_artist = CASE WHEN album_artist = '' OR album_artist IS NULL THEN ? ELSE album_artist END
				WHERE id = ?
			`, albumDir, hasCover, year, albumArtistName, id)
		}
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	cleanTitle := strings.ToLower(title)
	res, err := s.database.Conn().Exec(
		"INSERT INTO albums (title, clean_title, artist_id, album_artist, year, folder_path, has_cover) VALUES (?, ?, ?, ?, ?, ?, ?)",
		title, cleanTitle, artistID, albumArtistName, year, albumDir, hasCover,
	)
	if err != nil {
		if err2 := s.database.Conn().QueryRow("SELECT id FROM albums WHERE title = ? AND artist_id = ? LIMIT 1", title, artistID).Scan(&id); err2 == nil {
			return id, nil
		}
		if albumDir != "" {
			if err3 := s.database.Conn().QueryRow("SELECT id FROM albums WHERE folder_path = ? LIMIT 1", albumDir).Scan(&id); err3 == nil {
				return id, nil
			}
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Scanner) deleteMissingFiles(ctx context.Context, seenPaths map[string]bool) {
	rows, err := s.database.Conn().QueryContext(ctx, "SELECT id, file_path FROM tracks")
	if err != nil {
		return
	}
	defer rows.Close()

	var toDelete []int64
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err == nil {
			if !seenPaths[path] {
				toDelete = append(toDelete, id)
			}
		}
	}

	for _, id := range toDelete {
		_, _ = s.database.Conn().ExecContext(ctx, "DELETE FROM tracks WHERE id = ?", id)
		_, _ = s.database.Conn().ExecContext(ctx, "DELETE FROM audit_issues WHERE entity_type = 'track' AND entity_id = ?", id)
	}

	// Always clean orphaned albums, artists and missing_tracks (e.g. when albums are consolidated)
	_, _ = s.database.Conn().ExecContext(ctx, "DELETE FROM albums WHERE (SELECT COUNT(*) FROM tracks WHERE album_id = albums.id) = 0")
	_, _ = s.database.Conn().ExecContext(ctx, "DELETE FROM artists WHERE (SELECT COUNT(*) FROM albums WHERE artist_id = artists.id) = 0 AND (SELECT COUNT(*) FROM tracks WHERE artist_id = artists.id) = 0")
	_, _ = s.database.Conn().ExecContext(ctx, "DELETE FROM missing_tracks WHERE album_id NOT IN (SELECT id FROM albums)")
}

