package audit

import (
	"context"
	"database/sql"
	"fmt"
	"music-manager/internal/core/textkey"
	"music-manager/internal/db"
	"path/filepath"
	"strings"
)

// DBTX определяет универсальный контракт для выполнения SQL-запросов (*sql.DB, *sql.Tx).
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// TrackCandidateDTO — сырая запись для детекции дубликатов.
type TrackCandidateDTO struct {
	FilePath    string
	Title       string
	Artist      string
	Album       string
	Duration    int
	SizeBytes   int64
	TrackNumber int
	Bitrate     int
}

// IncompleteAlbumDTO — запись неполного альбома из SQLite.
type IncompleteAlbumDTO struct {
	ID            int64
	FolderPath    string
	Title         string
	Artist        string
	Year          *int
	HasCover      bool
	CoverPath     *string
	PresentCount  int
	ExpectedCount int
	TotalTracks   int
	TotalDiscs    int
	DeezerAlbumID *string
}

// TagAuditTrackDTO — запись трека для инспекции тегов.
type TagAuditTrackDTO struct {
	ID          int64
	FilePath    string
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	Year        int
	TrackNumber int
	Duration    float64
}

// AuditRepository определяет контракт взаимодействия с базой данных для модуля Audit.
type AuditRepository interface {
	// 1. Audit Summary & Search
	GetAuditSummary(ctx context.Context) (*AuditSummary, error)
	SearchLibrary(ctx context.Context, query string, limit int) (*SearchLibraryResult, error)

	// 2. Duplicates
	GetTracksForDuplicatesScan(ctx context.Context) ([]TrackCandidateDTO, error)
	DeleteTrackAndCleanup(ctx context.Context, path string) error

	// 3. Incomplete Albums & Missing Tracks
	GetIncompleteAlbums(ctx context.Context, limit int) ([]IncompleteAlbumDTO, error)
	GetPresentTrackNumbers(ctx context.Context, albumID int64) ([]int, error)
	GetAlbumMetadataByPath(ctx context.Context, folderPath string) (*IncompleteAlbumDTO, error)
	VerifyCatalog(ctx context.Context, albumID int64, deezerID string, catalog []db.CatalogTrack) (map[db.TrackPosition]bool, error)

	// 4. Invalid Paths & Reorganization
	GetInvalidPaths(ctx context.Context) ([]InvalidPathRecord, error)
	UpdateTrackPath(ctx context.Context, oldPath, newPath string) error

	// 5. Missing Lyrics & Covers
	GetTracksMissingLyrics(ctx context.Context, limit int) ([]MissingLyricsItem, error)
	GetTracksForLyricsFetch(ctx context.Context) ([]TrackCandidateDTO, error)
	SetTrackLyricsStatusAndResolve(ctx context.Context, path, kind string) error
	GetAlbumsMissingCovers(ctx context.Context, limit int) ([]MissingCoverItem, error)
	UpdateAlbumCover(ctx context.Context, folderPath, coverPath, mime string) error

	// 6. Non-English Genres
	GetNonEnGenreTracks(ctx context.Context) ([]NonEnGenreItem, error)
	RetagTrackGenre(ctx context.Context, trackID int64, canonicalGenre string) error

	// 7. Tag Issues
	GetTracksForTagAudit(ctx context.Context) ([]TagAuditTrackDTO, error)
	GetTrackForTagAuditByPath(ctx context.Context, path string) (*TagAuditTrackDTO, error)
	UpdateTrackMetadata(ctx context.Context, path, title, artist, album string, year int) error

	// 8. Audit Issues Log
	LogAuditIssue(ctx context.Context, issue AuditIssue) (int64, error)
	ResolveAuditIssueByPath(ctx context.Context, path, issueType string) error
	DeleteAuditIssueByPath(ctx context.Context, path, issueType string) error
	ClearAuditIssues(ctx context.Context) error

	// 9. Aliases & Convenience methods
	DeleteTrackByPath(ctx context.Context, path string) error
	ListAllTracksForDuplicates(ctx context.Context) ([]TrackCandidateDTO, error)
	ListTracksForTagAudit(ctx context.Context) ([]TagAuditTrackDTO, error)
	ListTracksWithoutLyrics(ctx context.Context) ([]TrackCandidateDTO, error)
	GetMissingTracksForAlbum(ctx context.Context, albumPath string) ([]db.CatalogTrack, error)
}

