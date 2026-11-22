package scanner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"music-manager/internal/core/safefs"
	"music-manager/internal/db"
	"music-manager/internal/id3"
)

type artistConflictError struct {
	name  string
	album bool
}

func (e *artistConflictError) Error() string {
	return fmt.Sprintf("%v: %s", db.ErrAmbiguousArtist, e.name)
}
func (e *artistConflictError) Unwrap() error { return db.ErrAmbiguousArtist }

func resolveIndexedArtist(ctx context.Context, tx *sql.Tx, path, dir, name string, previous int64, tags *id3.TrackTags, albumArtist bool) (int64, error) {
	var id sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT resolved_artist_id FROM library_artist_conflicts
		WHERE file_path = ? AND album_artist = ? AND artist_name = ?`, path, albumArtist, name).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if id.Valid {
		return id.Int64, nil
	}
	root := db.AlbumRoot(dir)
	err = tx.QueryRowContext(ctx, `SELECT id FROM artists WHERE id = ? AND (name = ?2 OR clean_name = music_search_key(?2))`, previous, name).Scan(&id)
	if err == nil {
		return id.Int64, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if !albumArtist && tags.HasTrackNumber {
		err = tx.QueryRowContext(ctx, `SELECT a.id FROM release_tracklist r
			JOIN albums al ON al.id = r.album_id JOIN artists a ON a.id = r.artist_id
			WHERE al.folder_path = ? AND r.disc_number = ? AND r.track_number = ?
			AND music_search_key(r.title) = music_search_key(?) AND a.clean_name = music_search_key(?)`,
			root, tags.DiscNumber, tags.TrackNumber, tags.Title, name).Scan(&id)
		if err == nil {
			return id.Int64, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	err = tx.QueryRowContext(ctx, `WITH related(id) AS (
		SELECT a.artist_id FROM albums a WHERE a.folder_path = ?1
		UNION SELECT t.artist_id FROM tracks t JOIN albums a ON a.id = t.album_id WHERE a.folder_path = ?1 AND NOT ?3
	)
	SELECT CASE WHEN COUNT(*) = 1 THEN MIN(a.id) END
	FROM related JOIN artists a USING (id) WHERE a.clean_name = music_search_key(?2)`, root, name, albumArtist).Scan(&id)
	if err != nil {
		return 0, err
	}
	if id.Valid {
		return id.Int64, nil
	}
	resolved, err := getOrCreateArtist(ctx, tx, name)
	if errors.Is(err, db.ErrAmbiguousArtist) {
		return 0, &artistConflictError{name: name, album: albumArtist}
	}
	return resolved, err
}

func (s *Scanner) ArtistConflicts(ctx context.Context) ([]db.ArtistConflict, error) {
	rows, err := s.database.Conn().QueryContext(ctx, `SELECT c.file_path, c.artist_name, c.album_artist,
		a.id, a.name, COALESCE(a.deezer_id, '') FROM library_artist_conflicts c
		JOIN artists a ON a.clean_name = music_search_key(c.artist_name) OR a.name = c.artist_name
		WHERE c.resolved_artist_id IS NULL ORDER BY c.file_path, c.album_artist, a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]db.ArtistConflict, 0)
	for rows.Next() {
		var conflict db.ArtistConflict
		var choice db.ArtistChoice
		if err := rows.Scan(&conflict.Path, &conflict.Name, &conflict.AlbumArtist, &choice.ID, &choice.Name, &choice.DeezerID); err != nil {
			return nil, err
		}
		if n := len(out); n != 0 && out[n-1].Path == conflict.Path && out[n-1].AlbumArtist == conflict.AlbumArtist {
			out[n-1].Choices = append(out[n-1].Choices, choice)
		} else {
			conflict.Choices = []db.ArtistChoice{choice}
			out = append(out, conflict)
		}
	}
	return out, rows.Err()
}

func (s *Scanner) ResolveArtistConflict(ctx context.Context, path string, albumArtist bool, artistID int64) error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	full, err := s.resolveIndexedPath(path)
	if err != nil {
		return err
	}
	if _, err := safefs.Stat(full); err != nil {
		return err
	}
	result, err := s.database.Conn().ExecContext(ctx, `UPDATE library_artist_conflicts
		SET resolved_artist_id = ?3 WHERE file_path = ?1 AND album_artist = ?2
		AND EXISTS (SELECT 1 FROM artists a WHERE a.id = ?3 AND
			(a.name = artist_name OR a.clean_name = music_search_key(artist_name)))`, path, albumArtist, artistID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	err = s.indexFileLocked(ctx, path, "", "", false)
	if errors.Is(err, db.ErrAmbiguousArtist) {
		return nil
	}
	if err != nil {
		_, resetErr := s.database.Conn().ExecContext(context.WithoutCancel(ctx), `UPDATE library_artist_conflicts
			SET resolved_artist_id = NULL WHERE file_path = ? AND album_artist = ?`, path, albumArtist)
		return errors.Join(err, resetErr)
	}
	return err
}
