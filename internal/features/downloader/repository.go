package downloader

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"music-manager/internal/core/textkey"
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
	GetAlbumTrackPositions(ctx context.Context, folderPath string) (map[db.TrackPosition]bool, error)
	CheckTrackInLibrary(ctx context.Context, title, artist string, isrc ...*string) (*ExistingTrackInfo, error)
}

// Repository реализует персистентный слой задач загрузки в SQLite WAL.
type Repository struct {
	db *sql.DB
}

// NewRepository создает новый экземпляр репозитория Downloader.
func NewRepository(db *sql.DB) *Repository {
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
	var selection sql.NullString
	var createdAtRaw, updatedAtRaw any
	var backfill sql.NullString

	err := scanner.Scan(
		&j.ID, &statusStr, &j.Source, &j.URL, &j.Progress, &j.CurrentStep,
		&j.TotalTracks, &j.CompletedTracks, &j.Error, &j.TargetAlbumPath,
		&j.IsBackfill, &j.AlbumTitle, &j.ArtistName, &createdAtRaw, &updatedAtRaw, &selection, &backfill, &j.ArtistID,
	)
	if err != nil {
		return nil, err
	}

	if backfill.Valid {
		if err := json.Unmarshal([]byte(backfill.String), &j.Backfill); err != nil {
			j.selectionCorrupt = true
		}
	}
	j.Status = JobStatus(statusStr)
	createdAtStr, _ := parseTimeField(createdAtRaw)
	_, updatedAt := parseTimeField(updatedAtRaw)
	j.CreatedAt = createdAtStr
	j.UpdatedAt = updatedAt
	j.legacySelection = !selection.Valid
	if selection.Valid && !j.selectionCorrupt {
		loadSourceSelection(&j, selection.String)
	}
	if j.selectionCorrupt {
		j.Status = StatusError
		message := ErrCorruptJobSelection.Error()
		j.CurrentStep = message
		j.Error = &message
	}
	return &j, nil
}

const insertJobSQL = `
	INSERT INTO download_jobs (
		id, status, source, url, progress, current_step,
		total_tracks, completed_tracks, error, target_album_path,
		is_backfill, album_title, artist_name, backfill_selection, artist_id, selection_json, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`

const updateJobSQL = `
	UPDATE download_jobs SET
		status=?, source=?, url=?, progress=?, current_step=?, total_tracks=?,
		completed_tracks=?, error=?, target_album_path=?, is_backfill=?, album_title=?,
		artist_name=?, backfill_selection=?, artist_id=?, selection_json=?, updated_at=CURRENT_TIMESTAMP
	WHERE id=?`

const selectJobSQL = `
	SELECT j.id, j.status, j.source, j.url, j.progress, j.current_step,
	       j.total_tracks, j.completed_tracks, j.error, j.target_album_path,
	       j.is_backfill, j.album_title, j.artist_name, j.created_at, j.updated_at,
	       j.selection_json, j.backfill_selection, j.artist_id
	FROM download_jobs j`

func (r *Repository) CreateJob(ctx context.Context, job *Job) error {
	return r.writeJob(ctx, job, insertJobSQL, true)
}

func (r *Repository) createJobs(ctx context.Context, jobs []*Job) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, job := range jobs {
		if err := writeJobSnapshot(ctx, tx, job.Snapshot(), insertJobSQL, true); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repository) SaveJob(ctx context.Context, job *Job) error {
	return r.writeJob(ctx, job, insertJobSQL+`
	ON CONFLICT(id) DO UPDATE SET
		status=excluded.status, progress=excluded.progress, current_step=excluded.current_step,
		total_tracks=excluded.total_tracks, completed_tracks=excluded.completed_tracks,
		error=excluded.error, target_album_path=excluded.target_album_path,
		is_backfill=excluded.is_backfill, album_title=excluded.album_title,
		artist_id=excluded.artist_id, artist_name=excluded.artist_name,
		backfill_selection=excluded.backfill_selection, selection_json=excluded.selection_json, updated_at=CURRENT_TIMESTAMP`, true)
}

