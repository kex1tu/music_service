package scanner

import (
	"context"
	"database/sql"

	"music-manager/internal/db"
)

func (s *Scanner) getOrCreateArtist(name string) (int64, error) {
	cleanName := db.CleanArtistKey(name)
	if err := s.database.UpsertTrackedArtist(context.Background(), name); err != nil {
		return 0, err
	}

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
	return db.UpsertLocalAlbum(context.Background(), s.database.Conn(), title, artistID, albumArtistName, year, albumDir, hasCover)
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
