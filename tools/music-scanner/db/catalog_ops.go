package db

import (
	"database/sql"
)

// DeleteMissingFiles удаляет треки, файлы которых больше не существуют на диске.
func (db *DB) DeleteMissingFiles(existingRelPaths map[string]bool) (int, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	rows, err := db.conn.Query("SELECT id, file_path FROM tracks")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var toDelete []int64
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			return 0, err
		}
		if !existingRelPaths[path] {
			toDelete = append(toDelete, id)
		}
	}

	if len(toDelete) == 0 {
		return 0, nil
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	for _, id := range toDelete {
		_, _ = tx.Exec("DELETE FROM tracks WHERE id = ?", id)
		_, _ = tx.Exec("DELETE FROM audit_issues WHERE entity_type = 'track' AND entity_id = ?", id)
	}

	_, _ = tx.Exec("DELETE FROM albums WHERE id NOT IN (SELECT DISTINCT album_id FROM tracks)")
	_, _ = tx.Exec("DELETE FROM artists WHERE id NOT IN (SELECT DISTINCT artist_id FROM albums)")

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return len(toDelete), nil
}

// GetIncompleteAlbums возвращает список неполных альбомов для сверки с каталогом Deezer/Genius.
func (db *DB) GetIncompleteAlbums() ([]IncompleteAlbumInfo, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	rows, err := db.conn.Query(`
		SELECT al.id, ar.name, al.title, COALESCE(al.total_tracks, 0), COALESCE(al.deezer_id, '')
		FROM albums al
		JOIN artists ar ON al.artist_id = ar.id
		WHERE (al.is_complete = 0 AND al.total_tracks > 1)
		   OR (SELECT COUNT(*) FROM missing_tracks WHERE album_id = al.id) > 0
		   OR al.total_tracks = 0
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []IncompleteAlbumInfo
	for rows.Next() {
		var item IncompleteAlbumInfo
		if err := rows.Scan(&item.ID, &item.Artist, &item.Title, &item.TotalTracks, &item.DeezerID); err != nil {
			return nil, err
		}
		res = append(res, item)
	}
	return res, nil
}

// GetAlbumTracks возвращает список существующих локальных треков альбома.
func (db *DB) GetAlbumTracks(albumID int64) ([]TrackAuditRecord, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	rows, err := db.conn.Query(`
		SELECT t.title, ar.name, t.track_number, t.disc_number, t.duration, t.file_path
		FROM tracks t
		JOIN artists ar ON t.artist_id = ar.id
		WHERE t.album_id = ?
		ORDER BY t.disc_number, t.track_number
	`, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tracks []TrackAuditRecord
	for rows.Next() {
		var r TrackAuditRecord
		var dur sql.NullFloat64
		if err := rows.Scan(&r.Title, &r.Artist, &r.TrackNumber, &r.DiscNumber, &dur, &r.RelPath); err != nil {
			return nil, err
		}
		if dur.Valid {
			r.Duration = dur.Float64
		}
		tracks = append(tracks, r)
	}
	return tracks, nil
}

// UpdateAlbumCatalogInfo обновляет метаданные альбома, полученные из внешнего каталога Deezer (общее число треков, ID релиза).
func (db *DB) UpdateAlbumCatalogInfo(albumID int64, totalTracks int, deezerID string, coverURL string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`
		UPDATE albums SET
			total_tracks = CASE WHEN total_tracks <= 0 THEN ? ELSE total_tracks END,
			deezer_id = COALESCE(NULLIF(deezer_id, ''), ?),
			is_complete = CASE
				WHEN ? > 0 AND (SELECT COUNT(*) FROM tracks WHERE album_id = ?) >= ? THEN 1
				WHEN total_tracks <= 1 AND (SELECT COUNT(*) FROM tracks WHERE album_id = ?) >= 1 THEN 1
				ELSE 0
			END,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, totalTracks, deezerID, totalTracks, albumID, totalTracks, albumID, albumID)
	return err
}

// SaveMissingTracks сохраняет найденные отсутствующие треки в таблицу missing_tracks.
func (db *DB) SaveMissingTracks(albumID int64, tracks []MissingTrackRecord) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Всегда очищаем устаревшие записи missing_tracks для этого альбома
	if _, err := tx.Exec("DELETE FROM missing_tracks WHERE album_id = ?", albumID); err != nil {
		return err
	}

	if len(tracks) > 0 {
		stmt, err := tx.Prepare(`
			INSERT INTO missing_tracks (
				album_id, title, artist, track_number, disc_number,
				expected_duration, deezer_id, deezer_url, spotify_id,
				genius_url, isrc, status, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'missing', CURRENT_TIMESTAMP)
		`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, t := range tracks {
			_, err := stmt.Exec(
				albumID, t.Title, t.Artist, t.TrackNumber, t.DiscNumber,
				t.ExpectedDuration, t.DeezerID, t.DeezerURL, t.SpotifyID,
				t.GeniusURL, t.ISRC,
			)
			if err != nil {
				return err
			}
		}

		_, _ = tx.Exec("UPDATE albums SET is_complete = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?", albumID)
	} else {
		_, _ = tx.Exec(`
			UPDATE albums SET
				is_complete = CASE
					WHEN total_tracks > 1 AND (SELECT COUNT(*) FROM tracks WHERE album_id = ?) < total_tracks THEN 0
					ELSE 1
				END,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, albumID, albumID)
	}

	return tx.Commit()
}
