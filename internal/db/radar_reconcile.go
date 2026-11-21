package db

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"

	"music-manager/internal/core/textkey"
)

var artistSeparator = regexp.MustCompile(`(?i)\s+(?:feat\.?|ft\.?|vs\.?)\s+|[,;&/\\\x00]+`)

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

// sameAlbumCredit compares the whole legacy credit across catalog separators.
func sameAlbumCredit(catalog, local string) bool {
	catalogParts := artistSeparator.Split(textkey.Search(catalog), -1)
	localParts := artistSeparator.Split(textkey.Search(local), -1)
	return len(catalogParts) > 1 && slices.EqualFunc(catalogParts, localParts, func(a, b string) bool {
		return strings.TrimSpace(a) != "" && strings.TrimSpace(a) == strings.TrimSpace(b)
	})
}

type radarAlbum struct {
	id       int64
	deezerID string
	complete bool
	expected int
}

func (db *DB) radarAlbumCandidates(ctx context.Context, release RadarReleaseEntity) ([]radarAlbum, error) {
	rows, err := db.conn.QueryContext(ctx, `
		WITH linked_credits(artist_id) AS (
			SELECT artist_id FROM radar_release_artists WHERE release_id = ?1 AND artist_id IS NOT NULL
		), credited(artist_id) AS (
			SELECT artist_id FROM linked_credits
			UNION
			SELECT `+ArtistIDLookup("names.value")+`
			FROM json_each(music_artist_credit_parts(?2)) AS names
			WHERE NOT EXISTS (SELECT 1 FROM linked_credits)
			  AND (names.value = ?2 OR `+ArtistIDLookup("?2")+` IS NULL)
		)
		SELECT a.id, ar.name, COALESCE(a.album_artist, ''), COALESCE(a.year, 0),
			COALESCE(a.deezer_id, ''), COALESCE(a.is_complete, 0),
			COALESCE(a.expected_tracks_count, 0), EXISTS (
				SELECT 1 FROM album_artists aa JOIN credited c ON c.artist_id = aa.artist_id WHERE aa.album_id = a.id
			), EXISTS (SELECT 1 FROM album_artists aa WHERE aa.album_id = a.id)
		FROM albums a JOIN artists ar ON a.artist_id = ar.id
		WHERE a.search_title = ?3 OR a.deezer_id = NULLIF(?4, '')`,
		release.ID, release.ArtistName, textkey.Search(release.Title), release.DeezerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var matches []radarAlbum
	for rows.Next() {
		var a radarAlbum
		var artist, albumArtist string
		var year int
		var linked, hasCredits bool
		if err := rows.Scan(&a.id, &artist, &albumArtist, &year, &a.deezerID, &a.complete, &a.expected, &linked, &hasCredits); err != nil {
			return nil, err
		}
		if a.deezerID != "" && release.DeezerID != "" {
			if a.deezerID != release.DeezerID {
				continue
			}
		} else {
			if year > 0 && release.Year > 0 && year != release.Year {
				continue
			}
			// Older or display-only credits can still select a candidate for tracklist verification.
			legacyMatch := !hasCredits && (radarArtistMatches(release.ArtistName, artist) || radarArtistMatches(release.ArtistName, albumArtist))
			if !linked && !legacyMatch && !sameAlbumCredit(release.ArtistName, albumArtist) {
				continue
			}
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
		SELECT a.name, EXISTS (SELECT 1 FROM radar_release_artists credit
			WHERE credit.release_id = ? AND credit.artist_id = t.artist_id)
		FROM tracks t JOIN artists a ON a.id = t.artist_id
		WHERE t.search_title = ?`, release.ID, textkey.Search(title))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var localArtist string
		var linked bool
		if err := rows.Scan(&localArtist, &linked); err != nil {
			return false, err
		}
		if linked || radarArtistMatches(artist, localArtist) {
			return true, nil
		}
	}
	return false, rows.Err()
}

type radarArtist struct {
	id   int64
	name string
}

func (db *DB) radarSingleTrackIndex(ctx context.Context, releases []RadarReleaseEntity) (map[string][]radarArtist, error) {
	titles := make(map[string]bool)
	for _, release := range releases {
		if release.isSingleTrack() {
			title, _ := singleTrackCredit(release)
			titles[textkey.Search(title)] = true
		}
	}
	tracks := make(map[string][]radarArtist)
	if len(titles) == 0 {
		return tracks, nil
	}
	rows, err := db.conn.QueryContext(ctx, `SELECT t.search_title, ar.id, ar.name FROM tracks t JOIN artists ar ON ar.id=t.artist_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var title string
		var artist radarArtist
		if err := rows.Scan(&title, &artist.id, &artist.name); err != nil {
			return nil, err
		}
		if titles[title] {
			tracks[title] = append(tracks[title], artist)
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

// radarSingleArtistLinks reads identities in bulk for the same reconciliation pass.
func (db *DB) radarSingleArtistLinks(ctx context.Context) (map[int64]map[int64]bool, error) {
	rows, err := db.conn.QueryContext(ctx, `
		SELECT credit.release_id, credit.artist_id
		FROM radar_release_artists credit JOIN radar_releases rr ON rr.id = credit.release_id
		WHERE credit.artist_id IS NOT NULL AND rr.release_type = 'single' AND rr.track_count <= 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	links := make(map[int64]map[int64]bool)
	for rows.Next() {
		var releaseID, artistID int64
		if err := rows.Scan(&releaseID, &artistID); err != nil {
			return nil, err
		}
		if links[releaseID] == nil {
			links[releaseID] = make(map[int64]bool)
		}
		links[releaseID][artistID] = true
	}
	return links, rows.Err()
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
			COALESCE(track_count, 0), COALESCE(deezer_id, ''), status, COALESCE(tracklist, '')
		FROM radar_releases rr
		WHERE status IN ('new', 'downloaded', 'in_library', 'missing', 'version_exists')
			AND (status = 'in_library' OR rr.release_type <> 'single' OR rr.track_count > 1
				OR rr.tracklist IS NOT NULL OR EXISTS (
					SELECT 1 FROM tracks t WHERE t.search_title = rr.search_title
				))`)
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
		if tracklist != "" {
			if err := json.Unmarshal([]byte(tracklist), &r.Tracklist); err != nil {
				rows.Close()
				return 0, err
			}
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
	var linkedArtists map[int64]map[int64]bool
	if len(singles) != 0 {
		linkedArtists, err = db.radarSingleArtistLinks(ctx)
		if err != nil {
			return 0, err
		}
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
				if linkedArtists[r.ID][localArtist.id] || radarArtistMatches(artist, localArtist.name) {
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
