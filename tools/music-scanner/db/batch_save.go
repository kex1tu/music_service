package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var artistSeparatorRegex = regexp.MustCompile(`(?i)(\\\\+|\x00|/|;|,|\||\s+feat\.?\s+|\s+ft\.?\s+|\s+&\s+|\s+with\s+)`)
var cleanMetaRegex = regexp.MustCompile(`[\p{P}\s_]+`)
var cyrillicRegex = regexp.MustCompile(`\p{Cyrillic}`)

func extractPrimaryArtist(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := artistSeparatorRegex.Split(raw, 2)
	if len(parts) > 0 {
		return strings.TrimSpace(parts[0])
	}
	return raw
}

func cleanMetaString(s string) string {
	s = strings.ToLower(s)
	s = cleanMetaRegex.ReplaceAllString(s, "")
	return s
}

// SaveTrackBatch атомарно сохраняет пакет треков в БД, обновляя таблицы artists, albums, tracks, genres и audit_issues.
func (db *DB) SaveTrackBatch(records []TrackAuditRecord) error {
	if len(records) == 0 {
		return nil
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	artistCache := make(map[string]int64)
	albumCache := make(map[string]int64)
	genreCache := make(map[string]int64)

	for _, r := range records {
		// 1. Определение Album Artist и Track Artist
		albumArtistName := strings.TrimSpace(r.AlbumArtist)
		if albumArtistName == "" {
			albumArtistName = extractPrimaryArtist(r.Artist)
		}
		if albumArtistName == "" {
			albumArtistName = "Unknown Artist"
		}

		albumArtistID, ok := artistCache[albumArtistName]
		if !ok {
			err = tx.QueryRow("SELECT id FROM artists WHERE name = ?", albumArtistName).Scan(&albumArtistID)
			if err == sql.ErrNoRows {
				res, err := tx.Exec("INSERT INTO artists (name, clean_name) VALUES (?, ?)", albumArtistName, cleanMetaString(albumArtistName))
				if err != nil {
					return fmt.Errorf("failed to insert album artist %s: %w", albumArtistName, err)
				}
				albumArtistID, _ = res.LastInsertId()
			} else if err != nil {
				return err
			}
			artistCache[albumArtistName] = albumArtistID
		}

		trackArtistName := strings.TrimSpace(r.Artist)
		if trackArtistName == "" {
			trackArtistName = albumArtistName
		}
		trackArtistID := albumArtistID
		if trackArtistName != albumArtistName {
			var ok bool
			trackArtistID, ok = artistCache[trackArtistName]
			if !ok {
				err = tx.QueryRow("SELECT id FROM artists WHERE name = ?", trackArtistName).Scan(&trackArtistID)
				if err == sql.ErrNoRows {
					res, err := tx.Exec("INSERT INTO artists (name, clean_name) VALUES (?, ?)", trackArtistName, r.CleanArtist)
					if err != nil {
						return fmt.Errorf("failed to insert track artist %s: %w", trackArtistName, err)
					}
					trackArtistID, _ = res.LastInsertId()
				} else if err != nil {
					return err
				}
				artistCache[trackArtistName] = trackArtistID
			}
		}

		// 2. Определение каталога и альбома
		folderPath := filepath.Dir(r.RelPath)
		baseFolder := filepath.Base(folderPath)
		baseLower := strings.ToLower(baseFolder)
		if strings.HasPrefix(baseLower, "cd") || strings.HasPrefix(baseLower, "disc") || strings.HasPrefix(baseLower, "диск") {
			folderPath = filepath.Dir(folderPath)
		}
		if folderPath == "." {
			folderPath = ""
		}

		albumTitle := strings.TrimSpace(r.Album)
		if albumTitle == "" {
			if folderPath != "" {
				albumTitle = filepath.Base(folderPath)
			} else {
				albumTitle = "Unknown Album"
			}
		}

		var albumKey string
		if folderPath != "" {
			albumKey = fmt.Sprintf("folder:%s", folderPath)
		} else {
			albumKey = fmt.Sprintf("artist_album:%d:%s", albumArtistID, albumTitle)
		}

		albumID, ok := albumCache[albumKey]
		if !ok {
			if folderPath != "" {
				err = tx.QueryRow("SELECT id FROM albums WHERE folder_path = ? LIMIT 1", folderPath).Scan(&albumID)
			} else {
				err = sql.ErrNoRows
			}
			if err == sql.ErrNoRows {
				err = tx.QueryRow("SELECT id FROM albums WHERE artist_id = ? AND title = ? LIMIT 1", albumArtistID, albumTitle).Scan(&albumID)
			}

			if err == sql.ErrNoRows {
				res, err := tx.Exec(`
					INSERT INTO albums (
						artist_id, title, clean_title, album_artist, year,
						total_tracks, cover_path, has_cover, folder_path, is_path_valid
					) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				`, albumArtistID, albumTitle, r.CleanAlbum, albumArtistName, r.Year,
					r.TrackTotal, r.CoverPath, r.HasCover, folderPath, r.IsPathValid)
				if err != nil {
					return fmt.Errorf("failed to insert album %s: %w", albumTitle, err)
				}
				albumID, _ = res.LastInsertId()
			} else if err != nil {
				return err
			} else {
				_, _ = tx.Exec(`
					UPDATE albums SET
						has_cover = CASE WHEN ? THEN 1 ELSE has_cover END,
						cover_path = COALESCE(NULLIF(?, ''), cover_path),
						total_tracks = CASE WHEN ? > total_tracks THEN ? ELSE total_tracks END,
						year = COALESCE(NULLIF(?, 0), year),
						album_artist = COALESCE(NULLIF(?, ''), album_artist)
					WHERE id = ?
				`, r.HasCover, r.CoverPath, r.TrackTotal, r.TrackTotal, r.Year, albumArtistName, albumID)
			}
			albumCache[albumKey] = albumID
		}

		// 3. Track
		var trackID int64
		err = tx.QueryRow(`
			INSERT INTO tracks (
				album_id, artist_id, title, clean_title, track_number, track_total,
				disc_number, disc_total, duration, bitrate, file_path, file_size,
				mtime, has_embedded_cover, lyrics_status, lyrics_path,
				is_path_valid, path_violation_reason, suggested_path, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(file_path) DO UPDATE SET
				album_id = excluded.album_id,
				artist_id = excluded.artist_id,
				title = excluded.title,
				clean_title = excluded.clean_title,
				track_number = excluded.track_number,
				track_total = excluded.track_total,
				disc_number = excluded.disc_number,
				disc_total = excluded.disc_total,
				duration = excluded.duration,
				bitrate = excluded.bitrate,
				file_size = excluded.file_size,
				mtime = excluded.mtime,
				has_embedded_cover = excluded.has_embedded_cover,
				lyrics_status = excluded.lyrics_status,
				lyrics_path = excluded.lyrics_path,
				is_path_valid = excluded.is_path_valid,
				path_violation_reason = excluded.path_violation_reason,
				suggested_path = excluded.suggested_path,
				updated_at = CURRENT_TIMESTAMP
			RETURNING id
		`, albumID, trackArtistID, r.Title, r.CleanTitle, r.TrackNumber, r.TrackTotal,
			r.DiscNumber, r.DiscTotal, r.Duration, r.Bitrate, r.RelPath, r.FileSize,
			r.Mtime, r.HasCover, r.LyricsStatus, r.LyricsPath,
			r.IsPathValid, r.PathViolationReason, r.SuggestedPath).Scan(&trackID)
		if err != nil {
			return fmt.Errorf("failed to upsert track %s: %w", r.RelPath, err)
		}

		// Очищаем предыдущие связи трека с жанрами и неактуальные audit_issues
		_, _ = tx.Exec("DELETE FROM track_genres WHERE track_id = ?", trackID)
		_, _ = tx.Exec("DELETE FROM audit_issues WHERE entity_id = ? AND entity_type = 'track'", trackID)

		// 4. Genre
		genreName := strings.TrimSpace(r.Genre)
		if genreName != "" {
			genreID, ok := genreCache[genreName]
			if !ok {
				err = tx.QueryRow("SELECT id FROM genres WHERE name = ?", genreName).Scan(&genreID)
				if err == sql.ErrNoRows {
					isValidEn := !r.GenreIsNonEn
					res, err := tx.Exec("INSERT INTO genres (name, is_valid_en, canonical_en_name) VALUES (?, ?, ?)",
						genreName, isValidEn, r.GenreCanonical)
					if err != nil {
						return fmt.Errorf("failed to insert genre %s: %w", genreName, err)
					}
					genreID, _ = res.LastInsertId()
				} else if err == nil {
					_, _ = tx.Exec("UPDATE genres SET is_valid_en = ?, canonical_en_name = ? WHERE id = ?",
						!r.GenreIsNonEn, r.GenreCanonical, genreID)
				} else if err != nil {
					return err
				}
				genreCache[genreName] = genreID
			}
			_, _ = tx.Exec("INSERT OR IGNORE INTO track_genres (track_id, genre_id) VALUES (?, ?)", trackID, genreID)
		}

		// 5. Audit Issues logging
		if !r.IsPathValid {
			_, _ = tx.Exec(`
				INSERT INTO audit_issues (entity_type, entity_id, issue_type, severity, description, suggested_action)
				VALUES ('track', ?, 'invalid_path', 'warning', ?, ?)
			`, trackID, r.PathViolationReason, "Переместить в "+r.SuggestedPath)
		}
		if r.GenreIsNonEn {
			isAutoFixed := 0
			if r.GenreIsAutoFixed {
				isAutoFixed = 1
			}
			_, _ = tx.Exec(`
				INSERT INTO audit_issues (entity_type, entity_id, issue_type, severity, description, suggested_action, is_auto_fixed, is_resolved)
				VALUES ('track', ?, 'non_en_genre', 'warning', ?, ?, ?, ?)
			`, trackID, "Русскоязычный жанр: "+r.Genre, "Заменить на "+r.GenreCanonical, isAutoFixed, isAutoFixed)
		}
		if r.LyricsStatus == "none" {
			_, _ = tx.Exec(`
				INSERT INTO audit_issues (entity_type, entity_id, issue_type, severity, description, suggested_action)
				VALUES ('track', ?, 'missing_lyrics', 'info', 'Отсутствует текст песни (.lrc/.txt)', 'Скачать через LRCLIB')
			`, trackID)
		}
		if !r.HasCover {
			_, _ = tx.Exec(`
				INSERT INTO audit_issues (entity_type, entity_id, issue_type, severity, description, suggested_action)
				VALUES ('track', ?, 'missing_cover', 'info', 'Отсутствует графическая обложка', 'Загрузить обложку альбома')
			`, trackID)
		}
	}

	// 6. Обновляем счетчики треков и статус полноты для затронутых альбомов
	for _, albumID := range albumCache {
		_, _ = tx.Exec(`
			UPDATE albums SET
				present_tracks_count = (SELECT COUNT(*) FROM tracks WHERE album_id = ?),
				total_tracks = CASE
					WHEN total_tracks <= 0 THEN COALESCE((SELECT MAX(track_total) FROM tracks WHERE album_id = ?), 0)
					ELSE total_tracks
				END,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, albumID, albumID, albumID)

		_, _ = tx.Exec(`
			UPDATE albums SET
				is_complete = CASE
					WHEN (SELECT COUNT(*) FROM missing_tracks WHERE album_id = ?) > 0 THEN 0
					WHEN total_tracks > 1 AND present_tracks_count < total_tracks THEN 0
					WHEN (total_tracks <= 1 OR total_tracks IS NULL) AND present_tracks_count >= 1 THEN 1
					ELSE 1
				END,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, albumID, albumID)
	}

	// Очищаем осиротевшие альбомы, у которых больше нет треков
	_, _ = tx.Exec("DELETE FROM albums WHERE (SELECT COUNT(*) FROM tracks WHERE album_id = albums.id) = 0")
	_, _ = tx.Exec("DELETE FROM missing_tracks WHERE album_id NOT IN (SELECT id FROM albums)")

	return tx.Commit()
}
