package downloader

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"music-manager/internal/db"
	"music-manager/internal/id3"
)

var (
	// ErrJobNotFound возвращается, когда задача загрузки не найдена в хранилище.
	ErrJobNotFound = errors.New("download job not found")
)

// DBTX определяет контракт для выполнения SQL-запросов (совместим с *sql.DB, *sql.Tx).
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// JobRepository определяет контракт хранилища задач загрузки и связанных кэшей.
type JobRepository interface {
	CreateJob(ctx context.Context, job *Job) error
	SaveJob(ctx context.Context, job *Job) error
	GetJob(ctx context.Context, id string) (*Job, error)
	ListJobs(ctx context.Context, limit, offset int) ([]*Job, error)
	GetAllJobs(ctx context.Context) ([]*Job, error)
	UpdateJobProgress(ctx context.Context, id string, progress float64, currentStep string) error
	UpdateJobStatus(ctx context.Context, id string, status JobStatus, currentStep string, errMessage *string) error
	DeleteJob(ctx context.Context, id string) error
	ResetInterruptedJobs(ctx context.Context) (int64, error)
	GetActiveJobs(ctx context.Context) ([]*Job, error)
	GetActiveJobCount(ctx context.Context) (int, error)
	ClearFinishedJobs(ctx context.Context, age time.Duration) (int64, error)
	ClearCompletedJobs(ctx context.Context, age time.Duration) (int64, error)
	GetMissingTracksForAlbum(ctx context.Context, folderPath string) ([]MissingTrackInfo, error)
	CheckTrackInLibrary(ctx context.Context, title, artist string) (*ExistingTrackInfo, error)

	GetPinnedSource(ctx context.Context, cacheKey string) (*PinnedTrackSourceDTO, error)
	SavePinnedSource(ctx context.Context, pin PinnedTrackSourceDTO) error
	DeletePinnedSource(ctx context.Context, cacheKey string) error
	UpdateTrackPinnedURL(ctx context.Context, trackID int64, pinnedURL string) error
	GetSoundCloudCache(ctx context.Context, key string, ttlHours int) (*SoundCloudCacheDTO, error)
	PinSoundCloudVersion(ctx context.Context, key, artist, title, url string, duration int) error
	InvalidateSoundCloudURL(ctx context.Context, key string, failedURL string) (*SoundCloudCacheDTO, error)
}

// Repository реализует персистентный слой задач загрузки в SQLite WAL.
type Repository struct {
	db DBTX
}

// NewRepository создает новый экземпляр репозитория Downloader.
func NewRepository(db DBTX) *Repository {
	return &Repository{db: db}
}

func parseTimeField(raw any) (string, time.Time) {
	switch v := raw.(type) {
	case time.Time:
		return v.UTC().Format(time.RFC3339Nano), v.UTC()
	case *time.Time:
		if v != nil {
			return v.UTC().Format(time.RFC3339Nano), v.UTC()
		}
	case string:
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UTC().Format(time.RFC3339Nano), t.UTC()
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.UTC().Format(time.RFC3339Nano), t.UTC()
		}
		if t, err := time.Parse("2006-01-02 15:04:05", v); err == nil {
			return t.UTC().Format(time.RFC3339Nano), t.UTC()
		}
		return v, time.Now().UTC()
	case []byte:
		str := string(v)
		return parseTimeField(str)
	}
	now := time.Now().UTC()
	return now.Format(time.RFC3339Nano), now
}

// scanJob сканирует строку SQL в экземпляр Job.
func scanJob(scanner interface{ Scan(dest ...any) error }) (*Job, error) {
	var j Job
	var statusStr string
	var createdAtRaw, updatedAtRaw any

	err := scanner.Scan(
		&j.ID, &statusStr, &j.Source, &j.URL, &j.Progress, &j.CurrentStep,
		&j.TotalTracks, &j.CompletedTracks, &j.Error, &j.TargetAlbumPath,
		&j.IsBackfill, &j.AlbumTitle, &j.ArtistName, &createdAtRaw, &updatedAtRaw,
	)
	if err != nil {
		return nil, err
	}

	j.Status = JobStatus(statusStr)
	createdAtStr, _ := parseTimeField(createdAtRaw)
	_, updatedAt := parseTimeField(updatedAtRaw)
	j.CreatedAt = createdAtStr
	j.UpdatedAt = updatedAt
	return &j, nil
}

