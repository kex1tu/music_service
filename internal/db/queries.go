package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DownloadJobEntity представляет запись задачи загрузки в SQLite.
type DownloadJobEntity struct {
	ID              string    `json:"id"`
	Status          string    `json:"status"`
	Source          string    `json:"source"`
	URL             string    `json:"url"`
	Progress        float64   `json:"progress"`
	CurrentStep     string    `json:"current_step"`
	TotalTracks     int       `json:"total_tracks"`
	CompletedTracks int       `json:"completed_tracks"`
	Error           *string   `json:"error"`
	TargetAlbumPath *string   `json:"target_album_path"`
	IsBackfill      bool      `json:"is_backfill"`
	AlbumTitle      *string   `json:"album_title"`
	ArtistName      *string   `json:"artist_name"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// SaveJob вставляет или обновляет состояние задачи в базе данных.
func (db *DB) SaveJob(ctx context.Context, job DownloadJobEntity) error {
	query := `
	INSERT INTO download_jobs (
		id, status, source, url, progress, current_step,
		total_tracks, completed_tracks, error, target_album_path,
		is_backfill, album_title, artist_name, updated_at
	) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, CURRENT_TIMESTAMP)
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
		updated_at = CURRENT_TIMESTAMP
	WHERE download_jobs.selection_json IS NULL AND download_jobs.backfill_selection IS NULL;
	`
	result, err := db.conn.ExecContext(ctx, query,
		job.ID, job.Status, job.Source, job.URL, job.Progress, job.CurrentStep,
		job.TotalTracks, job.CompletedTracks, job.Error, job.TargetAlbumPath,
		job.IsBackfill, job.AlbumTitle, job.ArtistName,
	)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return fmt.Errorf("job %q has a selection snapshot and requires the downloader repository", job.ID)
	}
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
	Bitrate      int     `json:"-"`
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
	PrimaryArtist  string `json:"-"`
	RawAlbumArtist string `json:"-"`
	ID             int64  `json:"id"`
	Title          string `json:"title"`
	Album          string `json:"album"`
	Artist         string `json:"artist"`
	AlbumArtist    string `json:"album_artist"`
	Year           int    `json:"year,omitempty"`
	Path           string `json:"path"`
	HasCover       bool   `json:"has_cover"`
	CoverPath      string `json:"cover_path,omitempty"`
	TrackCount     int    `json:"track_count"`
}

// SearchArtistItem представляет исполнителя в результатах глобального поиска.
type SearchArtistItem struct {
	CreditFolder string `json:"-"`
	TrackPath    string `json:"-"`
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Title        string `json:"title"`
	TrackCount   int    `json:"track_count"`
	FolderPath   string `json:"folder_path,omitempty"`
	FirstPath    string `json:"first_path,omitempty"`
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
	return QueryLibrarySearch(ctx, db.conn, query, limit, false)
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
	_, err := DeleteLibraryTracks(ctx, db.conn, path, false)
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

// ResetLibraryTables атомарно очищает перестраиваемый индекс медиатеки.
func (db *DB) ResetLibraryTables(ctx context.Context) error {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("не удалось начать очистку кэша: %w", err)
	}
	defer tx.Rollback()

	queries := []string{
		"DELETE FROM track_genres;",
		"DELETE FROM library_artist_conflicts;",
		"DELETE FROM audit_issues;",
		"DELETE FROM release_tracklist;",
		"DELETE FROM tracks;",
		"DELETE FROM albums;",
		"DELETE FROM artists WHERE " + unusedArtist,
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
