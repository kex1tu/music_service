package db

import (
	"context"
	"fmt"
	"strings"
)

// RecordRadioPlay фиксирует факт воспроизведения трека радиостанцией.
func (db *DB) RecordRadioPlay(ctx context.Context, trackID int64, stationType, stationParam string) error {
	query := `INSERT INTO radio_history (track_id, station_type, station_param) VALUES (?, ?, ?);`
	_, err := db.conn.ExecContext(ctx, query, trackID, stationType, stationParam)
	return err
}

// GetRecentRadioTracks возвращает ID треков, игравших за последние N часов.
func (db *DB) GetRecentRadioTracks(ctx context.Context, stationType string, hours int) ([]int64, error) {
	if hours <= 0 {
		hours = 6
	}
	query := `
	SELECT DISTINCT track_id
	FROM radio_history
	WHERE station_type = ? AND played_at >= datetime('now', ?);
	`
	timeMod := fmt.Sprintf("-%d hours", hours)
	rows, err := db.conn.QueryContext(ctx, query, stationType, timeMod)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

// GetRecentRadioArtists возвращает имена артистов последних N сыгранных треков.
func (db *DB) GetRecentRadioArtists(ctx context.Context, stationType string, count int) ([]string, error) {
	if count <= 0 {
		count = 5
	}
	query := `
	SELECT DISTINCT LOWER(a.name)
	FROM radio_history rh
	JOIN tracks t ON rh.track_id = t.id
	JOIN artists a ON t.artist_id = a.id
	WHERE rh.station_type = ?
	ORDER BY rh.played_at DESC
	LIMIT ?;
	`
	rows, err := db.conn.QueryContext(ctx, query, stationType, count)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		result = append(result, name)
	}
	return result, rows.Err()
}

// GetWaveCandidateTracks возвращает случайную выборку треков медиатеки для селектора радио.
func (db *DB) GetWaveCandidateTracks(ctx context.Context, limit int) ([]TrackEntity, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `
	SELECT t.id, t.album_id, t.artist_id, t.title, ar.name AS artist, al.title AS album,
	       COALESCE(t.track_number, 1), COALESCE(t.duration, 0.0), COALESCE(t.bitrate, 320), t.file_path, t.file_size,
	       COALESCE(t.file_hash, ''), COALESCE(t.has_embedded_cover, 0), COALESCE(t.lyrics_status, 'none'),
	       COALESCE(al.has_cover, 0), COALESCE(al.cover_path, '')
	FROM tracks t
	JOIN albums al ON t.album_id = al.id
	JOIN artists ar ON t.artist_id = ar.id
	ORDER BY RANDOM()
	LIMIT ?;
	`
	rows, err := db.conn.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TrackEntity
	for rows.Next() {
		var t TrackEntity
		err := rows.Scan(
			&t.ID, &t.AlbumID, &t.ArtistID, &t.Title, &t.Artist, &t.Album,
			&t.TrackNumber, &t.Duration, &t.Bitrate, &t.FilePath, &t.FileSize,
			&t.FileHash, &t.HasEmbeddedCover, &t.LyricsStatus,
			&t.HasCover, &t.CoverPath,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

// GetTracksByGenre возвращает случайную выборку треков указанного жанра.
func (db *DB) GetTracksByGenre(ctx context.Context, genreName string, limit int) ([]TrackEntity, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
	SELECT t.id, t.album_id, t.artist_id, t.title, ar.name AS artist, al.title AS album,
	       t.track_number, t.duration, t.bitrate, t.file_path, t.file_size,
	       COALESCE(t.file_hash, ''), t.has_embedded_cover, t.lyrics_status,
	       al.has_cover, COALESCE(al.cover_path, '')
	FROM tracks t
	JOIN albums al ON t.album_id = al.id
	JOIN artists ar ON t.artist_id = ar.id
	JOIN track_genres tg ON tg.track_id = t.id
	JOIN genres g ON tg.genre_id = g.id
	WHERE LOWER(g.name) = LOWER(?)
	ORDER BY RANDOM()
	LIMIT ?;
	`
	rows, err := db.conn.QueryContext(ctx, query, genreName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TrackEntity
	for rows.Next() {
		var t TrackEntity
		err := rows.Scan(
			&t.ID, &t.AlbumID, &t.ArtistID, &t.Title, &t.Artist, &t.Album,
			&t.TrackNumber, &t.Duration, &t.Bitrate, &t.FilePath, &t.FileSize,
			&t.FileHash, &t.HasEmbeddedCover, &t.LyricsStatus,
			&t.HasCover, &t.CoverPath,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

// GetTracksByArtists возвращает выборку треков указанных исполнителей.
func (db *DB) GetTracksByArtists(ctx context.Context, artistNames []string, limit int) ([]TrackEntity, error) {
	if len(artistNames) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}

	var placeholders []string
	var args []any
	for _, a := range artistNames {
		placeholders = append(placeholders, "?")
		args = append(args, strings.ToLower(strings.TrimSpace(a)))
	}
	args = append(args, limit)

	query := fmt.Sprintf(`
	SELECT t.id, t.album_id, t.artist_id, t.title, ar.name AS artist, al.title AS album,
	       t.track_number, t.duration, t.bitrate, t.file_path, t.file_size,
	       COALESCE(t.file_hash, ''), t.has_embedded_cover, t.lyrics_status,
	       al.has_cover, COALESCE(al.cover_path, '')
	FROM tracks t
	JOIN albums al ON t.album_id = al.id
	JOIN artists ar ON t.artist_id = ar.id
	WHERE LOWER(ar.name) IN (%s)
	ORDER BY RANDOM()
	LIMIT ?;
	`, strings.Join(placeholders, ", "))

	rows, err := db.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TrackEntity
	for rows.Next() {
		var t TrackEntity
		err := rows.Scan(
			&t.ID, &t.AlbumID, &t.ArtistID, &t.Title, &t.Artist, &t.Album,
			&t.TrackNumber, &t.Duration, &t.Bitrate, &t.FilePath, &t.FileSize,
			&t.FileHash, &t.HasEmbeddedCover, &t.LyricsStatus,
			&t.HasCover, &t.CoverPath,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

// GetAvailableGenres возвращает топ жанров медиатеки с количеством треков.
func (db *DB) GetAvailableGenres(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 30
	}
	query := `
	SELECT g.name
	FROM genres g
	JOIN track_genres tg ON tg.genre_id = g.id
	GROUP BY g.id, g.name
	ORDER BY COUNT(tg.track_id) DESC
	LIMIT ?;
	`
	rows, err := db.conn.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil && name != "" {
			result = append(result, name)
		}
	}
	return result, rows.Err()
}