// CreateJob вставляет новую задачу загрузки.
func (r *Repository) CreateJob(ctx context.Context, job *Job) error {
	if job == nil {
		return errors.New("job is nil")
	}
	snap := job.Snapshot()

	query := `
	INSERT INTO download_jobs (
		id, status, source, url, progress, current_step,
		total_tracks, completed_tracks, error, target_album_path,
		is_backfill, album_title, artist_name, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
	`
	_, err := r.db.ExecContext(ctx, query,
		snap.ID, string(snap.Status), snap.Source, snap.URL, snap.Progress, snap.CurrentStep,
		snap.TotalTracks, snap.CompletedTracks, snap.Error, snap.TargetAlbumPath,
		snap.IsBackfill, snap.AlbumTitle, snap.ArtistName,
	)
	return err
}

// SaveJob выполняет атомарный Upsert задачи: вставляет новую запись или обновляет существующую.
func (r *Repository) SaveJob(ctx context.Context, job *Job) error {
	if job == nil {
		return errors.New("job is nil")
	}
	snap := job.Snapshot()

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
	_, err := r.db.ExecContext(ctx, query,
		snap.ID, string(snap.Status), snap.Source, snap.URL, snap.Progress, snap.CurrentStep,
		snap.TotalTracks, snap.CompletedTracks, snap.Error, snap.TargetAlbumPath,
		snap.IsBackfill, snap.AlbumTitle, snap.ArtistName,
	)
	return err
}

// GetJob возвращает задачу по её строковому идентификатору. Если запись отсутствует, возвращает (nil, nil).
func (r *Repository) GetJob(ctx context.Context, id string) (*Job, error) {
	query := `
	SELECT id, status, source, url, progress, current_step,
	       total_tracks, completed_tracks, error, target_album_path,
	       is_backfill, album_title, artist_name, created_at, updated_at
	FROM download_jobs
	WHERE id = ?;
	`
	row := r.db.QueryRowContext(ctx, query, id)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return job, nil
}

// ListJobs возвращает страницу задач, отсортированных по времени создания (новые сначала).
func (r *Repository) ListJobs(ctx context.Context, limit, offset int) ([]*Job, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	query := `
	SELECT id, status, source, url, progress, current_step,
	       total_tracks, completed_tracks, error, target_album_path,
	       is_backfill, album_title, artist_name, created_at, updated_at
	FROM download_jobs
	ORDER BY created_at DESC
	LIMIT ? OFFSET ?;
	`
	rows, err := r.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, j)
	}
	return list, nil
}