// Repository реализует персистентный слой SQLite WAL для аудита медиатеки.
type Repository struct {
	db DBTX
}

// NewRepository создает новый экземпляр репозитория аудита.
func NewRepository(db DBTX) *Repository {
	return &Repository{db: db}
}

// WithTx возвращает экземпляр Repository, выполняющий запросы в рамках транзакции.
func (r *Repository) WithTx(tx DBTX) *Repository {
	return &Repository{db: tx}
}

// ----------------------------------------------------------------------------
// 1. Audit Summary & Search
// ----------------------------------------------------------------------------

func (r *Repository) GetAuditSummary(ctx context.Context) (*AuditSummary, error) {
	var summary AuditSummary

	_ = r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tracks;").Scan(&summary.TotalTracks)
	_ = r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM albums;").Scan(&summary.TotalAlbums)
	_ = r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tracks WHERE lyrics_status = 'none';").Scan(&summary.MissingLyricsCount)
	_ = r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tracks WHERE has_embedded_cover = 0;").Scan(&summary.MissingCoversCount)
	_ = r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM albums WHERE is_complete = 0;").Scan(&summary.IncompleteAlbumsCount)
	_ = r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tracks WHERE is_path_valid = 0;").Scan(&summary.InvalidPathsCount)
	_ = r.db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT g.id) FROM genres g JOIN track_genres tg ON g.id = tg.genre_id WHERE g.is_valid_en = 0;").Scan(&summary.NonEnGenresCount)

	return &summary, nil
}

func (r *Repository) SearchLibrary(ctx context.Context, query string, limit int) (*SearchLibraryResult, error) {
	if limit <= 0 {
		limit = 50
	}
	term := textkey.Search(query)

	result := &SearchLibraryResult{
		Tracks:  make([]SearchTrackItem, 0),
		Albums:  make([]SearchAlbumItem, 0),
		Artists: make([]SearchArtistItem, 0),
	}

	// 1. Tracks search
	trackQuery := `
	SELECT t.id, t.title, ar.name AS artist, al.title AS album, al.year, t.duration, t.file_path, t.lyrics_status, al.has_cover, al.cover_path
	FROM tracks t
	JOIN albums al ON t.album_id = al.id
	JOIN artists ar ON t.artist_id = ar.id
	WHERE instr(music_search_key(t.title), ?) > 0 OR instr(music_search_key(t.clean_title), ?) > 0
	LIMIT ?;
	`
	tRows, err := r.db.QueryContext(ctx, trackQuery, term, term, limit)
	if err == nil {
		defer tRows.Close()
		for tRows.Next() {
			var it SearchTrackItem
			var artName, albTitle, fPath, lyrStatus, covPath sql.NullString
			var yr sql.NullInt64
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
				result.Tracks = append(result.Tracks, it)
			}
		}
	}

	// 2. Albums search
	albumQuery := `
	SELECT al.id, al.title, ar.name AS artist, al.year, al.has_cover, al.cover_path, al.folder_path
	FROM albums al
	JOIN artists ar ON al.artist_id = ar.id
	WHERE instr(music_search_key(al.title), ?) > 0 OR instr(music_search_key(al.clean_title), ?) > 0
	LIMIT ?;
	`
	aRows, err := r.db.QueryContext(ctx, albumQuery, term, term, limit)
	if err == nil {
		defer aRows.Close()
		for aRows.Next() {
			var it SearchAlbumItem
			var artName, covPath, fPath sql.NullString
			var yr sql.NullInt64
			if err := aRows.Scan(&it.ID, &it.Title, &artName, &yr, &it.HasCover, &covPath, &fPath); err == nil {
				if artName.Valid {
					it.Artist = artName.String
				}
				if yr.Valid {
					it.Year = int(yr.Int64)
				}
				if covPath.Valid {
					it.CoverPath = covPath.String
				}
				if fPath.Valid {
					it.Path = filepath.ToSlash(fPath.String)
				}
				result.Albums = append(result.Albums, it)
			}
		}
	}

	// 3. Artists search
	artistQuery := `
	SELECT id, name
	FROM artists
	WHERE instr(music_search_key(name), ?) > 0
	LIMIT ?;
	`
	arRows, err := r.db.QueryContext(ctx, artistQuery, term, limit)
	if err == nil {
		defer arRows.Close()
		for arRows.Next() {
			var it SearchArtistItem
			if err := arRows.Scan(&it.ID, &it.Name); err == nil {
				result.Artists = append(result.Artists, it)
			}
		}
	}

	return result, nil
}

