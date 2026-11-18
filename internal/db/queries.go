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

	"music-manager/internal/core/textkey"
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

// SearchTrackItem представляет трек в результатах глобального поиска.
type SearchTrackItem struct {
	ID           int64   `json:"id"`
	Title        string  `json:"title"`
	Artist       string  `json:"artist"`
	Album        string  `json:"album"`
	Year         int     `json:"year,omitempty"`
	Duration     float64 `json:"duration"`
	Path         string  `json:"path"`
	LyricsStatus string  `json:"lyrics_status,omitempty"`
	HasCover     bool    `json:"has_cover"`
	CoverPath    string  `json:"cover_path,omitempty"`
}

// SearchAlbumItem представляет альбом в результатах глобального поиска.
type SearchAlbumItem struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Album       string `json:"album"`
	Artist      string `json:"artist"`
	AlbumArtist string `json:"album_artist"`
	Year        int    `json:"year,omitempty"`
	Path        string `json:"path"`
	HasCover    bool   `json:"has_cover"`
	CoverPath   string `json:"cover_path,omitempty"`
	TrackCount  int    `json:"track_count"`
}

// SearchArtistItem представляет исполнителя в результатах глобального поиска.
type SearchArtistItem struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Title      string `json:"title"`
	TrackCount int    `json:"track_count"`
	FolderPath string `json:"folder_path,omitempty"`
	FirstPath  string `json:"first_path,omitempty"`
}

// SearchLibraryResult объединяет результаты поиска по трекам, альбомам и артистам.
type SearchLibraryResult struct {
	Query        string             `json:"query"`
	Artists      []SearchArtistItem `json:"artists"`
	Albums       []SearchAlbumItem  `json:"albums"`
	Tracks       []SearchTrackItem  `json:"tracks"`
	TotalMatches int                `json:"total_matches"`
}

