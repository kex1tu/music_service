package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"

	"music-manager/internal/core/textkey"
)

// QueryLibrarySearch keeps the same normalization and error handling for all callers.
// Audit searches track and album titles. Library search also matches artist credits.
func QueryLibrarySearch(ctx context.Context, conn catalogQuerier, query string, limit int, titlesOnly bool) (*SearchLibraryResult, error) {
	if limit <= 0 {
		limit = 50
	}
	args := []any{textkey.Search(query), limit, titlesOnly}
	tracks, err := queryLibraryRows(ctx, conn, `
		SELECT t.id, t.title, ar.name, al.title, COALESCE(al.year, 0), COALESCE(t.duration, 0),
			COALESCE(t.bitrate, 0), t.file_path, COALESCE(t.lyrics_status, 'none'), COALESCE(al.has_cover, 0), COALESCE(al.cover_path, '')
		FROM tracks t JOIN albums al ON al.id = t.album_id JOIN artists ar ON ar.id = t.artist_id
		WHERE instr(t.search_title, ?1) > 0 OR instr(t.search_clean_title, ?1) > 0
			OR (NOT ?3 AND instr(ar.clean_name, ?1) > 0)
		ORDER BY (instr(t.search_title, ?1) > 0 OR instr(t.search_clean_title, ?1) > 0) DESC, t.id
		LIMIT ?2`, args, func(row *sql.Rows, t *SearchTrackItem) error {
		err := row.Scan(&t.ID, &t.Title, &t.Artist, &t.Album, &t.Year, &t.Duration, &t.Bitrate,
			&t.Path, &t.LyricsStatus, &t.HasCover, &t.CoverPath)
		t.Path = filepath.ToSlash(t.Path)
		return err
	})
	if err != nil {
		return nil, err
	}
	albums, err := queryLibraryRows(ctx, conn, `
		SELECT al.id, al.title, ar.name, COALESCE(al.album_artist, ''), COALESCE(al.year, 0), al.folder_path,
			COALESCE(al.has_cover, 0), COALESCE(al.cover_path, ''),
			(SELECT COUNT(*) FROM tracks WHERE album_id = al.id)
		FROM albums al JOIN artists ar ON al.artist_id = ar.id
		WHERE instr(al.search_title, ?1) > 0 OR instr(al.search_clean_title, ?1) > 0
			OR (NOT ?3 AND al.id IN (`+SearchAlbumIDsByArtist+`))
		ORDER BY (instr(al.search_title, ?1) > 0 OR instr(al.search_clean_title, ?1) > 0) DESC, al.id
		LIMIT ?2`, args, func(row *sql.Rows, a *SearchAlbumItem) error {
		err := row.Scan(&a.ID, &a.Title, &a.PrimaryArtist, &a.RawAlbumArtist, &a.Year, &a.Path, &a.HasCover, &a.CoverPath, &a.TrackCount)
		a.Album, a.AlbumArtist = a.Title, a.RawAlbumArtist
		if a.AlbumArtist == "" {
			a.AlbumArtist = a.PrimaryArtist
		}
		a.Artist, a.Path = a.AlbumArtist, filepath.ToSlash(a.Path)
		return err
	})
	if err != nil {
		return nil, err
	}
	artists, err := queryLibraryRows(ctx, conn, `
		SELECT ar.id, ar.name, COUNT(t.id), COALESCE((`+FirstCreditedAlbumPath+`), ''),
			COALESCE(MIN(t.file_path), ''), COALESCE(MIN(al.folder_path), '')
		FROM artists ar LEFT JOIN tracks t ON t.artist_id = ar.id LEFT JOIN albums al ON al.id = t.album_id
		WHERE instr(ar.clean_name, ?1) > 0 AND
			(t.id IS NOT NULL OR EXISTS (SELECT 1 FROM album_artists aa WHERE aa.artist_id = ar.id)
			 OR (?3 AND EXISTS (SELECT 1 FROM albums owned WHERE owned.artist_id = ar.id)))
		GROUP BY ar.id ORDER BY ar.id LIMIT ?2`, args, func(row *sql.Rows, a *SearchArtistItem) error {
		var trackFolder string
		err := row.Scan(&a.ID, &a.Name, &a.TrackCount, &a.CreditFolder, &a.TrackPath, &trackFolder)
		a.Title, a.FirstPath = a.Name, a.CreditFolder
		if a.FirstPath == "" {
			a.FirstPath = trackFolder
		}
		if a.FirstPath == "" {
			a.FirstPath = a.TrackPath
		}
		a.FirstPath = filepath.ToSlash(a.FirstPath)
		a.FolderPath, _, _ = strings.Cut(a.FirstPath, "/")
		return err
	})
	if err != nil {
		return nil, err
	}
	return &SearchLibraryResult{Query: query, Tracks: tracks, Albums: albums, Artists: artists,
		TotalMatches: len(tracks) + len(albums) + len(artists)}, nil
}

func queryLibraryRows[T any](ctx context.Context, conn catalogQuerier, query string, args []any, scan func(*sql.Rows, *T) error) ([]T, error) {
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]T, 0)
	for rows.Next() {
		var item T
		if err := scan(rows, &item); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
