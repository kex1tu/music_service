package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"music-manager/internal/core/textkey"
	"music-manager/internal/id3"
)

// CatalogTrack carries the tracklist data used to verify a local release.
type CatalogTrack struct {
	BackfillSelection
	Title          string `json:"title"`
	Artist         string `json:"artist"`
	ArtistID       int64  `json:"artist_id,omitempty"`
	PrimaryArtist  string `json:"-"`
	DeezerArtistID string `json:"-"`
	Duration       int    `json:"duration"`
	ISRC           string `json:"isrc,omitempty"`
	Present        bool   `json:"-"`
}

type localCatalogTrack struct {
	title, isrc string
	artistID    int64
	duration    float64
}

func (t CatalogTrack) matches(local localCatalogTrack) bool {
	if t.ISRC != "" && local.isrc != "" {
		return textkey.Search(t.ISRC) == textkey.Search(local.isrc)
	}
	if t.ArtistID != 0 && t.ArtistID != local.artistID {
		return false
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
		title, COALESCE(isrc, ''), COALESCE(duration, 0), artist_id FROM tracks WHERE album_id = ?`, albumID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	localTracks := make(map[TrackPosition][]localCatalogTrack)
	for rows.Next() {
		var disc, track int
		var local localCatalogTrack
		if err := rows.Scan(&disc, &track, &local.title, &local.isrc, &local.duration, &local.artistID); err != nil {
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

var ErrCatalogAlbumChanged = errors.New("local album changed during catalog lookup")

// VerifyCatalog replaces the tracklist and updates the album's verification in one transaction.
func VerifyCatalog(ctx context.Context, conn interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}, albumID int64, deezerID string, catalog []CatalogTrack, expectedRevision ...int64) (map[TrackPosition]bool, error) {
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
	var lockedID, revision int64
	if err := tx.QueryRowContext(ctx, `UPDATE albums SET catalog_revision = catalog_revision + 1
		WHERE id = ? RETURNING artist_id, catalog_revision - 1`, albumID).Scan(&lockedID, &revision); err != nil {
		return nil, err
	}
	if len(expectedRevision) != 0 && revision != expectedRevision[0] {
		return nil, ErrCatalogAlbumChanged
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM release_tracklist WHERE album_id = ?", albumID); err != nil {
		return nil, err
	}
	for _, track := range catalog {
		name := track.PrimaryArtist
		if name == "" {
			if credits := id3.ParseMultiValue(track.Artist); len(credits) > 0 {
				name = credits[0]
			}
		}
		artistID, err := ResolveCatalogArtist(ctx, tx, name, track.DeezerArtistID, lockedID)
		if err != nil {
			return nil, fmt.Errorf("позиция %d/%d (%s): %w", track.DiscNumber, track.TrackNumber, track.Title, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO release_tracklist
			(album_id, track_number, disc_number, title, artist_id, artist_credit,
			 expected_duration, deezer_id, deezer_url, isrc)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, albumID, track.TrackNumber, track.DiscNumber,
			track.Title, artistID, track.Artist, track.Duration, track.DeezerID, track.DeezerURL, track.ISRC); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE albums SET catalog_checked_at = CURRENT_TIMESTAMP,
		total_tracks = ?, total_discs = ?, deezer_id = NULLIF(?, '') WHERE id = ?`,
		len(catalog), discs, deezerID, albumID); err != nil {
		return nil, err
	}
	matched, err := RefreshReleaseCompleteness(ctx, tx, albumID)
	if err != nil {
		return nil, err
	}
	return matched, tx.Commit()
}

// ReleaseTracklist reads the saved positions. A checked release always has a
// complete snapshot, even when every position is present or absent locally.
func ReleaseTracklist(ctx context.Context, conn catalogQuerier, albumID int64, missingOnly bool) ([]CatalogTrack, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT r.title, COALESCE(NULLIF(r.artist_credit, ''), a.name), r.artist_id, r.track_number,
			r.disc_number, r.expected_duration, r.deezer_id, r.deezer_url, r.isrc, r.is_present
		FROM release_tracklist r JOIN artists a ON a.id = r.artist_id
		WHERE r.album_id = ? AND (NOT ? OR r.is_present = 0)
		ORDER BY r.disc_number, r.track_number`, albumID, missingOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tracks []CatalogTrack
	for rows.Next() {
		var t CatalogTrack
		if err := rows.Scan(&t.Title, &t.Artist, &t.ArtistID, &t.TrackNumber, &t.DiscNumber,
			&t.Duration, &t.DeezerID, &t.DeezerURL, &t.ISRC, &t.Present); err != nil {
			return nil, err
		}
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

// RefreshReleaseCompleteness derives presence from the same matching rules as
// the initial verification. This runs in the transaction changing local tracks.
func RefreshReleaseCompleteness(ctx context.Context, tx *sql.Tx, albumID int64) (map[TrackPosition]bool, error) {
	catalog, err := ReleaseTracklist(ctx, tx, albumID, false)
	if err != nil {
		return nil, err
	}
	if len(catalog) == 0 {
		_, err := tx.ExecContext(ctx, `UPDATE albums SET is_complete = 0, expected_tracks_count = 0,
			present_tracks_count = (SELECT COUNT(*) FROM (
				SELECT DISTINCT MAX(1, COALESCE(disc_number, 1)), track_number
				FROM tracks WHERE album_id = ?1 AND track_number > 0)),
			total_tracks = COALESCE((SELECT MAX(track_total) FROM tracks WHERE album_id = ?1), 0),
			total_discs = COALESCE((SELECT MAX(MAX(COALESCE(disc_number, 1), COALESCE(disc_total, 1))) FROM tracks WHERE album_id = ?1), 1),
			updated_at = CURRENT_TIMESTAMP WHERE id = ?1`, albumID)
		return nil, err
	}
	matched, _, err := catalogMatches(ctx, tx, albumID, catalog)
	if err != nil {
		return nil, err
	}
	present := make([]TrackPosition, 0, len(matched))
	for position := range matched {
		present = append(present, position)
	}
	payload, err := json.Marshal(present)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE release_tracklist SET is_present = EXISTS (
			SELECT 1 FROM json_each(?2) p WHERE json_extract(p.value, '$.disc_number') = disc_number
				AND json_extract(p.value, '$.track_number') = track_number)
		WHERE album_id = ?1`, albumID, string(payload)); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE albums SET is_complete = ?, expected_tracks_count = ?,
		present_tracks_count = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		len(matched) == len(catalog), len(catalog), len(matched), albumID)
	return matched, err
}
