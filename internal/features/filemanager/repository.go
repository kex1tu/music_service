package filemanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"music-manager/internal/core/textkey"
	"path/filepath"
	"strconv"
	"strings"

	"music-manager/internal/db"
)

var (
	// ErrTrackNotFound возвращается, когда запрашиваемый трек отсутствует в БД.
	ErrTrackNotFound = errors.New("track not found")
)

// DBTX определяет контракт для выполнения SQL-запросов (совместим с *sql.DB и *sql.Tx).
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Repository определяет методы доступа к данным медиатеки в SQLite WAL.
type Repository interface {
	GetTrackByIDOrHash(ctx context.Context, idOrHash string) (*TrackMetadataResponse, error)
	GetTracksMetadataCache(ctx context.Context) (map[string]FileItem, error)
	ListLibraryTracks(ctx context.Context, query string, limit, offset int) ([]LibraryTrackItem, int, error)
	SearchLibrary(ctx context.Context, q string, limit int) (*SearchLibraryResult, error)
	DeleteTrackByPath(ctx context.Context, relPath string) (int64, error)
	DeleteDirectoryRecords(ctx context.Context, dirRelPath string) (int64, error)
	UpdateTrackPath(ctx context.Context, oldRelPath, newRelPath string) error
	UpdateAlbumCoverStatus(ctx context.Context, folderPath string, hasCover bool) error
	GetTrackDetailsForRedownload(ctx context.Context, relPath string) (artist, title, album, deezerID, pinnedURL string, duration int, albumDeezerID string, err error)
	GetAlbumDetailsForRedownload(ctx context.Context, dirRelPath string) (artist, album, deezerID string, err error)
}

// SQLiteRepository реализует Repository поверх базы SQLite.
type SQLiteRepository struct {
	db DBTX
}

// NewRepository создает новый экземпляр репозитория FileManager.
func NewRepository(db DBTX) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

// GetTrackByIDOrHash находит трек по ID, хэшу или относительному пути.
func (r *SQLiteRepository) GetTrackByIDOrHash(ctx context.Context, idOrHash string) (*TrackMetadataResponse, error) {
	idOrHash = strings.TrimSpace(idOrHash)
	if idOrHash == "" {
		return nil, ErrTrackNotFound
	}

	var idNum int64
	if num, err := strconv.ParseInt(idOrHash, 10, 64); err == nil && num > 0 {
		idNum = num
	}

	query := `
	SELECT t.id, COALESCE(t.file_hash, ''), t.title, ar.name AS artist, al.title AS album,
	       COALESCE(al.year, 0),
	       COALESCE((SELECT GROUP_CONCAT(g.name, ', ') FROM track_genres tg JOIN genres g ON tg.genre_id = g.id WHERE tg.track_id = t.id), ''),
	       COALESCE(t.duration, 0.0), COALESCE(t.bitrate, 0), COALESCE(t.track_number, 1), t.file_size, t.file_path,
	       (t.has_embedded_cover OR al.has_cover)
	FROM tracks t
	JOIN albums al ON t.album_id = al.id
	JOIN artists ar ON t.artist_id = ar.id
	WHERE (t.id = ? AND ? > 0) OR t.file_hash = ? OR t.file_path = ?
	LIMIT 1;
	`

	var t TrackMetadataResponse
	var rawID int64
	var rawDur float64
	var hasCover bool

	row := r.db.QueryRowContext(ctx, query, idNum, idNum, idOrHash, idOrHash)
	err := row.Scan(
		&rawID, &t.ID, &t.Title, &t.Artist, &t.Album,
		&t.Year, &t.Genre,
		&rawDur, &t.BitrateKbps, &t.TrackNumber, &t.FileSize, &t.FilePath,
		&hasCover,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTrackNotFound
		}
		return nil, err
	}

	if t.ID == "" {
		t.ID = strconv.FormatInt(rawID, 10)
	}
	t.DurationSec = int(rawDur)
	t.HasCover = hasCover
	t.SampleRate = 44100
	t.Codec = "mp3"
	t.StreamURL = fmt.Sprintf("/api/tracks/%s/stream", t.ID)
	t.CoverURL = fmt.Sprintf("/api/tracks/%s/cover", t.ID)

	return &t, nil
}

