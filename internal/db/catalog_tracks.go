package db

import (
	"context"
	"database/sql"
	"fmt"
	"math"

	"music-manager/internal/core/textkey"
	"music-manager/internal/id3"
)

// CatalogTrack carries the tracklist data used to verify a local release.
type CatalogTrack struct {
	BackfillSelection
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	Duration int    `json:"duration"`
	ISRC     string `json:"isrc,omitempty"`
}

type localCatalogTrack struct {
	title, isrc string
	duration    float64
}

func (t CatalogTrack) matches(local localCatalogTrack) bool {
	if t.ISRC != "" && local.isrc != "" {
		return textkey.Search(t.ISRC) == textkey.Search(local.isrc)
	}
	if title := textkey.Search(t.Title); title != "" && title == textkey.Search(local.title) {
		return true
	}
	title := id3.CleanReleaseTitle(t.Title)
	return title != "" && title == id3.CleanReleaseTitle(local.title) &&
		t.Duration > 0 && local.duration > 0 && math.Abs(float64(t.Duration)-local.duration) <= 5
}

type catalogQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func catalogMatches(ctx context.Context, query catalogQuerier, albumID int64, catalog []CatalogTrack) (map[TrackPosition]bool, int, error) {
	rows, err := query.QueryContext(ctx, `SELECT COALESCE(disc_number, 1), COALESCE(track_number, 0),
		title, COALESCE(isrc, ''), COALESCE(duration, 0) FROM tracks WHERE album_id = ?`, albumID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	localTracks := make(map[TrackPosition][]localCatalogTrack)
	for rows.Next() {
		var disc, track int
		var local localCatalogTrack
		if err := rows.Scan(&disc, &track, &local.title, &local.isrc, &local.duration); err != nil {
			return nil, 0, err
		}
		position := ReleasePosition(disc, track)
		localTracks[position] = append(localTracks[position], local)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	matched := make(map[TrackPosition]bool)
	for _, remote := range catalog {
		for _, local := range localTracks[remote.TrackPosition] {
			if remote.matches(local) {
				matched[remote.TrackPosition] = true
				break
			}
		}
	}
	return matched, len(localTracks), nil
}

// VerifyCatalog updates the gap rows and the album's verification in one transaction.
func VerifyCatalog(ctx context.Context, conn interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}, albumID int64, deezerID string, catalog []CatalogTrack) (map[TrackPosition]bool, error) {
	if len(catalog) == 0 {
		return nil, fmt.Errorf("empty catalog tracklist")
	}
	catalogPositions := make(map[TrackPosition]bool, len(catalog))
	discs := 1
	for _, track := range catalog {
		if track.DiscNumber < 1 || track.TrackNumber < 1 || catalogPositions[track.TrackPosition] {
			return nil, fmt.Errorf("invalid or duplicate catalog position %+v", track.TrackPosition)
		}
		catalogPositions[track.TrackPosition] = true
		discs = max(discs, track.DiscNumber)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Reserve the writer before reading the local tracks to verify one snapshot.
	var lockedID int64
	if err := tx.QueryRowContext(ctx, "UPDATE albums SET updated_at = updated_at WHERE id = ? RETURNING id", albumID).Scan(&lockedID); err != nil {
		return nil, err
	}
	matched, _, err := catalogMatches(ctx, tx, albumID, catalog)
	if err != nil {
		return nil, err
	}
	for _, track := range catalog {
		if matched[track.TrackPosition] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO missing_tracks
			(album_id, track_number, disc_number, title, artist, expected_duration, deezer_id, deezer_url, isrc, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(album_id, track_number, disc_number) DO UPDATE SET
				title = excluded.title, artist = excluded.artist, expected_duration = excluded.expected_duration,
				deezer_id = excluded.deezer_id, deezer_url = excluded.deezer_url, isrc = excluded.isrc,
				updated_at = CURRENT_TIMESTAMP`,
			albumID, track.TrackNumber, track.DiscNumber, track.Title, track.Artist, track.Duration,
			track.DeezerID, track.DeezerURL, track.ISRC); err != nil {
			return nil, err
		}
	}

	rows, err := tx.QueryContext(ctx, "SELECT id, COALESCE(disc_number, 1), track_number FROM missing_tracks WHERE album_id = ?", albumID)
	if err != nil {
		return nil, err
	}
	var closed []int64
	for rows.Next() {
		var id int64
		var disc, track int
		if err := rows.Scan(&id, &disc, &track); err != nil {
			rows.Close()
			return nil, err
		}
		position := ReleasePosition(disc, track)
		if matched[position] || !catalogPositions[position] {
			closed = append(closed, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, id := range closed {
		if _, err := tx.ExecContext(ctx, "DELETE FROM missing_tracks WHERE id = ?", id); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE albums SET is_complete = ?, expected_tracks_count = ?,
		present_tracks_count = ?, total_tracks = ?, total_discs = ?, deezer_id = NULLIF(?, ''),
		updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		len(matched) == len(catalog), len(catalog), len(matched), len(catalog), discs, deezerID, albumID); err != nil {
		return nil, err
	}
	return matched, tx.Commit()
}
