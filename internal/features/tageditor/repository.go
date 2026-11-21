package tageditor

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"time"

	librarydb "music-manager/internal/db"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

var (
	// ErrTrackNotFound возвращается, если трек по указанному пути отсутствует в базе данных.
	ErrTrackNotFound = errors.New("track not found in database")
	// ErrAlbumNotFound возвращается, если альбом не найден.
	ErrAlbumNotFound = errors.New("album not found in database")
)

// DBTX определяет универсальный контракт выполнения SQL-запросов (*sql.DB, *sql.Tx).
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// TrackRecord представляет запись трека из SQLite базы данных медиатеки.
type TrackRecord struct {
	ID               int64
	AlbumID          int64
	ArtistID         int64
	Title            string
	CleanTitle       string
	TrackNumber      int
	TrackTotal       int
	DiscNumber       int
	DiscTotal        int
	Duration         float64
	Bitrate          int
	FilePath         string
	FileSize         int64
	Mtime            float64
	FileHash         string
	HasEmbeddedCover bool
	LyricsStatus     string
	ArtistName       string
	AlbumTitle       string
	AlbumArtist      string
	Year             int
	CoverPath        string
	Genres           []string
}

// ToTrackTags конвертирует запись из БД в доменную структуру TrackTags.
func (tr TrackRecord) ToTrackTags() TrackTags {
	genreFormatted := ""
	if len(tr.Genres) > 0 {
		genreFormatted = strings.Join(tr.Genres, "\\\\")
	}
	return TrackTags{
		Path:         tr.FilePath,
		Title:        tr.Title,
		Artist:       tr.ArtistName,
		Album:        tr.AlbumTitle,
		AlbumArtist:  tr.AlbumArtist,
		Year:         tr.Year,
		Genre:        genreFormatted,
		TrackNumber:  tr.TrackNumber,
		TrackTotal:   tr.TrackTotal,
		DiscNumber:   tr.DiscNumber,
		DiscTotal:    tr.DiscTotal,
		Duration:     tr.Duration,
		Bitrate:      tr.Bitrate,
		HasCover:     tr.HasEmbeddedCover,
		LyricsStatus: tr.LyricsStatus,
	}
}

// TagRepository определяет контракт персистентного слоя SQLite WAL для редактора тегов.
type TagRepository interface {
	// 1. Поиск и чтение метаданных треков
	GetTrackByPath(ctx context.Context, relPath string) (*TrackRecord, error)
	GetTracksByAlbumID(ctx context.Context, albumID int64) ([]TrackRecord, error)
	GetTracksByFolderPath(ctx context.Context, folderPath string) ([]TrackRecord, error)

	// 2. Модификация тегов и путей
	UpdateTrackTags(ctx context.Context, relPath string, tags TrackTags) error
	BatchUpdateTrackTags(ctx context.Context, tracks []TrackTags) error
	UpdateTrackFilePath(ctx context.Context, oldPath, newPath string) error

	// 3. Индексация артистов и альбомов
	GetOrCreateArtist(ctx context.Context, name string) (int64, error)
	GetOrCreateAlbum(ctx context.Context, title string, artistID int64, albumArtistName string, year int, folderPath string, hasCover bool) (int64, error)
	UpdateAlbumCover(ctx context.Context, folderPath, coverPath, mime string) error
	UpdateAlbumStats(ctx context.Context, albumID int64) error

	// 4. Связи жанров
	GetTrackGenres(ctx context.Context, trackID int64) ([]string, error)
	SetTrackGenres(ctx context.Context, trackID int64, genres []string) error

	// 5. Системные настройки и MusicBrainz OAuth токены
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, val string) error
	DeleteSetting(ctx context.Context, key string) error
	SaveMusicBrainzTokens(ctx context.Context, accessToken, refreshToken string, expiresAt time.Time, username string) error
	LoadMusicBrainzTokens(ctx context.Context) (accessToken, refreshToken, username string, expiresAt time.Time, err error)
	ClearMusicBrainzTokens(ctx context.Context) error
	SaveTokens(ctx context.Context, accessToken, refreshToken string, expiresAt time.Time, username string) error
	ClearTokens(ctx context.Context) error

	// 6. Транзакционная поддержка
	WithTx(tx DBTX) *Repository
}