// ----------------------------------------------------------------------------
// 2. Duplicates
// ----------------------------------------------------------------------------

func (r *Repository) GetTracksForDuplicatesScan(ctx context.Context) ([]TrackCandidateDTO, error) {
	query := `
	SELECT t.file_path, t.title, COALESCE(ar.name, ''), COALESCE(al.title, ''),
	       COALESCE(t.duration, 0), COALESCE(t.file_size, 0), COALESCE(t.track_number, 1), COALESCE(t.bitrate, 0)
	FROM tracks t
	LEFT JOIN artists ar ON t.artist_id = ar.id
	LEFT JOIN albums al ON t.album_id = al.id
	ORDER BY t.file_path;
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []TrackCandidateDTO
	for rows.Next() {
		var tr TrackCandidateDTO
		var dur float64
		if err := rows.Scan(&tr.FilePath, &tr.Title, &tr.Artist, &tr.Album, &dur, &tr.SizeBytes, &tr.TrackNumber, &tr.Bitrate); err != nil {
			return nil, err
		}
		tr.Duration = int(dur)
		list = append(list, tr)
	}
	return list, nil
}

func (r *Repository) DeleteTrackAndCleanup(ctx context.Context, path string) error {
	var trackID, albumID int64
	err := r.db.QueryRowContext(ctx, "SELECT id, album_id FROM tracks WHERE file_path = ?;", path).Scan(&trackID, &albumID)
	if err == nil {
		_, _ = r.db.ExecContext(ctx, "DELETE FROM tracks WHERE id = ?;", trackID)
		_, _ = r.db.ExecContext(ctx, "DELETE FROM track_genres WHERE track_id = ?;", trackID)
		_, _ = r.db.ExecContext(ctx, "DELETE FROM audit_issues WHERE (entity_type = 'track' AND entity_id = ?) OR file_path = ?;", trackID, path)

		if albumID > 0 {
			var count int
			_ = r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tracks WHERE album_id = ?;", albumID).Scan(&count)
			if count == 0 {
				_, _ = r.db.ExecContext(ctx, "DELETE FROM albums WHERE id = ?;", albumID)
				_, _ = r.db.ExecContext(ctx, "DELETE FROM missing_tracks WHERE album_id = ?;", albumID)
			} else {
				_, _ = r.db.ExecContext(ctx, "UPDATE albums SET present_tracks_count = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;", count, albumID)
			}
		}

		_, _ = r.db.ExecContext(ctx, "DELETE FROM artists WHERE (SELECT COUNT(*) FROM albums WHERE artist_id = artists.id) = 0;")
		return nil
	}

	// Fallback if not found by file_path
	_, err = r.db.ExecContext(ctx, "DELETE FROM tracks WHERE file_path = ?;", path)
	_, _ = r.db.ExecContext(ctx, "DELETE FROM audit_issues WHERE file_path = ?;", path)
	return err
}

// ----------------------------------------------------------------------------
// 3. Incomplete Albums & Missing Tracks
// ----------------------------------------------------------------------------

func (r *Repository) GetIncompleteAlbums(ctx context.Context, limit int) ([]IncompleteAlbumDTO, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `
	SELECT al.id, al.folder_path, al.title, a.name, al.year, al.has_cover, al.cover_path,
	       al.present_tracks_count, al.expected_tracks_count, al.total_tracks, al.deezer_id,
	       COALESCE(al.total_discs, 1), COALESCE((SELECT MAX(MAX(t.disc_number, t.disc_total)) FROM tracks t WHERE t.album_id = al.id), 1)
	FROM albums al
	JOIN artists a ON al.artist_id = a.id
	WHERE al.present_tracks_count > 0
	  AND (al.is_complete = 0 OR (al.expected_tracks_count > 0 AND al.expected_tracks_count > al.present_tracks_count) OR (al.total_tracks > 1 AND al.present_tracks_count < al.total_tracks))
	ORDER BY al.title
	LIMIT ?;
	`
	rows, err := r.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []IncompleteAlbumDTO
	for rows.Next() {
		var item IncompleteAlbumDTO
		var taggedDiscs int
		var yr, exp, tot sql.NullInt64
		var cov, dz sql.NullString
		if err := rows.Scan(
			&item.ID, &item.FolderPath, &item.Title, &item.Artist, &yr, &item.HasCover, &cov,
			&item.PresentCount, &exp, &tot, &dz, &item.TotalDiscs, &taggedDiscs,
		); err != nil {
			return nil, err
		}
		if yr.Valid {
			y := int(yr.Int64)
			item.Year = &y
		}
		if cov.Valid {
			item.CoverPath = &cov.String
		}
		if dz.Valid {
			item.DeezerAlbumID = &dz.String
		}
		item.TotalDiscs = max(item.TotalDiscs, taggedDiscs)
		item.ExpectedCount = int(exp.Int64)
		item.TotalTracks = int(tot.Int64)
		list = append(list, item)
	}
	return list, rows.Err()
}

func (r *Repository) GetPresentTrackNumbers(ctx context.Context, albumID int64) ([]int, error) {
	query := "SELECT track_number FROM tracks WHERE album_id = ? ORDER BY track_number ASC;"
	rows, err := r.db.QueryContext(ctx, query, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nums []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err == nil {
			nums = append(nums, n)
		}
	}
	return nums, nil
}

func (r *Repository) GetAlbumMetadataByPath(ctx context.Context, folderPath string) (*IncompleteAlbumDTO, error) {
	cleanPath := db.AlbumRoot(filepath.ToSlash(folderPath))

	query := `
	SELECT al.id, al.title, ar.name, COALESCE(al.year, 0), COALESCE(al.deezer_id, ''),
	       COALESCE(al.expected_tracks_count, 0), COALESCE(al.present_tracks_count, 0), COALESCE(al.total_tracks, 0)
	FROM albums al
	JOIN artists ar ON al.artist_id = ar.id
	WHERE al.folder_path = ?
	LIMIT 1;
	`
	var item IncompleteAlbumDTO
	var dz string
	var yr int
	err := r.db.QueryRowContext(ctx, query, cleanPath).Scan(
		&item.ID, &item.Title, &item.Artist, &yr, &dz,
		&item.ExpectedCount, &item.PresentCount, &item.TotalTracks,
	)
	if err != nil {
		return nil, err
	}
	if yr > 0 {
		item.Year = &yr
	}
	if dz != "" {
		item.DeezerAlbumID = &dz
	}
	item.FolderPath = cleanPath
	return &item, nil
}

func (r *Repository) VerifyCatalog(ctx context.Context, albumID int64, deezerID string, catalog []db.CatalogTrack) (map[db.TrackPosition]bool, error) {
	conn, ok := r.db.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return nil, fmt.Errorf("catalog verification requires a database connection")
	}
	return db.VerifyCatalog(ctx, conn, albumID, deezerID, catalog)
}

// ----------------------------------------------------------------------------
// 4. Invalid Paths & Reorganization
// ----------------------------------------------------------------------------

func (r *Repository) GetInvalidPaths(ctx context.Context) ([]InvalidPathRecord, error) {
	query := `SELECT file_path, suggested_path, path_violation_reason FROM tracks WHERE is_path_valid = 0;`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []InvalidPathRecord
	for rows.Next() {
		var rec InvalidPathRecord
		var sug, reas sql.NullString
		if err := rows.Scan(&rec.FilePath, &sug, &reas); err != nil {
			return nil, err
		}
		if sug.Valid {
			rec.SuggestedPath = sug.String
		}
		if reas.Valid {
			rec.PathViolationReason = reas.String
		}
		list = append(list, rec)
	}
	return list, nil
}

func (r *Repository) UpdateTrackPath(ctx context.Context, oldPath, newPath string) error {
	query := `
	UPDATE tracks
	SET file_path = ?, suggested_path = ?, is_path_valid = 1, path_violation_reason = '', updated_at = CURRENT_TIMESTAMP
	WHERE file_path = ?;
	`
	_, err := r.db.ExecContext(ctx, query, newPath, newPath, oldPath)
	return err
}

// ----------------------------------------------------------------------------
// 5. Missing Lyrics & Covers
// ----------------------------------------------------------------------------

func (r *Repository) GetTracksMissingLyrics(ctx context.Context, limit int) ([]MissingLyricsItem, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `
	SELECT t.file_path, t.title, a.name, al.title
	FROM tracks t
	JOIN artists a ON t.artist_id = a.id
	JOIN albums al ON t.album_id = al.id
	WHERE t.lyrics_status = 'none'
	LIMIT ?;
	`
	rows, err := r.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []MissingLyricsItem
	for rows.Next() {
		var it MissingLyricsItem
		if err := rows.Scan(&it.Path, &it.Title, &it.Artist, &it.Album); err == nil {
			it.Directory = filepath.Dir(it.Path)
			list = append(list, it)
		}
	}
	return list, nil
}

func (r *Repository) GetTracksForLyricsFetch(ctx context.Context) ([]TrackCandidateDTO, error) {
	query := `
	SELECT t.file_path, t.title, COALESCE(ar.name, ''), COALESCE(al.title, ''), COALESCE(t.duration, 0), 0, 0, 0
	FROM tracks t
	LEFT JOIN artists ar ON t.artist_id = ar.id
	LEFT JOIN albums al ON t.album_id = al.id
	WHERE t.lyrics_status = 'none'
	ORDER BY t.id;
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []TrackCandidateDTO
	for rows.Next() {
		var tr TrackCandidateDTO
		var dur float64
		if err := rows.Scan(&tr.FilePath, &tr.Title, &tr.Artist, &tr.Album, &dur, &tr.SizeBytes, &tr.TrackNumber, &tr.Bitrate); err == nil {
			tr.Duration = int(dur)
			list = append(list, tr)
		}
	}
	return list, nil
}