// updateProgressSnapshot only updates an existing job that is no longer queued.
func (r *Repository) updateProgressSnapshot(ctx context.Context, job *Job) error {
	return r.writeJob(ctx, job, updateJobSQL+" AND status <> 'QUEUED'", false)
}

func (r *Repository) updateJobWithSelection(ctx context.Context, job *Job, expected JobStatus) error {
	return r.writeJob(ctx, job, updateJobSQL+" AND status=?", false, expected)
}

func (r *Repository) writeJob(ctx context.Context, job *Job, query string, insert bool, conditions ...any) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := writeJobSnapshot(ctx, tx, job.Snapshot(), query, insert, conditions...); err != nil {
		return err
	}
	return tx.Commit()
}

func writeJobSnapshot(ctx context.Context, tx *sql.Tx, snap *Job, query string, insert bool, conditions ...any) error {
	if snap == nil {
		return errors.New("job is nil")
	}
	if snap.selectionCorrupt {
		return ErrCorruptJobSelection
	}
	var backfill any
	if snap.Backfill != nil {
		payload, err := json.Marshal(snap.Backfill)
		if err != nil {
			return err
		}
		backfill = string(payload)
	}
	selection, err := marshalSourceSelection(snap)
	if err != nil {
		return fmt.Errorf("encode job selection: %w", err)
	}
	args := []any{snap.Status, snap.Source, snap.URL, snap.Progress, snap.CurrentStep,
		snap.TotalTracks, snap.CompletedTracks, snap.Error, snap.TargetAlbumPath,
		snap.IsBackfill, snap.AlbumTitle, snap.ArtistName, backfill, snap.ArtistID, string(selection)}
	if insert {
		args = append([]any{snap.ID}, args...)
	} else {
		args = append(args, snap.ID)
	}
	args = append(args, conditions...)
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrJobNotFound
	}
	return nil
}