// Repository реализует персистентный слой TagRepository поверх SQLite WAL.
type Repository struct {
	db DBTX
}

// NewRepository создает новый экземпляр репозитория TagEditor.
func NewRepository(db DBTX) *Repository {
	return &Repository{db: db}
}

// WithTx создает репозиторий, привязанный к переданной транзакции.
func (r *Repository) WithTx(tx DBTX) *Repository {
	return &Repository{db: tx}
}

// ----------------------------------------------------------------------------
// 1. Поиск и чтение метаданных треков
// ----------------------------------------------------------------------------

func scanTrackRecord(row interface{ Scan(dest ...any) error }) (*TrackRecord, error) {
	var tr TrackRecord
	var artName, albTitle, albArtist, covPath, hash sql.NullString
	var yr sql.NullInt64

	err := row.Scan(
		&tr.ID, &tr.AlbumID, &tr.ArtistID, &tr.Title, &tr.CleanTitle,
		&tr.TrackNumber, &tr.TrackTotal, &tr.DiscNumber, &tr.DiscTotal,
		&tr.Duration, &tr.Bitrate, &tr.FilePath, &tr.FileSize, &tr.Mtime,
		&hash, &tr.HasEmbeddedCover, &tr.LyricsStatus,
		&artName, &albTitle, &albArtist, &yr, &covPath,
	)
	if err != nil {
		return nil, err
	}

	if artName.Valid {
		tr.ArtistName = artName.String
	}
	if albTitle.Valid {
		tr.AlbumTitle = albTitle.String
	}
	if albArtist.Valid {
		tr.AlbumArtist = albArtist.String
	}
	if yr.Valid {
		tr.Year = int(yr.Int64)
	}
	if covPath.Valid {
		tr.CoverPath = covPath.String
	}
	if hash.Valid {
		tr.FileHash = hash.String
	}
	return &tr, nil
}

// GetTrackByPath возвращает запись трека из SQLite по относительному пути к файлу.
func (r *Repository) GetTrackByPath(ctx context.Context, relPath string) (*TrackRecord, error) {
	if r == nil || r.db == nil {
		return nil, ErrTrackNotFound
	}
	cleanRel := filepath.ToSlash(filepath.Clean(relPath))
	query := `
	SELECT t.id, t.album_id, t.artist_id, t.title, t.clean_title,
	       t.track_number, t.track_total, t.disc_number, t.disc_total,
	       t.duration, t.bitrate, t.file_path, t.file_size, t.mtime,
	       t.file_hash, t.has_embedded_cover, t.lyrics_status,
	       ar.name, al.title, al.album_artist, al.year, al.cover_path
	FROM tracks t
	LEFT JOIN artists ar ON t.artist_id = ar.id
	LEFT JOIN albums al ON t.album_id = al.id
	WHERE t.file_path = ? OR t.file_path = ?;
	`
	row := r.db.QueryRowContext(ctx, query, relPath, cleanRel)
	tr, err := scanTrackRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTrackNotFound
	}
	if err != nil {
		return nil, err
	}

	genres, _ := r.GetTrackGenres(ctx, tr.ID)
	tr.Genres = genres
	return tr, nil
}

