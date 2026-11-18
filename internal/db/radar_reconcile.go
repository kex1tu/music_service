package db

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"music-manager/internal/core/textkey"
	"music-manager/internal/id3"
)

var artistSeparator = regexp.MustCompile(`\s+(?:feat\.?|ft\.?|vs\.?)\s+|[,;&/\\\x00]+`)

func radarArtistMatches(releaseArtist, localArtist string) bool {
	release, local := textkey.Search(releaseArtist), textkey.Search(localArtist)
	if release == "" || local == "" {
		return false
	}
	if release == local {
		return true
	}
	for _, name := range artistSeparator.Split(release, -1) {
		for _, candidate := range artistSeparator.Split(local, -1) {
			if strings.TrimSpace(name) != "" && strings.TrimSpace(name) == strings.TrimSpace(candidate) {
				return true
			}
		}
	}
	return false
}

type radarAlbum struct {
	id       int64
	deezerID string
	complete bool
	expected int
}

func (db *DB) radarAlbumCandidates(ctx context.Context, release RadarReleaseEntity) ([]radarAlbum, error) {
	key := id3.CleanReleaseTitle(release.Title)
	rows, err := db.conn.QueryContext(ctx, `
		SELECT a.id, a.title, ar.name, COALESCE(a.album_artist, ''), COALESCE(a.year, 0),
			COALESCE(a.deezer_id, ''), COALESCE(a.is_complete, 0),
			COALESCE(a.expected_tracks_count, 0)
		FROM albums a JOIN artists ar ON a.artist_id = ar.id
		WHERE a.clean_title IN (?, ?) OR a.deezer_id = NULLIF(?, '')`,
		key, textkey.Search(release.Title), release.DeezerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var matches []radarAlbum
	for rows.Next() {
		var a radarAlbum
		var title, artist, albumArtist string
		var year int
		if err := rows.Scan(&a.id, &title, &artist, &albumArtist, &year, &a.deezerID, &a.complete, &a.expected); err != nil {
			return nil, err
		}
		if !radarArtistMatches(release.ArtistName, artist) && !radarArtistMatches(release.ArtistName, albumArtist) {
			continue
		}
		if a.deezerID != "" && release.DeezerID != "" {
			if a.deezerID != release.DeezerID {
				continue
			}
		} else if textkey.Search(title) != textkey.Search(release.Title) ||
			(year > 0 && release.Year > 0 && year != release.Year) {
			continue
		}
		matches = append(matches, a)
	}
	return matches, rows.Err()
}

// HasRadarAlbumCandidate checks whether fetching a catalog tracklist could
// help verify an album already on disk.
func (db *DB) HasRadarAlbumCandidate(ctx context.Context, release RadarReleaseEntity) (bool, error) {
	albums, err := db.radarAlbumCandidates(ctx, release)
	return len(albums) > 0, err
}

// RadarReleaseInLibrary applies the same matching rules during catalog discovery
// and background reconciliation.
func (db *DB) RadarReleaseInLibrary(ctx context.Context, release RadarReleaseEntity) (bool, error) {
	if release.isSingleTrack() {
		return db.radarSingleInLibrary(ctx, release)
	}
	albums, err := db.radarAlbumCandidates(ctx, release)
	if err != nil {
		return false, err
	}
	for _, album := range albums {
		if len(release.Tracklist) != 0 {
			matches, localCount, err := catalogMatches(ctx, db.conn, album.id, release.Tracklist)
			if err != nil {
				return false, err
			}
			if len(matches) == len(release.Tracklist) && localCount == len(release.Tracklist) {
				return true, nil
			}
			continue
		}
		// A verified catalog link can stand in for a missing tracklist until
		// the provider is available again.
		if !album.complete || album.expected == 0 || release.DeezerID == "" || album.deezerID != release.DeezerID ||
			(release.TrackCount > 0 && release.TrackCount != album.expected) {
			continue
		}
		complete, err := db.radarHasCompletePositions(ctx, album.id, album.expected)
		if complete || err != nil {
			return complete, err
		}
	}
	return false, nil
}

func singleTrackCredit(release RadarReleaseEntity) (title, artist string) {
	title, artist = release.Title, release.ArtistName
	if len(release.Tracklist) == 1 {
		title = release.Tracklist[0].Title
		if release.Tracklist[0].Artist != "" {
			artist = release.Tracklist[0].Artist
		}
	}
	return title, artist
}

func (r RadarReleaseEntity) isSingleTrack() bool {
	return r.ReleaseType == "single" && r.TrackCount <= 1 && len(r.Tracklist) <= 1
}

func (db *DB) radarSingleInLibrary(ctx context.Context, release RadarReleaseEntity) (bool, error) {
	title, artist := singleTrackCredit(release)
	rows, err := db.conn.QueryContext(ctx, `
		SELECT t.title, a.name FROM tracks t JOIN artists a ON a.id = t.artist_id
		WHERE t.clean_title IN (?, ?)`, id3.CleanReleaseTitle(title), textkey.Search(title))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var localTitle, localArtist string
		if err := rows.Scan(&localTitle, &localArtist); err != nil {
			return false, err
		}
		if textkey.Search(title) == textkey.Search(localTitle) && radarArtistMatches(artist, localArtist) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (db *DB) radarSingleTrackIndex(ctx context.Context, releases []RadarReleaseEntity) (map[string][]string, error) {
	titles := make(map[string]bool)
	for _, release := range releases {
		if release.isSingleTrack() {
			title, _ := singleTrackCredit(release)
			titles[textkey.Search(title)] = true
		}
	}
	tracks := make(map[string][]string)
	if len(titles) == 0 {
		return tracks, nil
	}
	rows, err := db.conn.QueryContext(ctx, `SELECT t.title, ar.name FROM tracks t JOIN artists ar ON ar.id=t.artist_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var title, artist string
		if err := rows.Scan(&title, &artist); err != nil {
			return nil, err
		}
		key := textkey.Search(title)
		if titles[key] {
			tracks[key] = append(tracks[key], artist)
		}
	}
	return tracks, rows.Err()
}

func (db *DB) radarHasCompletePositions(ctx context.Context, albumID int64, expected int) (bool, error) {
	rows, err := db.conn.QueryContext(ctx, `
		SELECT DISTINCT MAX(1, COALESCE(disc_number, 1)), COALESCE(track_number, 0)
		FROM tracks WHERE album_id = ?1 AND NOT EXISTS(
			SELECT 1 FROM missing_tracks WHERE album_id = ?1
		) ORDER BY 1, 2`, albumID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	count, previous := 0, ReleasePosition(1, 0)
	for rows.Next() {
		var p TrackPosition
		if err := rows.Scan(&p.DiscNumber, &p.TrackNumber); err != nil {
			return false, err
		}
		if count == 0 && p != ReleasePosition(1, 1) {
			return false, nil
		}
		if (p.DiscNumber != previous.DiscNumber || p.TrackNumber != previous.TrackNumber+1) &&
			(p.DiscNumber != previous.DiscNumber+1 || p.TrackNumber != 1) {
			return false, nil
		}
		previous = p
		count++
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return count == expected, nil
}

// RadarReleaseRecent is the inbox policy for releases absent from the library.
func RadarReleaseRecent(date string, year int, now time.Time) bool {
	if parsed, err := time.Parse("2006-01-02", date); err == nil {
		age := now.Sub(parsed)
		return age >= -7*24*time.Hour && age <= 365*24*time.Hour
	}
	return year >= now.Year()-1 && year <= now.Year()
}

// ReconcileAllRadarReleases rechecks local statuses without modifying ignored releases.
func (db *DB) ReconcileAllRadarReleases(ctx context.Context) (int, error) {
	rows, err := db.conn.QueryContext(ctx, `
		SELECT id, artist_name, title, release_type, COALESCE(release_date, ''), COALESCE(year, 0),
			COALESCE(track_count, 0), COALESCE(deezer_id, ''), status, COALESCE(tracklist, 'null')
		FROM radar_releases WHERE status IN ('new', 'downloaded', 'in_library', 'missing', 'version_exists')`)
	if err != nil {
		return 0, err
	}
	var releases []RadarReleaseEntity
	for rows.Next() {
		var r RadarReleaseEntity
		var tracklist string
		if err := rows.Scan(&r.ID, &r.ArtistName, &r.Title, &r.ReleaseType, &r.ReleaseDate, &r.Year,
			&r.TrackCount, &r.DeezerID, &r.Status, &tracklist); err != nil {
			rows.Close()
			return 0, err
		}
		if err := json.Unmarshal([]byte(tracklist), &r.Tracklist); err != nil {
			rows.Close()
			return 0, err
		}
		releases = append(releases, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	singles, err := db.radarSingleTrackIndex(ctx, releases)
	if err != nil {
		return 0, err
	}
	type update struct {
		id            int64
		before, after string
	}
	var updates []update
	for _, r := range releases {
		var found bool
		if r.isSingleTrack() {
			title, artist := singleTrackCredit(r)
			for _, localArtist := range singles[textkey.Search(title)] {
				if radarArtistMatches(artist, localArtist) {
					found = true
					break
				}
			}
		} else {
			found, err = db.RadarReleaseInLibrary(ctx, r)
			if err != nil {
				return 0, err
			}
		}
		next := r.Status
		if found {
			next = "in_library"
		} else if r.Status == "in_library" {
			next = "missing"
			if RadarReleaseRecent(r.ReleaseDate, r.Year, time.Now()) {
				next = "new"
			}
		}
		if next != r.Status {
			updates = append(updates, update{r.ID, r.Status, next})
		}
	}
	if len(updates) == 0 {
		return 0, nil
	}
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	count := 0
	for _, u := range updates {
		result, err := tx.ExecContext(ctx, `UPDATE radar_releases SET status = ?, is_radar = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ? AND status = ? AND status <> 'ignored'`, u.after, u.after == "new", u.id, u.before)
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if u.after == "in_library" {
			count += int(n)
		}
	}
	return count, tx.Commit()
}