// SearchResultItem сохранен для обратной совместимости.
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
func (db *DB) SearchLibrary(ctx context.Context, query string, limit int) (*SearchLibraryResult, error) {
	if limit <= 0 {
		limit = 50
	}
	term := textkey.Search(query)

	tracks := make([]SearchTrackItem, 0)
	albums := make([]SearchAlbumItem, 0)
	artists := make([]SearchArtistItem, 0)

	// 1. Поиск по трекам
	trackQuery := `
	SELECT t.id, t.title, ar.name AS artist, al.title AS album, al.year, t.duration, t.file_path, t.lyrics_status, al.has_cover, al.cover_path
	FROM tracks t
	JOIN albums al ON t.album_id = al.id
	JOIN artists ar ON t.artist_id = ar.id
	WHERE instr(t.search_title, ?) > 0 OR instr(t.search_clean_title, ?) > 0
	LIMIT ?;
	`
	tRows, err := db.conn.QueryContext(ctx, trackQuery, term, term, limit)
	if err == nil {
		defer tRows.Close()
		for tRows.Next() {
			var it SearchTrackItem
			var artName sql.NullString
			var albTitle sql.NullString
			var yr sql.NullInt64
			var fPath sql.NullString
			var lyrStatus sql.NullString
			var covPath sql.NullString
			if err := tRows.Scan(&it.ID, &it.Title, &artName, &albTitle, &yr, &it.Duration, &fPath, &lyrStatus, &it.HasCover, &covPath); err == nil {
				if artName.Valid {
					it.Artist = artName.String
				}
				if albTitle.Valid {
					it.Album = albTitle.String
				}
				if yr.Valid {
					it.Year = int(yr.Int64)
				}
				if fPath.Valid {
					it.Path = filepath.ToSlash(fPath.String)
				}
				if lyrStatus.Valid {
					it.LyricsStatus = lyrStatus.String
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
	SELECT al.id, al.title, COALESCE(NULLIF(al.album_artist, ''), ar.name) AS album_artist, al.year, al.folder_path, al.has_cover, al.cover_path,
	       (SELECT COUNT(1) FROM tracks WHERE album_id = al.id) AS track_count
	FROM albums al
	JOIN artists ar ON al.artist_id = ar.id
	WHERE instr(al.search_title, ?) > 0 OR instr(al.search_clean_title, ?) > 0
	LIMIT ?;
	`
	aRows, err := db.conn.QueryContext(ctx, albumQuery, term, term, limit)
	if err == nil {
		defer aRows.Close()
		for aRows.Next() {
			var it SearchAlbumItem
			var albArtist sql.NullString
			var yr sql.NullInt64
			var fPath sql.NullString
			var covPath sql.NullString
			if err := aRows.Scan(&it.ID, &it.Title, &albArtist, &yr, &fPath, &it.HasCover, &covPath, &it.TrackCount); err == nil {
				it.Album = it.Title
				if albArtist.Valid {
					it.AlbumArtist = albArtist.String
					it.Artist = albArtist.String
				}
				if yr.Valid {
					it.Year = int(yr.Int64)
				}
				if fPath.Valid {
					it.Path = filepath.ToSlash(fPath.String)
				}
				if covPath.Valid {
					it.CoverPath = covPath.String
				}
				albums = append(albums, it)
			}
		}
	}

	// 3. Поиск по артистам с подсчетом треков и вычислением пути к каталогу
	artistQuery := `
	SELECT ar.id, ar.name,
	       COUNT(DISTINCT t.id) AS track_count,
	       COALESCE(MIN(al.folder_path), MIN(t.file_path), '') AS first_path
	FROM artists ar
	LEFT JOIN tracks t ON t.artist_id = ar.id
	LEFT JOIN albums al ON al.artist_id = ar.id
	WHERE instr(ar.clean_name, ?) > 0
	GROUP BY ar.id, ar.name
	LIMIT ?;
	`
	arRows, err := db.conn.QueryContext(ctx, artistQuery, term, limit)
	if err == nil {
		defer arRows.Close()
		for arRows.Next() {
			var it SearchArtistItem
			var firstPath sql.NullString
			if err := arRows.Scan(&it.ID, &it.Name, &it.TrackCount, &firstPath); err == nil {
				it.Title = it.Name
				if firstPath.Valid && firstPath.String != "" {
					fp := filepath.ToSlash(firstPath.String)
					it.FirstPath = fp
					parts := strings.Split(fp, "/")
					if len(parts) > 1 {
						it.FolderPath = parts[0]
					} else {
						it.FolderPath = fp
					}
				}
				artists = append(artists, it)
			}
		}
	}

	return &SearchLibraryResult{
		Query:        query,
		Artists:      artists,
		Albums:       albums,
		Tracks:       tracks,
		TotalMatches: len(artists) + len(albums) + len(tracks),
	}, nil
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

// ResetLibraryTables атомарно очищает таблицы кэша медиатеки при запуске сервиса.
func (db *DB) ResetLibraryTables(ctx context.Context) error {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("не удалось начать очистку кэша: %w", err)
	}
	defer tx.Rollback()

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
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("ошибка очистки таблицы (%s): %w", q, err)
		}
	}
	return tx.Commit()
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

// TrackMetadataCache holds cached metadata for quick lookups during directory walks.
type TrackMetadataCache struct {
	TrackNumber  int
	TrackTotal   int
	DiscNumber   int
	DiscTotal    int
	Title        string
	Artist       string
	Album        string
	AlbumArtist  string
	Year         int
	Genre        string
	Duration     int
	HasCover     bool
	LyricsStatus string
}

// GetTracksMetadataCache loads metadata for all tracks from SQLite into a map keyed by slash-normalized file_path.
func (db *DB) GetTracksMetadataCache(ctx context.Context) (map[string]TrackMetadataCache, error) {
	query := `
	SELECT 
		t.file_path, 
		t.track_number, 
		t.track_total,
		t.disc_number,
		t.disc_total,
		t.title, 
		COALESCE(ar.name, ''), 
		COALESCE(al.title, ''), 
		COALESCE(al.album_artist, ''),
		COALESCE(al.year, 0),
		t.duration, 
		(t.has_embedded_cover OR al.has_cover), 
		COALESCE(t.lyrics_status, 'none'),
		COALESCE((SELECT GROUP_CONCAT(g.name, '\\') FROM track_genres tg JOIN genres g ON tg.genre_id = g.id WHERE tg.track_id = t.id), '')
	FROM tracks t
	LEFT JOIN albums al ON t.album_id = al.id
	LEFT JOIN artists ar ON t.artist_id = ar.id;
	`
	rows, err := db.conn.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cache := make(map[string]TrackMetadataCache)
	for rows.Next() {
		var fPath sql.NullString
		var trkNum, trkTot, discNum, discTot, yr sql.NullInt64
		var title, artist, album, albArtist, lyrStatus, genre sql.NullString
		var dur sql.NullFloat64
		var hasCover sql.NullBool

		if err := rows.Scan(&fPath, &trkNum, &trkTot, &discNum, &discTot, &title, &artist, &album, &albArtist, &yr, &dur, &hasCover, &lyrStatus, &genre); err != nil {
			continue
		}
		if !fPath.Valid {
			continue
		}
		cleanPath := filepath.ToSlash(fPath.String)
		cache[cleanPath] = TrackMetadataCache{
			TrackNumber:  int(trkNum.Int64),
			TrackTotal:   int(trkTot.Int64),
			DiscNumber:   int(discNum.Int64),
			DiscTotal:    int(discTot.Int64),
			Title:        title.String,
			Artist:       artist.String,
			Album:        album.String,
			AlbumArtist:  albArtist.String,
			Year:         int(yr.Int64),
			Genre:        genre.String,
			Duration:     int(dur.Float64),
			HasCover:     hasCover.Bool,
			LyricsStatus: lyrStatus.String,
		}
	}
	return cache, nil
}

// GetSetting возвращает строковое значение настройки из таблицы app_settings.
// Если настройка не найдена, возвращает пустую строку и nil error.
func (db *DB) GetSetting(ctx context.Context, key string) (string, error) {
	var val string
	err := db.conn.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key = ?", key).Scan(&val)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return val, nil
}

// SetSetting вставляет или обновляет строковое значение настройки в app_settings.
func (db *DB) SetSetting(ctx context.Context, key, val string) error {
	query := `
	INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP;
	`
	_, err := db.conn.ExecContext(ctx, query, key, val)
	return err
}

// DeleteSetting удаляет настройку по ключу из app_settings.
func (db *DB) DeleteSetting(ctx context.Context, key string) error {
	_, err := db.conn.ExecContext(ctx, "DELETE FROM app_settings WHERE key = ?", key)
	return err
}

// PinnedTrackSource представляет запись постоянной ручной привязки аудио-источника.
type PinnedTrackSource struct {
	CacheKey   string    `json:"cache_key"`
	ArtistName string    `json:"artist_name"`
	TrackTitle string    `json:"track_title"`
	ISRC       *string   `json:"isrc,omitempty"`
	PinnedURL  string    `json:"pinned_url"`
	SourceType string    `json:"source_type"`
	Duration   int       `json:"duration"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// GetPinnedSource возвращает зафиксированный источник трека по ключу.
func (db *DB) GetPinnedSource(ctx context.Context, cacheKey string) (*PinnedTrackSource, error) {
	cacheKey = strings.TrimSpace(cacheKey)
	if cacheKey == "" {
		return nil, nil
	}

	query := `
	SELECT cache_key, artist_name, track_title, isrc, pinned_url, source_type, duration, created_at, updated_at
	FROM pinned_track_sources
	WHERE cache_key = ?;
	`
	row := db.conn.QueryRowContext(ctx, query, cacheKey)
	var p PinnedTrackSource
	var isrc sql.NullString
	err := row.Scan(
		&p.CacheKey, &p.ArtistName, &p.TrackTitle, &isrc,
		&p.PinnedURL, &p.SourceType, &p.Duration,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if isrc.Valid && isrc.String != "" {
		p.ISRC = &isrc.String
	}
	return &p, nil
}

// SavePinnedSource сохраняет постоянную ручную привязку аудио-источника.
func (db *DB) SavePinnedSource(ctx context.Context, pin PinnedTrackSource) error {
	query := `
	INSERT INTO pinned_track_sources (
		cache_key, artist_name, track_title, isrc, pinned_url, source_type, duration, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(cache_key) DO UPDATE SET
		pinned_url = excluded.pinned_url,
		source_type = excluded.source_type,
		duration = excluded.duration,
		updated_at = CURRENT_TIMESTAMP;
	`
	var isrcVal any
	if pin.ISRC != nil && *pin.ISRC != "" {
		isrcVal = *pin.ISRC
	}
	_, err := db.conn.ExecContext(ctx, query,
		pin.CacheKey, pin.ArtistName, pin.TrackTitle, isrcVal,
		pin.PinnedURL, pin.SourceType, pin.Duration,
	)
	return err
}

// UpdateTrackPinnedURL сохраняет URL ручной привязки для трека в таблице tracks.
func (db *DB) UpdateTrackPinnedURL(ctx context.Context, trackID int64, pinnedURL string) error {
	query := `UPDATE tracks SET pinned_source_url = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;`
	_, err := db.conn.ExecContext(ctx, query, pinnedURL, trackID)
	return err
}

// UpdateMissingTrackPinnedURL сохраняет URL ручной привязки для недостающего трека.
func (db *DB) UpdateMissingTrackPinnedURL(ctx context.Context, missingTrackID int64, pinnedURL string) error {
	query := `UPDATE missing_tracks SET pinned_source_url = ? WHERE id = ?;`
	_, err := db.conn.ExecContext(ctx, query, pinnedURL, missingTrackID)
	return err
}

// DeletePinnedSource удаляет ручную привязку аудио-источника по ключу.
func (db *DB) DeletePinnedSource(ctx context.Context, cacheKey string) error {
	query := `DELETE FROM pinned_track_sources WHERE cache_key = ?;`
	_, err := db.conn.ExecContext(ctx, query, cacheKey)
	return err
}