func (r *Repository) SetTrackLyricsStatusAndResolve(ctx context.Context, path, kind string) error {
	_, err := r.db.ExecContext(ctx, "UPDATE tracks SET lyrics_status = ?, updated_at = CURRENT_TIMESTAMP WHERE file_path = ?;", kind, path)
	if err != nil {
		return err
	}
	_, _ = r.db.ExecContext(ctx, "UPDATE audit_issues SET is_resolved = 1 WHERE entity_type = 'track' AND file_path = ? AND issue_type = 'missing_lyrics';", path)
	return nil
}

func (r *Repository) GetAlbumsMissingCovers(ctx context.Context, limit int) ([]MissingCoverItem, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `
	SELECT al.folder_path, al.title, a.name, COUNT(t.id)
	FROM albums al
	JOIN artists a ON al.artist_id = a.id
	LEFT JOIN tracks t ON t.album_id = al.id
	WHERE al.has_cover = 0
	GROUP BY al.id
	LIMIT ?;
	`
	rows, err := r.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []MissingCoverItem
	for rows.Next() {
		var it MissingCoverItem
		if err := rows.Scan(&it.Path, &it.Title, &it.Artist, &it.TrackCount); err == nil {
			it.Album = it.Title
			it.IsAlbumDir = true
			list = append(list, it)
		}
	}
	return list, nil
}

