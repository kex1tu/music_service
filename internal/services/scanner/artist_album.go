package scanner

import (
	"context"
	"database/sql"
	"strings"
)

func (s *Scanner) getOrCreateArtist(name string) (int64, error) {
	var id int64
	err := s.database.Conn().QueryRow("SELECT id FROM artists WHERE name = ?", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	cleanName := strings.ToLower(name)
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

func (s *Scanner) getOrCreateAlbum(title string, artistID int64, year int, albumDir string) (int64, error) {
	var id int64
	err := s.database.Conn().QueryRow("SELECT id FROM albums WHERE title = ? AND artist_id = ?", title, artistID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	cleanTitle := strings.ToLower(title)
	res, err := s.database.Conn().Exec(
		"INSERT INTO albums (title, clean_title, artist_id, year, folder_path) VALUES (?, ?, ?, ?, ?)",
		title, cleanTitle, artistID, year, albumDir,
	)
	if err != nil {
		if err2 := s.database.Conn().QueryRow("SELECT id FROM albums WHERE title = ? AND artist_id = ?", title, artistID).Scan(&id); err2 == nil {
			return id, nil
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

	if len(toDelete) > 0 {
		// Clean orphaned albums and artists
		_, _ = s.database.Conn().ExecContext(ctx, "DELETE FROM albums WHERE (SELECT COUNT(*) FROM tracks WHERE album_id = albums.id) = 0")
		_, _ = s.database.Conn().ExecContext(ctx, "DELETE FROM artists WHERE (SELECT COUNT(*) FROM albums WHERE artist_id = artists.id) = 0")
		_, _ = s.database.Conn().ExecContext(ctx, "DELETE FROM missing_tracks WHERE album_id NOT IN (SELECT id FROM albums)")
	}
}
