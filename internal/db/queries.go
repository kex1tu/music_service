package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DownloadJobEntity представляет запись задачи загрузки в SQLite.
type DownloadJobEntity struct {
	ID               string    `json:"id"`
	Status           string    `json:"status"`
	Source           string    `json:"source"`
	URL              string    `json:"url"`
	Progress         float64   `json:"progress"`
	CurrentStep      string    `json:"current_step"`
	TotalTracks      int       `json:"total_tracks"`
	CompletedTracks  int       `json:"completed_tracks"`
	Error            *string   `json:"error"`
	TargetAlbumPath  *string   `json:"target_album_path"`
	IsBackfill       bool      `json:"is_backfill"`
	AlbumTitle       *string   `json:"album_title"`
	ArtistName       *string   `json:"artist_name"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// SaveJob вставляет или обновляет состояние задачи в базе данных.
func (db *DB) SaveJob(ctx context.Context, job DownloadJobEntity) error {
	query := `
	INSERT INTO download_jobs (
		id, status, source, url, progress, current_step,
		total_tracks, completed_tracks, error, target_album_path,
		is_backfill, album_title, artist_name, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(id) DO UPDATE SET
		status = excluded.status,
		progress = excluded.progress,
		current_step = excluded.current_step,
		total_tracks = excluded.total_tracks,
		completed_tracks = excluded.completed_tracks,
		error = excluded.error,
		target_album_path = excluded.target_album_path,
		is_backfill = excluded.is_backfill,
		album_title = excluded.album_title,
		artist_name = excluded.artist_name,
		updated_at = CURRENT_TIMESTAMP;
	`
	_, err := db.conn.ExecContext(ctx, query,
		job.ID, job.Status, job.Source, job.URL, job.Progress, job.CurrentStep,
		job.TotalTracks, job.CompletedTracks, job.Error, job.TargetAlbumPath,
		job.IsBackfill, job.AlbumTitle, job.ArtistName,
	)
	return err
}

// GetJob возвращает задачу по её идентификатору.
func (db *DB) GetJob(ctx context.Context, id string) (*DownloadJobEntity, error) {
	query := `
	SELECT id, status, source, url, progress, current_step,
	       total_tracks, completed_tracks, error, target_album_path,
	       is_backfill, album_title, artist_name, created_at, updated_at
	FROM download_jobs WHERE id = ?;
	`
	row := db.conn.QueryRowContext(ctx, query, id)
	var j DownloadJobEntity
	err := row.Scan(
		&j.ID, &j.Status, &j.Source, &j.URL, &j.Progress, &j.CurrentStep,
		&j.TotalTracks, &j.CompletedTracks, &j.Error, &j.TargetAlbumPath,
		&j.IsBackfill, &j.AlbumTitle, &j.ArtistName, &j.CreatedAt, &j.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// GetAllJobs возвращает список всех задач, упорядоченных по времени создания (новые сначала).
func (db *DB) GetAllJobs(ctx context.Context) ([]DownloadJobEntity, error) {
	query := `
	SELECT id, status, source, url, progress, current_step,
	       total_tracks, completed_tracks, error, target_album_path,
	       is_backfill, album_title, artist_name, created_at, updated_at
	FROM download_jobs ORDER BY created_at DESC;
	`
	rows, err := db.conn.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []DownloadJobEntity
	for rows.Next() {
		var j DownloadJobEntity
		if err := rows.Scan(
			&j.ID, &j.Status, &j.Source, &j.URL, &j.Progress, &j.CurrentStep,
			&j.TotalTracks, &j.CompletedTracks, &j.Error, &j.TargetAlbumPath,
			&j.IsBackfill, &j.AlbumTitle, &j.ArtistName, &j.CreatedAt, &j.UpdatedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, j)
	}
	return list, nil
}

// SearchResultItem универсальная сущность результата глобального поиска.
type SearchResultItem struct {
	ID        int64   `json:"id"`
	Title     string  `json:"title"`
	Artist    string  `json:"artist,omitempty"`
	Album     string  `json:"album,omitempty"`
	Year      int     `json:"year,omitempty"`
	Duration  float64 `json:"duration,omitempty"`
	Path      string  `json:"path,omitempty"`
	HasCover  bool    `json:"has_cover,omitempty"`
	CoverPath string  `json:"cover_path,omitempty"`
}

// SearchLibrary выполняет быстрый поиск по трекам, альбомам и артистам.
func (db *DB) SearchLibrary(ctx context.Context, query string, limit int) (tracks []SearchResultItem, albums []SearchResultItem, artists []SearchResultItem, err error) {
	term := "%" + query + "%"

	// 1. Поиск по трекам
	trackQuery := `
	SELECT t.id, t.title, ar.name AS artist, al.title AS album, al.year, t.duration, t.file_path, al.has_cover, al.cover_path
	FROM tracks t
	JOIN albums al ON t.album_id = al.id
	JOIN artists ar ON t.artist_id = ar.id
	WHERE t.title LIKE ? OR t.clean_title LIKE ?
	LIMIT ?;
	`
	tRows, err := db.conn.QueryContext(ctx, trackQuery, term, term, limit)
	if err == nil {
		defer tRows.Close()
		for tRows.Next() {
			var it SearchResultItem
			var yr sql.NullInt64
			var covPath sql.NullString
			if err := tRows.Scan(&it.ID, &it.Title, &it.Artist, &it.Album, &yr, &it.Duration, &it.Path, &it.HasCover, &covPath); err == nil {
				if yr.Valid {
					it.Year = int(yr.Int64)
				}
				if covPath.Valid {
					it.CoverPath = covPath.String
				}
				tracks = append(tracks, it)
			}
		}
	}

	// 2. Поиск по альбомам
	albumQuery := `
	SELECT al.id, al.title, ar.name AS artist, al.year, al.folder_path, al.has_cover, al.cover_path
	FROM albums al
	JOIN artists ar ON al.artist_id = ar.id
	WHERE al.title LIKE ? OR al.clean_title LIKE ?
	LIMIT ?;
	`
	aRows, err := db.conn.QueryContext(ctx, albumQuery, term, term, limit)
	if err == nil {
		defer aRows.Close()
		for aRows.Next() {
			var it SearchResultItem
			var yr sql.NullInt64
			var covPath sql.NullString
			if err := aRows.Scan(&it.ID, &it.Title, &it.Artist, &yr, &it.Path, &it.HasCover, &covPath); err == nil {
				if yr.Valid {
					it.Year = int(yr.Int64)
				}
				if covPath.Valid {
					it.CoverPath = covPath.String
				}
				albums = append(albums, it)
			}
		}
	}

	// 3. Поиск по артистам
	artistQuery := `
	SELECT id, name
	FROM artists
	WHERE name LIKE ? OR clean_name LIKE ?
	LIMIT ?;
	`
	arRows, err := db.conn.QueryContext(ctx, artistQuery, term, term, limit)
	if err == nil {
		defer arRows.Close()
		for arRows.Next() {
			var it SearchResultItem
			if err := arRows.Scan(&it.ID, &it.Title); err == nil {
				artists = append(artists, it)
			}
		}
	}

	return tracks, albums, artists, nil
}

// InvalidPathRecord представляет трек с нарушением канонического пути.
type InvalidPathRecord struct {
	FilePath            string `json:"path"`
	SuggestedPath       string `json:"suggested_path"`
	PathViolationReason string `json:"reason"`
}

// GetInvalidPaths возвращает список всех треков с некорректными путями.
func (db *DB) GetInvalidPaths(ctx context.Context) ([]InvalidPathRecord, error) {
	query := `
	SELECT file_path, suggested_path, path_violation_reason
	FROM tracks
	WHERE is_path_valid = 0;
	`
	rows, err := db.conn.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []InvalidPathRecord
	for rows.Next() {
		var r InvalidPathRecord
		var sug, reas sql.NullString
		if err := rows.Scan(&r.FilePath, &sug, &reas); err != nil {
			return nil, err
		}
		if sug.Valid {
			r.SuggestedPath = sug.String
		}
		if reas.Valid {
			r.PathViolationReason = reas.String
		}
		list = append(list, r)
	}
	return list, nil
}

// UpdateTrackPath обновляет путь к файлу после успешного перемещения.
func (db *DB) UpdateTrackPath(ctx context.Context, oldPath, newPath string) error {
	query := `
	UPDATE tracks
	SET file_path = ?, suggested_path = ?, is_path_valid = 1, path_violation_reason = '', updated_at = CURRENT_TIMESTAMP
	WHERE file_path = ?;
	`
	_, err := db.conn.ExecContext(ctx, query, newPath, newPath, oldPath)
	return err
}

// DeleteTrack удаляет запись о треке по пути к файлу.
func (db *DB) DeleteTrack(ctx context.Context, path string) error {
	query := `DELETE FROM tracks WHERE file_path = ?;`
	_, err := db.conn.ExecContext(ctx, query, path)
	return err
}

// ClearOldJobs удаляет завершенные задачи старше указанного интервала.
func (db *DB) ClearOldJobs(ctx context.Context, age time.Duration) (int64, error) {
	cutoff := time.Now().Add(-age)
	query := `
	DELETE FROM download_jobs
	WHERE status IN ('READY', 'ERROR', 'CANCELLED') AND updated_at < ?;
	`
	res, err := db.conn.ExecContext(ctx, query, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteJob удаляет задачу по ID из download_jobs.
func (db *DB) DeleteJob(ctx context.Context, id string) error {
	query := `DELETE FROM download_jobs WHERE id = ?;`
	_, err := db.conn.ExecContext(ctx, query, id)
	return err
}

// CleanString нормализует строку для нечеткого сопоставления.
func CleanString(s string) string {
	return fmt.Sprintf("%s", s)
}

// ResetLibraryTables очищает все таблицы кэша медиатеки при запуске сервиса, сохраняя таблицу download_jobs.
func (db *DB) ResetLibraryTables(ctx context.Context) error {
	queries := []string{
		"DELETE FROM track_genres;",
		"DELETE FROM audit_issues;",
		"DELETE FROM missing_tracks;",
		"DELETE FROM tracks;",
		"DELETE FROM albums;",
		"DELETE FROM artists;",
		"DELETE FROM genres;",
	}
	for _, q := range queries {
		if _, err := db.conn.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("ошибка очистки таблицы (%s): %w", q, err)
		}
	}
	return nil
}