func (r *Repository) UpdateAlbumCover(ctx context.Context, folderPath, coverPath, mime string) error {
	cleanPath := filepath.Clean(folderPath)
	query := `
	UPDATE albums
	SET cover_path = ?, cover_mime = ?, has_cover = 1, updated_at = CURRENT_TIMESTAMP
	WHERE folder_path = ? OR folder_path = ?;
	`
	_, err := r.db.ExecContext(ctx, query, coverPath, mime, folderPath, cleanPath)
	if err != nil {
		return err
	}
	// Mark tracks as having embedded cover if present
	trackQ := `
	UPDATE tracks
	SET has_embedded_cover = 1, updated_at = CURRENT_TIMESTAMP
	WHERE album_id IN (SELECT id FROM albums WHERE folder_path = ? OR folder_path = ?);
	`
	_, _ = r.db.ExecContext(ctx, trackQ, folderPath, cleanPath)
	return nil
}

// ----------------------------------------------------------------------------
// 6. Non-English Genres
// ----------------------------------------------------------------------------

func (r *Repository) GetNonEnGenreTracks(ctx context.Context) ([]NonEnGenreItem, error) {
	query := `
	SELECT t.file_path, g.name, COALESCE(g.canonical_en_name, '')
	FROM tracks t
	JOIN track_genres tg ON t.id = tg.track_id
	JOIN genres g ON tg.genre_id = g.id
	WHERE g.is_valid_en = 0
	ORDER BY t.file_path;
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []NonEnGenreItem
	for rows.Next() {
		var it NonEnGenreItem
		if err := rows.Scan(&it.Path, &it.CurrentGenre, &it.CanonicalGenre); err == nil {
			list = append(list, it)
		}
	}
	return list, nil
}

func (r *Repository) RetagTrackGenre(ctx context.Context, trackID int64, canonicalGenre string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM track_genres WHERE track_id = ?;", trackID)
	if err != nil {
		return err
	}

	for _, g := range strings.Split(canonicalGenre, "\\") {
		cg := strings.TrimSpace(g)
		if cg == "" {
			continue
		}
		var genreID int64
		err := r.db.QueryRowContext(ctx, "SELECT id FROM genres WHERE name = ?;", cg).Scan(&genreID)
		if err == sql.ErrNoRows {
			res, insErr := r.db.ExecContext(ctx, "INSERT INTO genres (name, is_valid_en, canonical_en_name) VALUES (?, 1, ?);", cg, cg)
			if insErr == nil {
				genreID, _ = res.LastInsertId()
			}
		}
		if genreID > 0 {
			_, _ = r.db.ExecContext(ctx, "INSERT OR IGNORE INTO track_genres (track_id, genre_id) VALUES (?, ?);", trackID, genreID)
		}
	}

	_, _ = r.db.ExecContext(ctx, "UPDATE audit_issues SET is_resolved = 1, is_auto_fixed = 1 WHERE entity_type = 'track' AND entity_id = ? AND issue_type = 'non_en_genre';", trackID)
	return nil
}

// ----------------------------------------------------------------------------
// 7. Tag Issues
// ----------------------------------------------------------------------------

func (r *Repository) GetTracksForTagAudit(ctx context.Context) ([]TagAuditTrackDTO, error) {
	query := `
	SELECT t.id, t.file_path, t.title, a.name, al.title, COALESCE(al.year, 0), t.track_number, t.duration, COALESCE(al.album_artist, '')
	FROM tracks t
	JOIN artists a ON t.artist_id = a.id
	JOIN albums al ON t.album_id = al.id
	ORDER BY t.id DESC;
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []TagAuditTrackDTO
	for rows.Next() {
		var tr TagAuditTrackDTO
		if err := rows.Scan(&tr.ID, &tr.FilePath, &tr.Title, &tr.Artist, &tr.Album, &tr.Year, &tr.TrackNumber, &tr.Duration, &tr.AlbumArtist); err == nil {
			list = append(list, tr)
		}
	}
	return list, nil
}