func (r *Repository) ClaimNextJob(ctx context.Context) (*Job, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `
		UPDATE download_jobs SET status='RESOLVING', current_step='Определение источника и метаданных',
		                          updated_at=CURRENT_TIMESTAMP
		WHERE status='QUEUED' AND id=(
		    SELECT id FROM download_jobs WHERE status='QUEUED' ORDER BY created_at,rowid LIMIT 1
		)
		RETURNING id`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job, err := scanJob(tx.QueryRowContext(ctx, selectJobSQL+" WHERE j.id=?", id))
	if err != nil {
		return nil, err
	}
	if job.selectionCorrupt {
		if _, err := tx.ExecContext(ctx, "UPDATE download_jobs SET status=?,current_step=?,error=? WHERE id=?",
			job.Status, job.CurrentStep, job.Error, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return job, nil
}

func (r *Repository) GetJob(ctx context.Context, id string) (*Job, error) {
	job, err := scanJob(r.db.QueryRowContext(ctx, selectJobSQL+" WHERE j.id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return job, err
}

func (r *Repository) ListJobs(ctx context.Context, limit, offset int) ([]*Job, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return r.listJobs(ctx, selectJobSQL+" ORDER BY j.created_at DESC,j.id LIMIT ? OFFSET ?", limit, offset)
}

func (r *Repository) GetAllJobs(ctx context.Context) ([]*Job, error) {
	return r.listJobs(ctx, selectJobSQL+" ORDER BY j.created_at DESC,j.id")
}

func (r *Repository) listJobs(ctx context.Context, query string, args ...any) ([]*Job, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
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
	return list, rows.Err()
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
	WHERE status IN ('RESOLVING', 'DOWNLOADING', 'CONVERTING', 'TAGGING');
	`
	res, err := r.db.ExecContext(ctx, query)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetActiveJobs возвращает все задачи, находящиеся в процессе выполнения или очереди.
func (r *Repository) GetActiveJobs(ctx context.Context) ([]*Job, error) {
	return r.listJobs(ctx, selectJobSQL+`
	WHERE j.status IN ('QUEUED', 'RESOLVING', 'DOWNLOADING', 'CONVERTING', 'TAGGING')
	ORDER BY j.created_at,j.id`)
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

func (r *Repository) albumCatalogChecked(ctx context.Context, folderPath string) (bool, error) {
	var checked bool
	err := r.db.QueryRowContext(ctx, "SELECT catalog_checked_at IS NOT NULL FROM albums WHERE folder_path = ?",
		db.AlbumRoot(filepath.ToSlash(folderPath))).Scan(&checked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return checked, err
}

// GetMissingTracksForAlbum возвращает позиции, помеченные как отсутствующие при сверке.
func (r *Repository) GetMissingTracksForAlbum(ctx context.Context, folderPath string) ([]MissingTrackInfo, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, "SELECT id FROM albums WHERE folder_path = ?", db.AlbumRoot(filepath.ToSlash(folderPath))).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tracks, err := db.ReleaseTracklist(ctx, r.db, id, true)
	if err != nil {
		return nil, err
	}
	result := make([]MissingTrackInfo, len(tracks))
	for i, track := range tracks {
		result[i] = MissingTrackInfo{CatalogTrack: track}
	}
	return result, nil
}

func (r *Repository) GetAlbumTrackPositions(ctx context.Context, folderPath string) (map[db.TrackPosition]bool, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT COALESCE(t.disc_number, 1), COALESCE(t.track_number, 0)
		FROM tracks t JOIN albums a ON a.id = t.album_id WHERE a.folder_path = ?`,
		db.AlbumRoot(filepath.ToSlash(folderPath)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	positions := make(map[db.TrackPosition]bool)
	for rows.Next() {
		var disc, track int
		if err := rows.Scan(&disc, &track); err != nil {
			return nil, err
		}
		positions[db.ReleasePosition(disc, track)] = true
	}
	return positions, rows.Err()
}

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
func (r *Repository) CheckTrackInLibrary(ctx context.Context, title, artist string, isrc ...*string) (*ExistingTrackInfo, error) {
	if r.db == nil {
		return nil, nil
	}

	if strings.TrimSpace(title) == "" {
		return nil, nil
	}
	titleKey := textkey.Search(id3.CleanTitle(title, ""))
	artistKey := textkey.Search(artist)
	var recording string
	if len(isrc) != 0 && isrc[0] != nil {
		recording = textkey.Search(*isrc[0])
	}

	rows, err := r.db.QueryContext(ctx, `
		WITH known_artist AS (
			SELECT MIN(id) AS id FROM artists WHERE clean_name = ?2 AND ?2 <> '' HAVING COUNT(*) = 1
		), candidates(id, exact_recording) AS (
			SELECT id, 1 FROM tracks WHERE ?3 <> '' AND music_search_key(COALESCE(isrc, '')) = ?3
			UNION ALL
			SELECT id, 0 FROM tracks WHERE search_title = ?1 AND artist_id = (SELECT id FROM known_artist)
				AND (?3 = '' OR NULLIF(trim(isrc), '') IS NULL)
		)
		SELECT t.id, t.title, a.name, al.title, t.file_path, COALESCE(t.bitrate, 0), c.exact_recording
		FROM candidates c JOIN tracks t ON t.id = c.id
		JOIN artists a ON a.id = t.artist_id JOIN albums al ON al.id = t.album_id
		ORDER BY exact_recording DESC,
			CASE WHEN t.bitrate >= 10000 THEN t.bitrate / 1000.0 ELSE t.bitrate END DESC, t.id
		LIMIT 2
	`, titleKey, artistKey, recording)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var match *ExistingTrackInfo
	for rows.Next() {
		var found ExistingTrackInfo
		var exactRecording bool
		if err := rows.Scan(&found.TrackID, &found.Title, &found.Artist, &found.Album, &found.FilePath, &found.Bitrate, &exactRecording); err != nil {
			return nil, err
		}
		if match != nil {
			return nil, nil
		}
		found.QualityTier = "upscaled_128k"
		if isHighQualityBitrate(found.Bitrate) {
			found.QualityTier = "true_320k"
		}
		if exactRecording {
			return &found, nil
		}
		match = &found
	}
	return match, rows.Err()
}

func (r *Repository) albumArtistForFolder(ctx context.Context, folder string) (*int64, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, "SELECT artist_id FROM albums WHERE folder_path = ?", folder).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}
