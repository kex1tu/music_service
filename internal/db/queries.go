package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
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

// TrackHash вычисляет стабильный 16-символьный хэш пути к файлу трека.
func TrackHash(relPath string) string {
	clean := filepath.Clean(filepath.ToSlash(relPath))
	h := sha256.Sum256([]byte(clean))
	return hex.EncodeToString(h[:])[:16]
}

// TrackEntity представляет запись трека в SQLite со связанными альбомом и артистом.
type TrackEntity struct {
	ID               int64   `json:"id"`
	AlbumID          int64   `json:"album_id"`
	ArtistID         int64   `json:"artist_id"`
	Title            string  `json:"title"`
	Artist           string  `json:"artist,omitempty"`
	Album            string  `json:"album,omitempty"`
	TrackNumber      int     `json:"track_number"`
	Duration         float64 `json:"duration"`
	Bitrate          int     `json:"bitrate"`
	FilePath         string  `json:"file_path"`
	FileSize         int64   `json:"file_size"`
	FileHash         string  `json:"file_hash,omitempty"`
	HasEmbeddedCover bool    `json:"has_embedded_cover"`
	LyricsStatus     string  `json:"lyrics_status"`
	HasCover         bool    `json:"has_cover"`
	CoverPath        string  `json:"cover_path,omitempty"`
}