// GetTracksMetadataCache извлекает облегченный кэш метаданных всех треков для мгновенного построения дерева файлов.
func (r *SQLiteRepository) GetTracksMetadataCache(ctx context.Context) (map[string]FileItem, error) {
	query := `
	SELECT 
		t.file_path, 
		COALESCE(t.id, 0),
		COALESCE(t.file_hash, ''),
		t.track_number, 
		t.track_total,
		t.disc_number,
		t.disc_total,
		t.title, 
		COALESCE(ar.name, ''), 
		COALESCE(al.title, ''), 
		COALESCE(al.album_artist, ''),
		COALESCE(al.year, 0),
		COALESCE(t.duration, 0.0), 
		COALESCE(t.bitrate, 320),
		(t.has_embedded_cover OR al.has_cover), 
		COALESCE(t.lyrics_status, 'none'),
		COALESCE((SELECT GROUP_CONCAT(g.name, '\\') FROM track_genres tg JOIN genres g ON tg.genre_id = g.id WHERE tg.track_id = t.id), ''),
		COALESCE(t.deezer_id, ''),
		COALESCE(t.pinned_source_url, ''),
		COALESCE(al.deezer_id, '')
	FROM tracks t
	LEFT JOIN albums al ON t.album_id = al.id
	LEFT JOIN artists ar ON t.artist_id = ar.id;
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cache := make(map[string]FileItem)
	for rows.Next() {
		var it FileItem
		var fPath sql.NullString
		var id int64
		var fHash sql.NullString
		var trkNum, trkTot, discNum, discTot, yr sql.NullInt64
		var title, artist, album, albArtist, lyrStatus, genre sql.NullString
		var durVal sql.NullFloat64
		var bit sql.NullInt64
		var cov sql.NullBool
		var dzID, pinnedURL, albDzID sql.NullString

		if err := rows.Scan(
			&fPath, &id, &fHash, &trkNum, &trkTot, &discNum, &discTot,
			&title, &artist, &album, &albArtist, &yr,
			&durVal, &bit, &cov, &lyrStatus, &genre,
			&dzID, &pinnedURL, &albDzID,
		); err != nil {
			continue
		}
		if !fPath.Valid || fPath.String == "" {
			continue
		}
		cleanPath := filepath.ToSlash(fPath.String)
		it.Path = cleanPath
		it.TrackID = id
		it.TrackHash = fHash.String
		if trkNum.Valid && trkNum.Int64 > 0 {
			num := int(trkNum.Int64)
			it.TrackNumber = &num
		}
		if trkTot.Valid && trkTot.Int64 > 0 {
			num := int(trkTot.Int64)
			it.TrackTotal = &num
		}
		if discNum.Valid && discNum.Int64 > 0 {
			num := int(discNum.Int64)
			it.DiscNumber = &num
		}
		if discTot.Valid && discTot.Int64 > 0 {
			num := int(discTot.Int64)
			it.DiscTotal = &num
		}
		if yr.Valid && yr.Int64 > 0 {
			y := int(yr.Int64)
			it.Year = &y
		}
		it.Title = title.String
		it.Artist = artist.String
		it.Album = album.String
		it.AlbumArtist = albArtist.String
		it.Genre = genre.String
		dur := int(durVal.Float64)
		it.Duration = &dur
		it.Bitrate = int(bit.Int64)
		it.HasCover = cov.Bool
		lyr := lyrStatus.String
		if lyr == "synced" {
			lyr = "lrc"
		} else if lyr == "plain" {
			lyr = "txt"
		}
		it.LyricsStatus = lyr
		it.HasLyrics = lyr == "lrc" || lyr == "txt"
		it.DeezerID = dzID.String
		it.PinnedSourceURL = pinnedURL.String
		it.AlbumDeezerID = albDzID.String

		cache[cleanPath] = it
	}

	return cache, nil
}

// ListLibraryTracks возвращает пагинированный список треков медиатеки.
func (r *SQLiteRepository) ListLibraryTracks(ctx context.Context, query string, limit, offset int) ([]LibraryTrackItem, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var countQuery string
	var selectQuery string
	var args []any
	var countArgs []any

	if strings.TrimSpace(query) != "" {
		term := textkey.Search(query)
		countQuery = `
		SELECT COUNT(*)
		FROM tracks t
		JOIN albums al ON t.album_id = al.id
		JOIN artists ar ON t.artist_id = ar.id
		WHERE instr(t.search_title, ?) > 0 OR instr(ar.clean_name, ?) > 0 OR instr(al.search_title, ?) > 0;
		`
		countArgs = []any{term, term, term}

		selectQuery = `
		SELECT t.id, COALESCE(t.file_hash, ''), t.title, ar.name AS artist, al.title AS album,
		       COALESCE(t.duration, 0.0), COALESCE(t.bitrate, 0), t.file_path
		FROM tracks t
		JOIN albums al ON t.album_id = al.id
		JOIN artists ar ON t.artist_id = ar.id
		WHERE instr(t.search_title, ?) > 0 OR instr(ar.clean_name, ?) > 0 OR instr(al.search_title, ?) > 0
		ORDER BY ar.name ASC, al.title ASC, t.track_number ASC
		LIMIT ? OFFSET ?;
		`
		args = []any{term, term, term, limit, offset}
	} else {
		countQuery = "SELECT COUNT(*) FROM tracks;"
		selectQuery = `
		SELECT t.id, COALESCE(t.file_hash, ''), t.title, ar.name AS artist, al.title AS album,
		       COALESCE(t.duration, 0.0), COALESCE(t.bitrate, 0), t.file_path
		FROM tracks t
		JOIN albums al ON t.album_id = al.id
		JOIN artists ar ON t.artist_id = ar.id
		ORDER BY t.id DESC
		LIMIT ? OFFSET ?;
		`
		args = []any{limit, offset}
	}

	var total int
	if err := r.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.db.QueryContext(ctx, selectQuery, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]LibraryTrackItem, 0, limit)
	for rows.Next() {
		var it LibraryTrackItem
		if err := rows.Scan(&it.ID, &it.FileHash, &it.Title, &it.Artist, &it.Album, &it.Duration, &it.Bitrate, &it.FilePath); err != nil {
			continue
		}
		trackIDStr := strconv.FormatInt(it.ID, 10)
		if it.FileHash != "" {
			trackIDStr = it.FileHash
		}
		it.StreamURL = fmt.Sprintf("/api/tracks/%s/stream", trackIDStr)
		it.CoverURL = fmt.Sprintf("/api/tracks/%s/cover", trackIDStr)
		items = append(items, it)
	}

	return items, total, nil
}

// SearchLibrary выполняет глобальный поиск по трекам, альбомам и артистам.
func (r *SQLiteRepository) SearchLibrary(ctx context.Context, query string, limit int) (*SearchLibraryResult, error) {
	if limit <= 0 {
		limit = 50
	}
	term := textkey.Search(query)

	result := &SearchLibraryResult{
		Tracks:  make([]SearchTrackItem, 0),
		Albums:  make([]SearchAlbumItem, 0),
		Artists: make([]SearchArtistItem, 0),
	}

	// 1. Поиск по трекам
	trackQuery := `
	SELECT t.id, t.title, ar.name AS artist, al.title AS album,
	       COALESCE(t.duration, 0.0), COALESCE(t.bitrate, 0), t.file_path, COALESCE(t.lyrics_status, 'none')
	FROM tracks t
	JOIN albums al ON t.album_id = al.id
	JOIN artists ar ON t.artist_id = ar.id
	WHERE instr(t.search_title, ?) > 0 OR instr(t.search_clean_title, ?) > 0
	LIMIT ?;
	`
	tRows, err := r.db.QueryContext(ctx, trackQuery, term, term, limit)
	if err == nil {
		defer tRows.Close()
		for tRows.Next() {
			var it SearchTrackItem
			if err := tRows.Scan(&it.ID, &it.Title, &it.Artist, &it.Album, &it.Duration, &it.Bitrate, &it.FilePath, &it.LyricsStatus); err == nil {
				it.StreamURL = fmt.Sprintf("/api/tracks/%d/stream", it.ID)
				result.Tracks = append(result.Tracks, it)
			}
		}
	}

	// 2. Поиск по альбомам
	albumQuery := `
	SELECT al.id, al.title, ar.name AS artist, COALESCE(al.album_artist, ''), COALESCE(al.year, 0),
	       al.folder_path, al.has_cover, COUNT(t.id) AS track_count
	FROM albums al
	JOIN artists ar ON al.artist_id = ar.id
	LEFT JOIN tracks t ON t.album_id = al.id
	WHERE instr(al.search_title, ?) > 0 OR instr(al.search_clean_title, ?) > 0
	GROUP BY al.id
	LIMIT ?;
	`
	aRows, err := r.db.QueryContext(ctx, albumQuery, term, term, limit)
	if err == nil {
		defer aRows.Close()
		for aRows.Next() {
			var it SearchAlbumItem
			if err := aRows.Scan(&it.ID, &it.Title, &it.Artist, &it.AlbumArtist, &it.Year, &it.FolderPath, &it.HasCover, &it.TrackCount); err == nil {
				result.Albums = append(result.Albums, it)
			}
		}
	}

	// 3. Поиск по артистам
	artistQuery := `
	SELECT ar.id, ar.name, COUNT(DISTINCT t.id) AS track_count,
	       COALESCE((SELECT al.folder_path FROM albums al WHERE al.artist_id = ar.id LIMIT 1), ''),
	       COALESCE((SELECT t.file_path FROM tracks t WHERE t.artist_id = ar.id LIMIT 1), '')
	FROM artists ar
	LEFT JOIN tracks t ON t.artist_id = ar.id
	WHERE instr(ar.clean_name, ?) > 0
	GROUP BY ar.id
	LIMIT ?;
	`
	arRows, err := r.db.QueryContext(ctx, artistQuery, term, limit)
	if err == nil {
		defer arRows.Close()
		for arRows.Next() {
			var it SearchArtistItem
			if err := arRows.Scan(&it.ID, &it.Name, &it.TrackCount, &it.FolderPath, &it.FirstPath); err == nil {
				result.Artists = append(result.Artists, it)
			}
		}
	}

	return result, nil
}

// DeleteTrackByPath удаляет трек по относительному пути и атомарно очищает осиротевшие альбомы и артистов.
func (r *SQLiteRepository) DeleteTrackByPath(ctx context.Context, relPath string) (int64, error) {
	return db.DeleteLibraryTracks(ctx, r.db, relPath, false)
}

// DeleteDirectoryRecords удаляет индекс треков внутри заданной директории.
func (r *SQLiteRepository) DeleteDirectoryRecords(ctx context.Context, dirRelPath string) (int64, error) {
	return db.DeleteLibraryTracks(ctx, r.db, dirRelPath, true)
}

// UpdateTrackPath обновляет путь к файлу трека и связанные задачи аудита.
func (r *SQLiteRepository) UpdateTrackPath(ctx context.Context, oldRelPath, newRelPath string) error {
	_, err := r.db.ExecContext(ctx, "UPDATE tracks SET file_path = ?, updated_at = CURRENT_TIMESTAMP WHERE file_path = ?", newRelPath, oldRelPath)
	if err != nil {
		return err
	}
	_, _ = r.db.ExecContext(ctx, "UPDATE audit_issues SET file_path = ? WHERE file_path = ?", newRelPath, oldRelPath)
	return nil
}

// UpdateAlbumCoverStatus обновляет статус наличия обложки в каталоге.
func (r *SQLiteRepository) UpdateAlbumCoverStatus(ctx context.Context, folderPath string, hasCover bool) error {
	val := 0
	if hasCover {
		val = 1
	}
	_, err := r.db.ExecContext(ctx, "UPDATE albums SET has_cover = ? WHERE folder_path = ?", val, folderPath)
	return err
}

// GetTrackDetailsForRedownload извлекает необходимые метаданные и идентификаторы трека для повторной загрузки.
func (r *SQLiteRepository) GetTrackDetailsForRedownload(ctx context.Context, relPath string) (artist, title, album, deezerID, pinnedURL string, duration int, albumDeezerID string, err error) {
	cleanRel := filepath.ToSlash(filepath.Clean(relPath))
	query := `
	SELECT 
		COALESCE(ar.name, ''), 
		COALESCE(t.title, ''), 
		COALESCE(al.title, ''), 
		COALESCE(t.deezer_id, ''), 
		COALESCE(t.pinned_source_url, ''), 
		COALESCE(t.duration, 0.0), 
		COALESCE(al.deezer_id, '')
	FROM tracks t
	JOIN albums al ON t.album_id = al.id
	JOIN artists ar ON t.artist_id = ar.id
	WHERE t.file_path = ? OR t.file_path = ?
	LIMIT 1;
	`
	var dur float64
	row := r.db.QueryRowContext(ctx, query, cleanRel, relPath)
	err = row.Scan(&artist, &title, &album, &deezerID, &pinnedURL, &dur, &albumDeezerID)
	duration = int(dur)
	return
}

// GetAlbumDetailsForRedownload извлекает метаданные и deezer_id альбома по пути к его директории.
func (r *SQLiteRepository) GetAlbumDetailsForRedownload(ctx context.Context, dirRelPath string) (artist, album, deezerID string, err error) {
	cleanRel := filepath.ToSlash(filepath.Clean(dirRelPath))
	// 1. Попытка прямого сопоставления по folder_path альбома
	query := `
	SELECT COALESCE(ar.name, al.album_artist, ''), COALESCE(al.title, ''), COALESCE(al.deezer_id, '')
	FROM albums al
	JOIN artists ar ON al.artist_id = ar.id
	WHERE al.folder_path = ? OR al.folder_path = ?
	LIMIT 1;
	`
	row := r.db.QueryRowContext(ctx, query, cleanRel, dirRelPath)
	err = row.Scan(&artist, &album, &deezerID)
	if err == nil && (artist != "" || album != "") {
		return artist, album, deezerID, nil
	}

	// 2. Фоллбэк: поиск трека внутри этой папки
	prefix := cleanRel + "/%"
	query2 := `
	SELECT COALESCE(ar.name, ''), COALESCE(al.title, ''), COALESCE(al.deezer_id, '')
	FROM tracks t
	JOIN albums al ON t.album_id = al.id
	JOIN artists ar ON t.artist_id = ar.id
	WHERE t.file_path LIKE ?
	LIMIT 1;
	`
	row2 := r.db.QueryRowContext(ctx, query2, prefix)
	err = row2.Scan(&artist, &album, &deezerID)
	return
}
