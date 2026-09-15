package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

type DB struct {
	conn *sql.DB
	mu   sync.Mutex
}

type FileMtimeEntry struct {
	Mtime float64
	Size  int64
}

type TrackAuditRecord struct {
	RelPath              string  `json:"rel_path"`
	Title                string  `json:"title"`
	CleanTitle           string  `json:"clean_title,omitempty"`
	Artist               string  `json:"artist"`
	CleanArtist          string  `json:"clean_artist,omitempty"`
	Album                string  `json:"album"`
	CleanAlbum           string  `json:"clean_album,omitempty"`
	AlbumArtist          string  `json:"album_artist"`
	Year                 int     `json:"year"`
	TrackNumber          int     `json:"track_number"`
	TrackTotal           int     `json:"track_total"`
	DiscNumber           int     `json:"disc_number"`
	DiscTotal            int     `json:"disc_total"`
	Duration             float64 `json:"duration"`
	Bitrate              int     `json:"bitrate"`
	FileSize             int64   `json:"size"`
	Mtime                float64 `json:"mtime"`
	HasCover             bool    `json:"has_cover"`
	CoverPath            string  `json:"cover_path,omitempty"`
	LyricsStatus         string  `json:"lyrics_status"`
	LyricsPath           string  `json:"lyrics_path,omitempty"`
	IsPathValid          bool    `json:"is_path_valid"`
	PathViolationReason  string  `json:"path_violation_reason,omitempty"`
	SuggestedPath        string  `json:"suggested_path,omitempty"`
	Genre                string  `json:"genre"`
	GenreIsNonEn         bool    `json:"genre_is_non_en"`
	GenreCanonical       string  `json:"genre_canonical,omitempty"`
	GenreIsAutoFixed     bool    `json:"genre_is_auto_fixed"`
}

type MissingTrackRecord struct {
	AlbumID          int64
	Title            string
	Artist           string
	TrackNumber      int
	DiscNumber       int
	ExpectedDuration float64
	DeezerID         string
	DeezerURL        string
	SpotifyID        string
	GeniusURL        string
	ISRC             string
}

// OpenDB открывает и настраивает SQLite базу данных.
func OpenDB(dbPath string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)", dbPath)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)

	if _, err := conn.Exec("PRAGMA journal_mode = WAL; PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;"); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to set pragmas: %w", err)
	}

	if _, err := conn.Exec(DDLSchema); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to apply ddl schema: %w", err)
	}

	return &DB{conn: conn}, nil
}

func (db *DB) Close() error {
	return db.conn.Close()
}

// GetExistingMtimes возвращает снимок существующих путей, mtime и размеров файлов для быстрого диффа.
func (db *DB) GetExistingMtimes() (map[string]FileMtimeEntry, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	rows, err := db.conn.Query("SELECT file_path, mtime, file_size FROM tracks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]FileMtimeEntry)
	for rows.Next() {
		var path string
		var mtime float64
		var size int64
		if err := rows.Scan(&path, &mtime, &size); err != nil {
			return nil, err
		}
		result[path] = FileMtimeEntry{Mtime: mtime, Size: size}
	}
	return result, rows.Err()
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
		// 1. Artist
		artistName := r.Artist
		if strings.TrimSpace(artistName) == "" {
			artistName = "Unknown Artist"
		}
		artistID, ok := artistCache[artistName]
		if !ok {
			err = tx.QueryRow("SELECT id FROM artists WHERE name = ?", artistName).Scan(&artistID)
			if err == sql.ErrNoRows {
				res, err := tx.Exec("INSERT INTO artists (name, clean_name) VALUES (?, ?)", artistName, r.CleanArtist)
				if err != nil {
					return fmt.Errorf("failed to insert artist %s: %w", artistName, err)
				}
				artistID, _ = res.LastInsertId()
			} else if err != nil {
				return err
			}
			artistCache[artistName] = artistID
		}

		// 2. Album
		albumTitle := r.Album
		if strings.TrimSpace(albumTitle) == "" {
			albumTitle = "Unknown Album"
		}
		albumKey := fmt.Sprintf("%d:%s", artistID, albumTitle)
		albumID, ok := albumCache[albumKey]
		if !ok {
			err = tx.QueryRow("SELECT id FROM albums WHERE artist_id = ? AND title = ?", artistID, albumTitle).Scan(&albumID)
			folderPath := filepath.Dir(r.RelPath)
			if err == sql.ErrNoRows {
				res, err := tx.Exec(`
					INSERT INTO albums (
						artist_id, title, clean_title, album_artist, year,
						total_tracks, cover_path, has_cover, folder_path, is_path_valid
					) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				`, artistID, albumTitle, r.CleanAlbum, r.AlbumArtist, r.Year,
					r.TrackTotal, r.CoverPath, r.HasCover, folderPath, r.IsPathValid)
				if err != nil {
					return fmt.Errorf("failed to insert album %s: %w", albumTitle, err)
				}
				albumID, _ = res.LastInsertId()
			} else if err != nil {
				return err
			} else {
				if r.HasCover {
					_, _ = tx.Exec("UPDATE albums SET has_cover = 1, cover_path = ? WHERE id = ?", r.CoverPath, albumID)
				}
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
		`, albumID, artistID, r.Title, r.CleanTitle, r.TrackNumber, r.TrackTotal,
			r.DiscNumber, r.DiscTotal, r.Duration, r.Bitrate, r.RelPath, r.FileSize,
			r.Mtime, r.HasCover, r.LyricsStatus, r.LyricsPath,
			r.IsPathValid, r.PathViolationReason, r.SuggestedPath).Scan(&trackID)
		if err != nil {
			return fmt.Errorf("failed to upsert track %s: %w", r.RelPath, err)
		}

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

	// 6. Обновляем счетчики треков альбомов
	for _, albumID := range albumCache {
		_, _ = tx.Exec(`
			UPDATE albums SET
				present_tracks_count = (SELECT COUNT(*) FROM tracks WHERE album_id = ?),
				is_complete = CASE
					WHEN total_tracks > 1 AND (SELECT COUNT(*) FROM tracks WHERE album_id = ?) >= total_tracks THEN 1
					WHEN (total_tracks <= 1 OR total_tracks IS NULL) AND (SELECT COUNT(*) FROM tracks WHERE album_id = ?) >= 1 THEN 1
					ELSE 0
				END,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, albumID, albumID, albumID, albumID)
	}

	return tx.Commit()
}

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

type IncompleteAlbumInfo struct {
	ID          int64
	Artist      string
	Title       string
	TotalTracks int
	DeezerID    string
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
	if len(tracks) == 0 {
		return nil
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO missing_tracks (
			album_id, title, artist, track_number, disc_number,
			expected_duration, deezer_id, deezer_url, spotify_id,
			genius_url, isrc, status, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'missing', CURRENT_TIMESTAMP)
		ON CONFLICT(album_id, disc_number, track_number) DO UPDATE SET
			title = excluded.title,
			artist = excluded.artist,
			expected_duration = excluded.expected_duration,
			deezer_id = excluded.deezer_id,
			deezer_url = excluded.deezer_url,
			spotify_id = excluded.spotify_id,
			genius_url = excluded.genius_url,
			isrc = excluded.isrc,
			updated_at = CURRENT_TIMESTAMP
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

	return tx.Commit()
}