// GetAllJobs возвращает полный список всех задач из базы данных.
func (r *Repository) GetAllJobs(ctx context.Context) ([]*Job, error) {
	query := `
	SELECT id, status, source, url, progress, current_step,
	       total_tracks, completed_tracks, error, target_album_path,
	       is_backfill, album_title, artist_name, created_at, updated_at
	FROM download_jobs
	ORDER BY created_at DESC;
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, j)
	}
	return list, nil
}

// UpdateJobProgress обновляет текущий процент выполнения и статусную строку шага.
func (r *Repository) UpdateJobProgress(ctx context.Context, id string, progress float64, currentStep string) error {
	query := `
	UPDATE download_jobs
	SET progress = ?,
	    current_step = ?,
	    updated_at = CURRENT_TIMESTAMP
	WHERE id = ?;
	`
	res, err := r.db.ExecContext(ctx, query, progress, currentStep, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrJobNotFound
	}
	return nil
}

// UpdateJobStatus переводит задачу в новый статус с опциональным текстом ошибки.
func (r *Repository) UpdateJobStatus(ctx context.Context, id string, status JobStatus, currentStep string, errMessage *string) error {
	query := `
	UPDATE download_jobs
	SET status = ?,
	    current_step = ?,
	    error = ?,
	    updated_at = CURRENT_TIMESTAMP
	WHERE id = ?;
	`
	res, err := r.db.ExecContext(ctx, query, string(status), currentStep, errMessage, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrJobNotFound
	}
	return nil
}

// DeleteJob удаляет задачу по идентификатору.
func (r *Repository) DeleteJob(ctx context.Context, id string) error {
	query := `DELETE FROM download_jobs WHERE id = ?;`
	_, err := r.db.ExecContext(ctx, query, id)
	return err
}

// ResetInterruptedJobs сбрасывает зависшие задачи в статус ERROR при перезапуске сервиса.
func (r *Repository) ResetInterruptedJobs(ctx context.Context) (int64, error) {
	query := `
	UPDATE download_jobs
	SET status = 'ERROR',
	    current_step = 'Прервано перезапуском сервера. Нажмите «Повторить»',
	    updated_at = CURRENT_TIMESTAMP
	WHERE status IN ('QUEUED', 'RESOLVING', 'DOWNLOADING', 'CONVERTING', 'TAGGING');
	`
	res, err := r.db.ExecContext(ctx, query)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetActiveJobs возвращает все задачи, находящиеся в процессе выполнения или очереди.
func (r *Repository) GetActiveJobs(ctx context.Context) ([]*Job, error) {
	query := `
	SELECT id, status, source, url, progress, current_step,
	       total_tracks, completed_tracks, error, target_album_path,
	       is_backfill, album_title, artist_name, created_at, updated_at
	FROM download_jobs
	WHERE status IN ('QUEUED', 'RESOLVING', 'DOWNLOADING', 'CONVERTING', 'TAGGING')
	ORDER BY created_at ASC;
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, j)
	}
	return list, nil
}

// GetActiveJobCount возвращает количество активных задач в очереди.
func (r *Repository) GetActiveJobCount(ctx context.Context) (int, error) {
	query := `
	SELECT COUNT(*)
	FROM download_jobs
	WHERE status IN ('QUEUED', 'RESOLVING', 'DOWNLOADING', 'CONVERTING', 'TAGGING');
	`
	var count int
	err := r.db.QueryRowContext(ctx, query).Scan(&count)
	return count, err
}