// GetTracksByAlbumID возвращает все треки указанного альбома.
func (r *Repository) GetTracksByAlbumID(ctx context.Context, albumID int64) ([]TrackRecord, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	query := `
	SELECT t.id, t.album_id, t.artist_id, t.title, t.clean_title,
	       t.track_number, t.track_total, t.disc_number, t.disc_total,
	       t.duration, t.bitrate, t.file_path, t.file_size, t.mtime,
	       t.file_hash, t.has_embedded_cover, t.lyrics_status,
	       ar.name, al.title, al.album_artist, al.year, al.cover_path
	FROM tracks t
	LEFT JOIN artists ar ON t.artist_id = ar.id
	LEFT JOIN albums al ON t.album_id = al.id
	WHERE t.album_id = ?
	ORDER BY t.disc_number ASC, t.track_number ASC, t.file_path ASC;
	`
	rows, err := r.db.QueryContext(ctx, query, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []TrackRecord
	for rows.Next() {
		tr, err := scanTrackRecord(rows)
		if err != nil {
			return nil, err
		}
		genres, _ := r.GetTrackGenres(ctx, tr.ID)
		tr.Genres = genres
		list = append(list, *tr)
	}
	return list, nil
}

// GetTracksByFolderPath возвращает треки альбома по пути к его папке.
func (r *Repository) GetTracksByFolderPath(ctx context.Context, folderPath string) ([]TrackRecord, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	cleanPath := filepath.ToSlash(filepath.Clean(folderPath))
	query := `
	SELECT t.id, t.album_id, t.artist_id, t.title, t.clean_title,
	       t.track_number, t.track_total, t.disc_number, t.disc_total,
	       t.duration, t.bitrate, t.file_path, t.file_size, t.mtime,
	       t.file_hash, t.has_embedded_cover, t.lyrics_status,
	       ar.name, al.title, al.album_artist, al.year, al.cover_path
	FROM tracks t
	JOIN albums al ON t.album_id = al.id
	LEFT JOIN artists ar ON t.artist_id = ar.id
	WHERE al.folder_path = ? OR al.folder_path = ?
	ORDER BY t.disc_number ASC, t.track_number ASC, t.file_path ASC;
	`
	rows, err := r.db.QueryContext(ctx, query, folderPath, cleanPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []TrackRecord
	for rows.Next() {
		tr, err := scanTrackRecord(rows)
		if err != nil {
			return nil, err
		}
		genres, _ := r.GetTrackGenres(ctx, tr.ID)
		tr.Genres = genres
		list = append(list, *tr)
	}
	return list, nil
}

// ----------------------------------------------------------------------------
// 2. Модификация тегов и путей
// ----------------------------------------------------------------------------

// UpdateTrackTags синхронизирует измененные теги с базой данных SQLite.
func (r *Repository) UpdateTrackTags(ctx context.Context, relPath string, tags TrackTags) error {
	if r == nil || r.db == nil {
		return nil
	}
	cleanRel := filepath.ToSlash(filepath.Clean(relPath))

	// 1. Ищем существующий трек
	var trackID int64
	var currentAlbumID int64
	err := r.db.QueryRowContext(ctx, "SELECT id, album_id FROM tracks WHERE file_path = ? OR file_path = ?;", relPath, cleanRel).Scan(&trackID, &currentAlbumID)
	if errors.Is(err, sql.ErrNoRows) {
		// Трек пока не проиндексирован в БД (например, находится во временной папке)
		return nil
	}
	if err != nil {
		return err
	}

	// 2. Получаем или создаем артиста трека
	primaryArtist := resolver.ExtractPrimaryArtist(tags.Artist)
	if primaryArtist == "" {
		primaryArtist = "Unknown Artist"
	}
	artistID, err := r.GetOrCreateArtist(ctx, primaryArtist)
	if err != nil {
		return err
	}

	// 3. Получаем или создаем артиста альбома и альбом
	effAlbumArtist := resolver.ExtractPrimaryArtist(tags.AlbumArtist)
	if effAlbumArtist == "" {
		effAlbumArtist = primaryArtist
	}
	albumArtistID, err := r.GetOrCreateArtist(ctx, effAlbumArtist)
	if err != nil {
		return err
	}

	albumTitle := strings.TrimSpace(tags.Album)
	if albumTitle == "" || strings.EqualFold(albumTitle, "single") || strings.EqualFold(albumTitle, "singles") {
		if strings.TrimSpace(tags.Title) != "" {
			albumTitle = strings.TrimSpace(tags.Title)
		} else {
			albumTitle = "Unknown Album"
		}
	}

	albumDir := filepath.ToSlash(filepath.Dir(cleanRel))
	tags.DiscNumber = librarydb.LocalDiscNumber(albumDir, tags.DiscNumber)
	displayAlbumArtist := strings.TrimSpace(tags.AlbumArtist)
	albumID, err := r.GetOrCreateAlbum(ctx, albumTitle, albumArtistID, displayAlbumArtist, tags.Year, albumDir, tags.HasCover)
	if err != nil {
		return err
	}

	// 4. Обновляем строку в tracks
	cleanTitle := strings.ToLower(tags.Title)
	updateQuery := `
	UPDATE tracks SET
		album_id = ?,
		artist_id = ?,
		title = ?,
		clean_title = ?,
		track_number = ?,
		track_total = ?,
		disc_number = ?,
		disc_total = ?,
		duration = CASE WHEN ? > 0 THEN ? ELSE duration END,
		bitrate = CASE WHEN ? > 0 THEN ? ELSE bitrate END,
		has_embedded_cover = ?,
		lyrics_status = CASE WHEN ? != '' THEN ? ELSE lyrics_status END,
		updated_at = CURRENT_TIMESTAMP
	WHERE id = ?;
	`
	_, err = r.db.ExecContext(ctx, updateQuery,
		albumID, artistID, tags.Title, cleanTitle,
		tags.TrackNumber, tags.TrackTotal, tags.DiscNumber, tags.DiscTotal,
		tags.Duration, tags.Duration, tags.Bitrate, tags.Bitrate,
		tags.HasCover, tags.LyricsStatus, tags.LyricsStatus,
		trackID,
	)
	if err != nil {
		return err
	}

	// 5. Обновляем жанры
	if strings.TrimSpace(tags.Genre) != "" {
		genres := id3.ParseGenres(tags.Genre)
		if len(genres) == 0 {
			genres = []string{strings.TrimSpace(tags.Genre)}
		}
		_ = r.SetTrackGenres(ctx, trackID, genres)
	}

	// 6. Снимаем проблему аудита по тегам
	_, _ = r.db.ExecContext(ctx, `
		UPDATE audit_issues
		SET is_resolved = 1, updated_at = CURRENT_TIMESTAMP
		WHERE entity_type = 'track' AND (entity_id = ? OR file_path = ?) AND issue_type IN ('tag_issue', 'non_en_genre');
	`, trackID, cleanRel)

	// 7. Обновляем агрегаты альбома
	_ = r.UpdateAlbumStats(ctx, albumID)
	if currentAlbumID > 0 && currentAlbumID != albumID {
		_ = r.UpdateAlbumStats(ctx, currentAlbumID)
	}

	return nil
}

// BatchUpdateTrackTags обновляет пачку треков в едином цикле.
func (r *Repository) BatchUpdateTrackTags(ctx context.Context, tracks []TrackTags) error {
	for _, tr := range tracks {
		if err := r.UpdateTrackTags(ctx, tr.Path, tr); err != nil {
			return err
		}
	}
	return nil
}

// UpdateTrackFilePath обновляет путь к файлу после переименования в Puddletag или Органайзере.
func (r *Repository) UpdateTrackFilePath(ctx context.Context, oldPath, newPath string) error {
	if r == nil || r.db == nil {
		return nil
	}
	oldClean := filepath.ToSlash(filepath.Clean(oldPath))
	newClean := filepath.ToSlash(filepath.Clean(newPath))

	query := `
	UPDATE tracks
	SET file_path = ?, is_path_valid = 1, path_violation_reason = '', updated_at = CURRENT_TIMESTAMP
	WHERE file_path = ? OR file_path = ?;
	`
	_, err := r.db.ExecContext(ctx, query, newClean, oldPath, oldClean)
	if err != nil {
		return err
	}

	_, _ = r.db.ExecContext(ctx, `
		UPDATE audit_issues
		SET file_path = ?, updated_at = CURRENT_TIMESTAMP
		WHERE file_path = ? OR file_path = ?;
	`, newClean, oldPath, oldClean)

	return nil
}

// ----------------------------------------------------------------------------
// 3. Индексация артистов и альбомов
// ----------------------------------------------------------------------------

// GetOrCreateArtist находит или вставляет артиста, поддерживая tracked_artists.
func (r *Repository) GetOrCreateArtist(ctx context.Context, name string) (int64, error) {
	return librarydb.GetOrCreateLibraryArtist(ctx, r.db, name)
}

// GetOrCreateAlbum находит или вставляет альбом.
func (r *Repository) GetOrCreateAlbum(ctx context.Context, title string, artistID int64, albumArtistName string, year int, folderPath string, hasCover bool) (int64, error) {
	return librarydb.UpsertLocalAlbum(ctx, r.db, title, artistID, albumArtistName, year, folderPath, hasCover)
}

// UpdateAlbumCover сохраняет путь к файлу обложки альбома.
func (r *Repository) UpdateAlbumCover(ctx context.Context, folderPath, coverPath, mime string) error {
	if r == nil || r.db == nil {
		return nil
	}
	cleanFolder := filepath.ToSlash(filepath.Clean(folderPath))
	query := `
	UPDATE albums
	SET cover_path = ?, cover_mime = ?, has_cover = 1, updated_at = CURRENT_TIMESTAMP
	WHERE folder_path = ? OR folder_path = ?;
	`
	_, err := r.db.ExecContext(ctx, query, coverPath, mime, folderPath, cleanFolder)
	if err != nil {
		return err
	}

	trackQuery := `
	UPDATE tracks
	SET has_embedded_cover = 1, updated_at = CURRENT_TIMESTAMP
	WHERE album_id IN (SELECT id FROM albums WHERE folder_path = ? OR folder_path = ?);
	`
	_, _ = r.db.ExecContext(ctx, trackQuery, folderPath, cleanFolder)
	return nil
}

// UpdateAlbumStats актуализирует число треков и признак полноты альбома.
func (r *Repository) UpdateAlbumStats(ctx context.Context, albumID int64) error {
	if r == nil || r.db == nil {
		return nil
	}
	queryCount := `
	UPDATE albums SET
		present_tracks_count = (SELECT COUNT(*) FROM tracks WHERE album_id = ?),
		total_tracks = CASE
			WHEN total_tracks <= 0 THEN COALESCE((SELECT MAX(track_total) FROM tracks WHERE album_id = ?), 0)
			ELSE total_tracks
		END,
		updated_at = CURRENT_TIMESTAMP
	WHERE id = ?;
	`
	if _, err := r.db.ExecContext(ctx, queryCount, albumID, albumID, albumID); err != nil {
		return err
	}

	queryComplete := `
	UPDATE albums SET
		is_complete = CASE
			WHEN (SELECT COUNT(*) FROM missing_tracks WHERE album_id = ?) > 0 THEN 0
			WHEN total_tracks > 1 AND present_tracks_count < total_tracks THEN 0
			WHEN (total_tracks <= 1 OR total_tracks IS NULL) AND present_tracks_count >= 1 THEN 1
			ELSE 1
		END,
		updated_at = CURRENT_TIMESTAMP
	WHERE id = ?;
	`
	_, err := r.db.ExecContext(ctx, queryComplete, albumID, albumID)
	return err
}

// ----------------------------------------------------------------------------
// 4. Связи жанров
// ----------------------------------------------------------------------------

// GetTrackGenres возвращает список канонических жанров для трека.
func (r *Repository) GetTrackGenres(ctx context.Context, trackID int64) ([]string, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	query := `
	SELECT g.name
	FROM genres g
	JOIN track_genres tg ON g.id = tg.genre_id
	WHERE tg.track_id = ?
	ORDER BY g.name ASC;
	`
	rows, err := r.db.QueryContext(ctx, query, trackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var genres []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err == nil && g != "" {
			genres = append(genres, g)
		}
	}
	return genres, nil
}

// SetTrackGenres заменяет список жанров трека.
func (r *Repository) SetTrackGenres(ctx context.Context, trackID int64, genres []string) error {
	if r == nil || r.db == nil {
		return nil
	}
	_, err := r.db.ExecContext(ctx, "DELETE FROM track_genres WHERE track_id = ?;", trackID)
	if err != nil {
		return err
	}

	for _, g := range genres {
		trimmed := strings.TrimSpace(g)
		if trimmed == "" {
			continue
		}

		genreRes := id3.AnalyzeAndNormalizeGenre(trimmed)
		rawName := trimmed
		canonName := genreRes.CanonicalGenre
		if canonName == "" {
			canonName = trimmed
		}
		isValidEn := !genreRes.HasCyrillic && !genreRes.IsNonCanonical

		var genreID int64
		err := r.db.QueryRowContext(ctx, "SELECT id FROM genres WHERE name = ?;", rawName).Scan(&genreID)
		if errors.Is(err, sql.ErrNoRows) {
			res, insErr := r.db.ExecContext(ctx, `
				INSERT INTO genres (name, is_valid_en, canonical_en_name)
				VALUES (?, ?, ?);
			`, rawName, isValidEn, canonName)
			if insErr == nil {
				genreID, _ = res.LastInsertId()
			}
		} else if err == nil {
			_, _ = r.db.ExecContext(ctx, `
				UPDATE genres SET is_valid_en = ?, canonical_en_name = ? WHERE id = ?;
			`, isValidEn, canonName, genreID)
		}

		if genreID > 0 {
			_, _ = r.db.ExecContext(ctx, "INSERT OR IGNORE INTO track_genres (track_id, genre_id) VALUES (?, ?);", trackID, genreID)
		}
	}
	return nil
}

// ----------------------------------------------------------------------------
// 5. Системные настройки и MusicBrainz OAuth токены
// ----------------------------------------------------------------------------

// GetSetting возвращает значение настройки из таблицы app_settings.
func (r *Repository) GetSetting(ctx context.Context, key string) (string, error) {
	if r == nil || r.db == nil {
		return "", nil
	}
	var val string
	err := r.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key = ?;", key).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return val, err
}

// SetSetting сохраняет или обновляет значение настройки.
func (r *Repository) SetSetting(ctx context.Context, key, val string) error {
	if r == nil || r.db == nil {
		return nil
	}
	query := `
	INSERT INTO app_settings (key, value, updated_at)
	VALUES (?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(key) DO UPDATE SET
		value = excluded.value,
		updated_at = CURRENT_TIMESTAMP;
	`
	_, err := r.db.ExecContext(ctx, query, key, val)
	return err
}

// DeleteSetting удаляет настройку по ключу.
func (r *Repository) DeleteSetting(ctx context.Context, key string) error {
	if r == nil || r.db == nil {
		return nil
	}
	_, err := r.db.ExecContext(ctx, "DELETE FROM app_settings WHERE key = ?;", key)
	return err
}

const musicBrainzTokenKeys = `'mb_access_token','mb_refresh_token','mb_expires_at','mb_username',
	'musicbrainz_access_token','musicbrainz_refresh_token','musicbrainz_expires_at','musicbrainz_username'`

// SaveMusicBrainzTokens updates both compatible key sets in one transaction.
func (r *Repository) SaveMusicBrainzTokens(ctx context.Context, accessToken, refreshToken string, expiresAt time.Time, username string) error {
	return r.writeMusicBrainzTokens(ctx, `
	INSERT INTO app_settings(key, value, updated_at) VALUES
	    ('mb_access_token', ?1, CURRENT_TIMESTAMP), ('mb_refresh_token', ?2, CURRENT_TIMESTAMP),
	    ('mb_expires_at', ?3, CURRENT_TIMESTAMP), ('mb_username', ?4, CURRENT_TIMESTAMP),
	    ('musicbrainz_access_token', ?1, CURRENT_TIMESTAMP), ('musicbrainz_refresh_token', ?2, CURRENT_TIMESTAMP),
	    ('musicbrainz_expires_at', ?3, CURRENT_TIMESTAMP), ('musicbrainz_username', ?4, CURRENT_TIMESTAMP)
	ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		accessToken, refreshToken, expiresAt.UTC().Format(time.RFC3339), username)
}

func (r *Repository) writeMusicBrainzTokens(ctx context.Context, query string, args ...any) error {
	if conn, ok := r.db.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
		return tx.Commit()
	}

	// WithTx already owns the connection. A savepoint rolls back this write alone.
	tx, ok := r.db.(*sql.Tx)
	if !ok {
		return errors.New("OAuth storage requires a SQLite transaction")
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT musicbrainz_oauth"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_, rollbackErr := tx.ExecContext(cleanupCtx, "ROLLBACK TO musicbrainz_oauth")
		_, releaseErr := tx.ExecContext(cleanupCtx, "RELEASE musicbrainz_oauth")
		return errors.Join(err, rollbackErr, releaseErr)
	}
	_, err := tx.ExecContext(ctx, "RELEASE musicbrainz_oauth")
	return err
}

// LoadMusicBrainzTokens reads one snapshot and chooses the active key set.
func (r *Repository) LoadMusicBrainzTokens(ctx context.Context) (accessToken, refreshToken, username string, expiresAt time.Time, err error) {
	rows, err := r.db.QueryContext(ctx, "SELECT key,value FROM app_settings WHERE key IN ("+musicBrainzTokenKeys+")")
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	defer rows.Close()
	settings := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return "", "", "", time.Time{}, err
		}
		settings[key] = value
	}
	if err := rows.Err(); err != nil {
		return "", "", "", time.Time{}, err
	}
	prefix := "mb_"
	if settings[prefix+"access_token"] == "" {
		prefix = "musicbrainz_"
	}
	if settings[prefix+"access_token"] == "" {
		return "", "", "", time.Time{}, nil
	}
	expiresAt, _ = time.Parse(time.RFC3339Nano, settings[prefix+"expires_at"])
	return settings[prefix+"access_token"], settings[prefix+"refresh_token"], settings[prefix+"username"], expiresAt.UTC(), nil
}

// ClearMusicBrainzTokens deletes both key sets atomically.
func (r *Repository) ClearMusicBrainzTokens(ctx context.Context) error {
	return r.writeMusicBrainzTokens(ctx, "DELETE FROM app_settings WHERE key IN ("+musicBrainzTokenKeys+")")
}

// Compile-time checks for interface conformance
var (
	_ TagRepository                        = (*Repository)(nil)
	_ TokenPersistence                     = (*Repository)(nil)
	_ resolver.MusicBrainzTokenPersistence = (*Repository)(nil)
)

// SaveTokens реализует интерфейс TokenPersistence и resolver.MusicBrainzTokenPersistence.
func (r *Repository) SaveTokens(ctx context.Context, accessToken, refreshToken string, expiresAt time.Time, username string) error {
	return r.SaveMusicBrainzTokens(ctx, accessToken, refreshToken, expiresAt, username)
}

// ClearTokens реализует интерфейс TokenPersistence и resolver.MusicBrainzTokenPersistence.
func (r *Repository) ClearTokens(ctx context.Context) error {
	return r.ClearMusicBrainzTokens(ctx)
}