func (r *Repository) GetTrackForTagAuditByPath(ctx context.Context, path string) (*TagAuditTrackDTO, error) {
	query := `
	SELECT t.id, t.file_path, t.title, a.name, al.title, COALESCE(al.year, 0), t.track_number, t.duration, COALESCE(al.album_artist, '')
	FROM tracks t
	JOIN artists a ON t.artist_id = a.id
	JOIN albums al ON t.album_id = al.id
	WHERE t.file_path = ?;
	`
	var tr TagAuditTrackDTO
	err := r.db.QueryRowContext(ctx, query, path).Scan(
		&tr.ID, &tr.FilePath, &tr.Title, &tr.Artist, &tr.Album, &tr.Year, &tr.TrackNumber, &tr.Duration, &tr.AlbumArtist,
	)
	if err != nil {
		return nil, err
	}
	return &tr, nil
}

func (r *Repository) UpdateTrackMetadata(ctx context.Context, path, title, artist, album string, year int) error {
	query := `
	UPDATE tracks
	SET title = ?, updated_at = CURRENT_TIMESTAMP
	WHERE file_path = ?;
	`
	_, err := r.db.ExecContext(ctx, query, title, path)
	return err
}

// ----------------------------------------------------------------------------
// 8. Audit Issues Log
// ----------------------------------------------------------------------------