// GetTrackByIDOrHash ищет трек по числовому ID, 16-символьному хэшу пути или прямому относительному пути.
func (db *DB) GetTrackByIDOrHash(ctx context.Context, idOrHash string) (*TrackEntity, error) {
	idOrHash = strings.TrimSpace(idOrHash)
	if idOrHash == "" {
		return nil, sql.ErrNoRows
	}

	var idNum int64
	if num, err := strconv.ParseInt(idOrHash, 10, 64); err == nil && num > 0 {
		idNum = num
	}

	query := `
	SELECT t.id, t.album_id, t.artist_id, t.title, ar.name AS artist, al.title AS album,
	       t.track_number, t.duration, t.bitrate, t.file_path, t.file_size,
	       COALESCE(t.file_hash, ''), t.has_embedded_cover, t.lyrics_status,
	       al.has_cover, COALESCE(al.cover_path, '')
	FROM tracks t
	JOIN albums al ON t.album_id = al.id
	JOIN artists ar ON t.artist_id = ar.id
	WHERE (t.id = ? AND ? > 0) OR t.file_hash = ? OR t.file_path = ?
	LIMIT 1;
	`

	var t TrackEntity
	row := db.conn.QueryRowContext(ctx, query, idNum, idNum, idOrHash, idOrHash)
	err := row.Scan(
		&t.ID, &t.AlbumID, &t.ArtistID, &t.Title, &t.Artist, &t.Album,
		&t.TrackNumber, &t.Duration, &t.Bitrate, &t.FilePath, &t.FileSize,
		&t.FileHash, &t.HasEmbeddedCover, &t.LyricsStatus,
		&t.HasCover, &t.CoverPath,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// UpdateTrackHash обновляет file_hash трека в SQLite.
func (db *DB) UpdateTrackHash(ctx context.Context, trackID int64, hash string) error {
	query := `UPDATE tracks SET file_hash = ? WHERE id = ?;`
	_, err := db.conn.ExecContext(ctx, query, hash, trackID)
	return err
}

// SoundCloudCandidateItem представляет сериализуемого кандидата из candidates_json.
type SoundCloudCandidateItem struct {
	URL        string `json:"url"`
	Title      string `json:"title"`
	Uploader   string `json:"uploader,omitempty"`
	Duration   int    `json:"duration"`
	Delta      int    `json:"delta"`
	Score      int    `json:"score"`
	Status     string `json:"status"` // marked, likely, unverified, manual, link_dead
	UploadDate string `json:"upload_date,omitempty"`
}

// SoundCloudCacheEntry представляет запись кэша SoundCloud в SQLite.
type SoundCloudCacheEntry struct {
	CacheKey       string                   `json:"cache_key"`
	TrackTitle     string                   `json:"track_title"`
	ArtistName     string                   `json:"artist_name"`
	SelectedURL    string                   `json:"selected_url"`
	Status         string                   `json:"status"`
	Duration       int                      `json:"duration"`
	IsManual       bool                     `json:"is_manual"`
	CandidatesJSON string                   `json:"candidates_json"`
	Candidates     []SoundCloudCandidateItem `json:"candidates,omitempty"`
	CheckedAt      time.Time                `json:"checked_at"`
	UpdatedAt      time.Time                `json:"updated_at"`
}

// GetSoundCloudCache возвращает кэшированную запись SoundCloud по ключу с учетом TTL.
func (db *DB) GetSoundCloudCache(ctx context.Context, key string, ttlHours int) (*SoundCloudCacheEntry, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	if ttlHours <= 0 {
		ttlHours = 720
	}

	query := `
	SELECT cache_key, track_title, artist_name, selected_url, status, duration, is_manual, candidates_json, checked_at, updated_at
	FROM soundcloud_uncensored_cache
	WHERE cache_key = ? AND (is_manual = 1 OR checked_at > datetime('now', printf('-%d hours', ?)));
	`
	row := db.conn.QueryRowContext(ctx, query, key, ttlHours)
	var e SoundCloudCacheEntry
	err := row.Scan(
		&e.CacheKey, &e.TrackTitle, &e.ArtistName, &e.SelectedURL,
		&e.Status, &e.Duration, &e.IsManual, &e.CandidatesJSON,
		&e.CheckedAt, &e.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if e.CandidatesJSON != "" && e.CandidatesJSON != "[]" {
		_ = json.Unmarshal([]byte(e.CandidatesJSON), &e.Candidates)
	}

	// Если is_manual = 0 и (status = 'link_dead' ИЛИ selected_url = '') -> трактуется как MISS (nil, nil)
	if !e.IsManual && (e.Status == "link_dead" || e.SelectedURL == "") {
		return nil, nil
	}

	return &e, nil
}

// SaveSoundCloudCache сохраняет или обновляет запись автоматического кэша в SQLite.
// Если запись была закреплена вручную (is_manual = 1), автоматическое обновление игнорируется.
func (db *DB) SaveSoundCloudCache(ctx context.Context, entry SoundCloudCacheEntry) error {
	if entry.CandidatesJSON == "" {
		if len(entry.Candidates) > 0 {
			if data, err := json.Marshal(entry.Candidates); err == nil {
				entry.CandidatesJSON = string(data)
			} else {
				entry.CandidatesJSON = "[]"
			}
		} else {
			entry.CandidatesJSON = "[]"
		}
	}

	query := `
	INSERT INTO soundcloud_uncensored_cache (
		cache_key, track_title, artist_name, selected_url, status, duration, is_manual, candidates_json, checked_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	ON CONFLICT(cache_key) DO UPDATE SET
		track_title = excluded.track_title,
		artist_name = excluded.artist_name,
		selected_url = excluded.selected_url,
		status = excluded.status,
		duration = excluded.duration,
		candidates_json = excluded.candidates_json,
		checked_at = CURRENT_TIMESTAMP,
		updated_at = CURRENT_TIMESTAMP
	WHERE soundcloud_uncensored_cache.is_manual = 0;
	`
	_, err := db.conn.ExecContext(ctx, query,
		entry.CacheKey, entry.TrackTitle, entry.ArtistName,
		entry.SelectedURL, entry.Status, entry.Duration,
		entry.IsManual, entry.CandidatesJSON,
	)
	return err
}

// PinSoundCloudVersion фиксирует выбранную вручную версию трека с защитой от перезаписи автоматическим резолвером.
func (db *DB) PinSoundCloudVersion(ctx context.Context, key, artist, title, url string, duration int) error {
	query := `
	INSERT INTO soundcloud_uncensored_cache (
		cache_key, artist_name, track_title, selected_url, duration, status, is_manual, candidates_json, checked_at, updated_at
	) VALUES (?, ?, ?, ?, ?, 'manual', 1, '[]', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	ON CONFLICT(cache_key) DO UPDATE SET
		selected_url = excluded.selected_url,
		duration = excluded.duration,
		is_manual = 1,
		status = 'manual',
		checked_at = CURRENT_TIMESTAMP,
		updated_at = CURRENT_TIMESTAMP;
	`
	_, err := db.conn.ExecContext(ctx, query, key, artist, title, url, duration)
	return err
}

// InvalidateSoundCloudURL обрабатывает недоступность URL (404/410).
// Для ручных версий выставляет status = 'link_dead'.
// Для автоматических версий исключает ссылку из списка кандидатов и ротирует на следующую.
func (db *DB) InvalidateSoundCloudURL(ctx context.Context, key string, failedURL string) (*SoundCloudCacheEntry, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}

	query := `
	SELECT cache_key, track_title, artist_name, selected_url, status, duration, is_manual, candidates_json, checked_at, updated_at
	FROM soundcloud_uncensored_cache
	WHERE cache_key = ?;
	`
	row := db.conn.QueryRowContext(ctx, query, key)
	var entry SoundCloudCacheEntry
	err := row.Scan(
		&entry.CacheKey, &entry.TrackTitle, &entry.ArtistName, &entry.SelectedURL,
		&entry.Status, &entry.Duration, &entry.IsManual, &entry.CandidatesJSON,
		&entry.CheckedAt, &entry.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if entry.CandidatesJSON != "" && entry.CandidatesJSON != "[]" {
		_ = json.Unmarshal([]byte(entry.CandidatesJSON), &entry.Candidates)
	}

	if entry.IsManual {
		entry.Status = "link_dead"
		_, err = db.conn.ExecContext(ctx,
			"UPDATE soundcloud_uncensored_cache SET status = 'link_dead', updated_at = CURRENT_TIMESTAMP WHERE cache_key = ?;",
			key,
		)
		return &entry, err
	}

	var remaining []SoundCloudCandidateItem
	for _, c := range entry.Candidates {
		if c.URL != failedURL && c.URL != "" {
			remaining = append(remaining, c)
		}
	}
	entry.Candidates = remaining

	if len(remaining) > 0 {
		next := remaining[0]
		candJSON, _ := json.Marshal(remaining)
		entry.SelectedURL = next.URL
		entry.Status = next.Status
		entry.Duration = next.Duration
		entry.CandidatesJSON = string(candJSON)

		_, err = db.conn.ExecContext(ctx, `
			UPDATE soundcloud_uncensored_cache
			SET selected_url = ?, status = ?, duration = ?, candidates_json = ?, updated_at = CURRENT_TIMESTAMP
			WHERE cache_key = ?;
		`, entry.SelectedURL, entry.Status, entry.Duration, entry.CandidatesJSON, key)
	} else {
		entry.SelectedURL = ""
		entry.Status = "link_dead"
		entry.CandidatesJSON = "[]"

		_, err = db.conn.ExecContext(ctx, `
			UPDATE soundcloud_uncensored_cache
			SET selected_url = '', status = 'link_dead', candidates_json = '[]', updated_at = CURRENT_TIMESTAMP
			WHERE cache_key = ?;
		`, key)
	}

	return &entry, err
}