// ClearFinishedJobs удаляет завершенные задачи (READY, PARTIAL_READY, ERROR, CANCELLED).
// Если age > 0, удаляет только задачи старше указанного интервала времени.
func (r *Repository) ClearFinishedJobs(ctx context.Context, age time.Duration) (int64, error) {
	var query string
	var args []any

	if age > 0 {
		cutoff := time.Now().UTC().Add(-age)
		query = `
		DELETE FROM download_jobs
		WHERE status IN ('READY', 'PARTIAL_READY', 'ERROR', 'CANCELLED')
		  AND updated_at < ?;
		`
		args = append(args, cutoff)
	} else {
		query = `
		DELETE FROM download_jobs
		WHERE status IN ('READY', 'PARTIAL_READY', 'ERROR', 'CANCELLED');
		`
	}

	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClearCompletedJobs является алиасом ClearFinishedJobs.
func (r *Repository) ClearCompletedJobs(ctx context.Context, age time.Duration) (int64, error) {
	return r.ClearFinishedJobs(ctx, age)
}

// GetMissingTracksForAlbum возвращает список недостающих треков для альбома из missing_tracks.
func (r *Repository) GetMissingTracksForAlbum(ctx context.Context, folderPath string) ([]MissingTrackInfo, error) {
	cleanPath := filepath.Clean(folderPath)
	query := `
	SELECT mt.title, mt.artist, mt.track_number, COALESCE(mt.expected_duration, 0), COALESCE(mt.deezer_url, '')
	FROM missing_tracks mt
	JOIN albums al ON mt.album_id = al.id
	WHERE (al.folder_path = ? OR al.folder_path = ?)
	ORDER BY mt.track_number ASC;
	`
	rows, err := r.db.QueryContext(ctx, query, folderPath, cleanPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []MissingTrackInfo
	for rows.Next() {
		var item MissingTrackInfo
		if err := rows.Scan(&item.Title, &item.Artist, &item.TrackNumber, &item.ExpectedDuration, &item.DeezerURL); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

// GetPinnedSource возвращает зафиксированный источник трека по cache_key.
func (r *Repository) GetPinnedSource(ctx context.Context, cacheKey string) (*PinnedTrackSourceDTO, error) {
	cacheKey = strings.TrimSpace(cacheKey)
	if cacheKey == "" {
		return nil, nil
	}

	query := `
	SELECT cache_key, artist_name, track_title, isrc, pinned_url, source_type, duration, created_at, updated_at
	FROM pinned_track_sources
	WHERE cache_key = ?;
	`
	row := r.db.QueryRowContext(ctx, query, cacheKey)
	var p PinnedTrackSourceDTO
	var isrc sql.NullString
	var createdAtRaw, updatedAtRaw any

	err := row.Scan(
		&p.CacheKey, &p.ArtistName, &p.TrackTitle, &isrc,
		&p.PinnedURL, &p.SourceType, &p.Duration,
		&createdAtRaw, &updatedAtRaw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if isrc.Valid && isrc.String != "" {
		p.ISRC = &isrc.String
	}
	_, p.CreatedAt = parseTimeField(createdAtRaw)
	_, p.UpdatedAt = parseTimeField(updatedAtRaw)
	return &p, nil
}

// SavePinnedSource сохраняет или обновляет ручную привязку источника трека.
func (r *Repository) SavePinnedSource(ctx context.Context, pin PinnedTrackSourceDTO) error {
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
	_, err := r.db.ExecContext(ctx, query,
		pin.CacheKey, pin.ArtistName, pin.TrackTitle, isrcVal,
		pin.PinnedURL, pin.SourceType, pin.Duration,
	)
	return err
}

// DeletePinnedSource удаляет ручную привязку аудио-источника по cache_key.
func (r *Repository) DeletePinnedSource(ctx context.Context, cacheKey string) error {
	query := `DELETE FROM pinned_track_sources WHERE cache_key = ?;`
	_, err := r.db.ExecContext(ctx, query, cacheKey)
	return err
}

// UpdateTrackPinnedURL обновляет URL ручной привязки в таблице tracks.
func (r *Repository) UpdateTrackPinnedURL(ctx context.Context, trackID int64, pinnedURL string) error {
	query := `UPDATE tracks SET pinned_source_url = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;`
	_, err := r.db.ExecContext(ctx, query, pinnedURL, trackID)
	return err
}

// GetSoundCloudCache возвращает валидный кэш альтернативных версий SoundCloud.
func (r *Repository) GetSoundCloudCache(ctx context.Context, key string, ttlHours int) (*SoundCloudCacheDTO, error) {
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
	row := r.db.QueryRowContext(ctx, query, key, ttlHours)
	var e SoundCloudCacheDTO
	var checkedAtRaw, updatedAtRaw any

	err := row.Scan(
		&e.CacheKey, &e.TrackTitle, &e.ArtistName, &e.SelectedURL,
		&e.Status, &e.Duration, &e.IsManual, &e.CandidatesJSON,
		&checkedAtRaw, &updatedAtRaw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	_, e.CheckedAt = parseTimeField(checkedAtRaw)
	_, e.UpdatedAt = parseTimeField(updatedAtRaw)

	if e.CandidatesJSON != "" && e.CandidatesJSON != "[]" {
		_ = json.Unmarshal([]byte(e.CandidatesJSON), &e.Candidates)
	}

	if !e.IsManual && (e.Status == "link_dead" || e.SelectedURL == "") {
		return nil, nil
	}
	return &e, nil
}

// PinSoundCloudVersion фиксирует вручную выбранную версию SoundCloud.
func (r *Repository) PinSoundCloudVersion(ctx context.Context, key, artist, title, url string, duration int) error {
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
	_, err := r.db.ExecContext(ctx, query, key, artist, title, url, duration)
	return err
}

// InvalidateSoundCloudURL обрабатывает недоступность URL трека с ротацией на следующего кандидата.
func (r *Repository) InvalidateSoundCloudURL(ctx context.Context, key string, failedURL string) (*SoundCloudCacheDTO, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}

	query := `
	SELECT cache_key, track_title, artist_name, selected_url, status, duration, is_manual, candidates_json, checked_at, updated_at
	FROM soundcloud_uncensored_cache
	WHERE cache_key = ?;
	`
	row := r.db.QueryRowContext(ctx, query, key)
	var entry SoundCloudCacheDTO
	var checkedAtRaw, updatedAtRaw any

	err := row.Scan(
		&entry.CacheKey, &entry.TrackTitle, &entry.ArtistName, &entry.SelectedURL,
		&entry.Status, &entry.Duration, &entry.IsManual, &entry.CandidatesJSON,
		&checkedAtRaw, &updatedAtRaw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	_, entry.CheckedAt = parseTimeField(checkedAtRaw)
	_, entry.UpdatedAt = parseTimeField(updatedAtRaw)

	if entry.CandidatesJSON != "" && entry.CandidatesJSON != "[]" {
		_ = json.Unmarshal([]byte(entry.CandidatesJSON), &entry.Candidates)
	}

	if entry.IsManual {
		entry.Status = "link_dead"
		_, err = r.db.ExecContext(ctx,
			"UPDATE soundcloud_uncensored_cache SET status = 'link_dead', updated_at = CURRENT_TIMESTAMP WHERE cache_key = ?;",
			key,
		)
		return &entry, err
	}

	var remaining []db.SoundCloudCandidateItem
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

		_, err = r.db.ExecContext(ctx, `
			UPDATE soundcloud_uncensored_cache
			SET selected_url = ?, status = ?, duration = ?, candidates_json = ?, updated_at = CURRENT_TIMESTAMP
			WHERE cache_key = ?;
		`, entry.SelectedURL, entry.Status, entry.Duration, entry.CandidatesJSON, key)
	} else {
		entry.SelectedURL = ""
		entry.Status = "link_dead"
		entry.CandidatesJSON = "[]"

		_, err = r.db.ExecContext(ctx, `
			UPDATE soundcloud_uncensored_cache
			SET selected_url = '', status = 'link_dead', candidates_json = '[]', updated_at = CURRENT_TIMESTAMP
			WHERE cache_key = ?;
		`, key)
	}

	return &entry, err
}

// isHighQualityBitrate проверяет, является ли битрейт высоким (>= 310k),
// учитывая варианты хранения в kbps (320) и bps (320000).
func isHighQualityBitrate(bitrate int) bool {
	if bitrate >= 310000 {
		return true
	}
	if bitrate >= 310 && bitrate <= 10000 {
		return true
	}
	return false
}

// CheckTrackInLibrary ищет трек в медиатеке по названию и исполнителю.
func (r *Repository) CheckTrackInLibrary(ctx context.Context, title, artist string) (*ExistingTrackInfo, error) {
	if r.db == nil {
		return nil, nil
	}

	cleanTitle := strings.ToLower(strings.TrimSpace(id3.CleanTitle(title, "")))
	if cleanTitle == "" {
		return nil, nil
	}

	cleanArtist := strings.ToLower(strings.TrimSpace(artist))

	rows, err := r.db.QueryContext(ctx, `
		SELECT t.id, t.title, a.name, COALESCE(al.title, ''), t.file_path, COALESCE(t.bitrate, 0)
		FROM tracks t
		JOIN artists a ON a.id = t.artist_id
		LEFT JOIN albums al ON al.id = t.album_id
		WHERE t.clean_title = ?
		LIMIT 10
	`, cleanTitle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type candidate struct {
		id       int64
		title    string
		artist   string
		album    string
		filePath string
		bitrate  int
	}

	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.title, &c.artist, &c.album, &c.filePath, &c.bitrate); err == nil {
			candidates = append(candidates, c)
		}
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	for _, c := range candidates {
		dbArtistClean := strings.ToLower(strings.TrimSpace(c.artist))
		if cleanArtist == "" || dbArtistClean == cleanArtist ||
			strings.Contains(cleanArtist, dbArtistClean) || strings.Contains(dbArtistClean, cleanArtist) {
			tier := "upscaled_128k"
			if isHighQualityBitrate(c.bitrate) {
				tier = "true_320k"
			}
			return &ExistingTrackInfo{
				TrackID:     c.id,
				Title:       c.title,
				Artist:      c.artist,
				Album:       c.album,
				FilePath:    c.filePath,
				Bitrate:     c.bitrate,
				QualityTier: tier,
			}, nil
		}
	}

	return nil, nil
}