func (r *Repository) LogAuditIssue(ctx context.Context, issue AuditIssue) (int64, error) {
	query := `
	INSERT INTO audit_issues (issue_type, entity_type, entity_id, file_path, details, is_resolved, is_auto_fixed, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP);
	`
	res, err := r.db.ExecContext(ctx, query, issue.IssueType, issue.EntityType, issue.EntityID, issue.FilePath, issue.Details, issue.IsResolved, issue.IsAutoFixed)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *Repository) ResolveAuditIssueByPath(ctx context.Context, path, issueType string) error {
	query := `UPDATE audit_issues SET is_resolved = 1, updated_at = CURRENT_TIMESTAMP WHERE file_path = ? AND issue_type = ?;`
	_, err := r.db.ExecContext(ctx, query, path, issueType)
	return err
}

func (r *Repository) DeleteAuditIssueByPath(ctx context.Context, path, issueType string) error {
	query := `DELETE FROM audit_issues WHERE file_path = ? AND issue_type = ?;`
	_, err := r.db.ExecContext(ctx, query, path, issueType)
	return err
}

func (r *Repository) ClearAuditIssues(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM audit_issues;")
	return err
}

func (r *Repository) DeleteTrackByPath(ctx context.Context, path string) error {
	return r.DeleteTrackAndCleanup(ctx, path)
}

func (r *Repository) ListAllTracksForDuplicates(ctx context.Context) ([]TrackCandidateDTO, error) {
	return r.GetTracksForDuplicatesScan(ctx)
}

func (r *Repository) ListTracksForTagAudit(ctx context.Context) ([]TagAuditTrackDTO, error) {
	return r.GetTracksForTagAudit(ctx)
}

func (r *Repository) ListTracksWithoutLyrics(ctx context.Context) ([]TrackCandidateDTO, error) {
	return r.GetTracksForLyricsFetch(ctx)
}

func (r *Repository) GetMissingTracksForAlbum(ctx context.Context, albumPath string) ([]db.CatalogTrack, error) {
	meta, err := r.GetAlbumMetadataByPath(ctx, albumPath)
	if err != nil || meta == nil {
		return nil, err
	}
	query := `
	SELECT title, artist, track_number, COALESCE(disc_number, 1), COALESCE(expected_duration, 0),
	       COALESCE(deezer_id, ''), COALESCE(deezer_url, ''), COALESCE(isrc, '')
	FROM missing_tracks
	WHERE album_id = ?
	ORDER BY disc_number, track_number;
	`
	rows, err := r.db.QueryContext(ctx, query, meta.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []db.CatalogTrack
	for rows.Next() {
		var m db.CatalogTrack
		if err := rows.Scan(&m.Title, &m.Artist, &m.TrackNumber, &m.DiscNumber, &m.Duration, &m.DeezerID, &m.DeezerURL, &m.ISRC); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}
