package scanner

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	"music-manager/internal/services/resolver"
)

type incompleteAlbum struct {
	ID          int64
	Artist      string
	Title       string
	TotalTracks int
	DeezerID    string
}

type localTrackItem struct {
	TrackNumber int
	Title       string
	Duration    float64
}

func selectBestDeezerAlbumScanner(candidates []resolver.DeezerAlbum, dbArtist, dbAlbum string, localTrackCount int) *resolver.DeezerAlbum {
	if len(candidates) == 0 {
		return nil
	}

	normTargetAlbum := strings.ToLower(resolver.CleanQueryText(dbAlbum))
	primaryArtist := resolver.ExtractPrimaryArtist(dbArtist)
	normTargetArtist := strings.ToLower(resolver.CleanQueryText(primaryArtist))

	var best *resolver.DeezerAlbum
	var bestScore float64 = -1.0

	for i := range candidates {
		cand := &candidates[i]
		candAlbum := strings.ToLower(resolver.CleanQueryText(cand.Title))
		candArtist := strings.ToLower(resolver.CleanQueryText(cand.Artist.Name))

		score := 0.0

		// Title similarity
		if candAlbum == normTargetAlbum {
			score += 50.0
		} else if strings.Contains(candAlbum, normTargetAlbum) || strings.Contains(normTargetAlbum, candAlbum) {
			score += 35.0
		} else {
			continue
		}

		// Artist similarity
		if normTargetArtist != "" {
			if candArtist == normTargetArtist || resolver.AreArtistsEquivalent(cand.Artist.Name, primaryArtist) {
				score += 30.0
			} else if strings.Contains(candArtist, normTargetArtist) || strings.Contains(normTargetArtist, candArtist) {
				score += 20.0
			} else {
				for _, variant := range resolver.TransliterateArtist(normTargetArtist) {
					if strings.Contains(candArtist, variant) || strings.Contains(variant, candArtist) {
						score += 20.0
						break
					}
				}
			}
		} else {
			score += 15.0
		}

		// Track count sanity check
		if localTrackCount > 1 && cand.NbTracks == 1 {
			score -= 40.0
		} else if localTrackCount > 1 && cand.NbTracks >= localTrackCount {
			score += 15.0
		} else if localTrackCount == 1 && cand.NbTracks == 1 {
			score += 10.0
		}

		if score > bestScore {
			bestScore = score
			best = cand
		}
	}

	if bestScore >= 40.0 {
		return best
	}
	return nil
}

// CheckIncompleteAlbums checks Deezer for missing tracks in incomplete albums.
func (s *Scanner) CheckIncompleteAlbums(ctx context.Context) {
	if s.deezerClient == nil {
		return
	}

	rows, err := s.database.Conn().QueryContext(ctx, `
		SELECT al.id, ar.name, al.title, COALESCE(al.total_tracks, 0), COALESCE(al.deezer_id, '')
		FROM albums al
		JOIN artists ar ON al.artist_id = ar.id
		WHERE (al.is_complete = 0 AND al.total_tracks > 1)
		   OR (SELECT COUNT(*) FROM missing_tracks WHERE album_id = al.id) > 0
		   OR al.total_tracks = 0
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	var albums []incompleteAlbum
	for rows.Next() {
		var a incompleteAlbum
		if err := rows.Scan(&a.ID, &a.Artist, &a.Title, &a.TotalTracks, &a.DeezerID); err == nil {
			albums = append(albums, a)
		}
	}

	for _, album := range albums {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Fetch local tracks first
		tRows, err := s.database.Conn().QueryContext(ctx, `
			SELECT track_number, title, duration FROM tracks WHERE album_id = ?
		`, album.ID)
		if err != nil {
			continue
		}

		var localTracks []localTrackItem
		for tRows.Next() {
			var lt localTrackItem
			if err := tRows.Scan(&lt.TrackNumber, &lt.Title, &lt.Duration); err == nil {
				localTracks = append(localTracks, lt)
			}
		}
		tRows.Close()

		if len(localTracks) == 0 {
			continue
		}

		cleanArtist := resolver.ExtractPrimaryArtist(album.Artist)
		if cleanArtist == "" {
			cleanArtist = album.Artist
		}

		var remoteAlbum *resolver.DeezerAlbum
		var fetchErr error

		if album.DeezerID != "" {
			remoteAlbum, fetchErr = s.deezerClient.GetAlbum(ctx, album.DeezerID)
		} else {
			query := fmt.Sprintf(`artist:"%s" album:"%s"`, cleanArtist, album.Title)
			searchResults, searchErr := s.deezerClient.SearchAlbums(ctx, query)
			if searchErr == nil && len(searchResults) > 0 {
				best := selectBestDeezerAlbumScanner(searchResults, cleanArtist, album.Title, len(localTracks))
				if best != nil {
					remoteAlbum, fetchErr = s.deezerClient.GetAlbum(ctx, strconv.FormatInt(best.ID, 10))
				}
			}
			if remoteAlbum == nil {
				simpleQuery := fmt.Sprintf("%s %s", cleanArtist, album.Title)
				searchResults, searchErr = s.deezerClient.SearchAlbums(ctx, simpleQuery)
				if searchErr == nil && len(searchResults) > 0 {
					best := selectBestDeezerAlbumScanner(searchResults, cleanArtist, album.Title, len(localTracks))
					if best != nil {
						remoteAlbum, fetchErr = s.deezerClient.GetAlbum(ctx, strconv.FormatInt(best.ID, 10))
					}
				}
			}
			if remoteAlbum == nil {
				searchResults, searchErr = s.deezerClient.SearchAlbums(ctx, album.Title)
				if searchErr == nil && len(searchResults) > 0 {
					best := selectBestDeezerAlbumScanner(searchResults, cleanArtist, album.Title, len(localTracks))
					if best != nil {
						remoteAlbum, fetchErr = s.deezerClient.GetAlbum(ctx, strconv.FormatInt(best.ID, 10))
					}
				}
			}
		}

		if fetchErr != nil || remoteAlbum == nil || remoteAlbum.Tracks == nil {
			continue
		}

		// Update album total tracks and Deezer ID
		if album.TotalTracks <= 0 && remoteAlbum.NbTracks > 0 {
			_, _ = s.database.Conn().ExecContext(ctx, `
				UPDATE albums SET total_tracks = ?, deezer_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?
			`, remoteAlbum.NbTracks, strconv.FormatInt(remoteAlbum.ID, 10), album.ID)
		}

		// 1-to-1 matching
		matchedLocal := make(map[int]bool) // localTracks index -> matched

		// Clear previous missing tracks
		_, _ = s.database.Conn().ExecContext(ctx, "DELETE FROM missing_tracks WHERE album_id = ?", album.ID)

		for i, rt := range remoteAlbum.Tracks.Data {
			pos := rt.TrackPosition
			if pos == 0 {
				pos = i + 1
			}
			dzTitleFull := strings.ToLower(resolver.CleanQueryTextFull(rt.Title))
			dzTitleNoParen := strings.ToLower(resolver.CleanQueryText(rt.Title))

			isMatch := false

			// Phase 1: Track position match + Title check
			for j, lt := range localTracks {
				if matchedLocal[j] {
					continue
				}
				if lt.TrackNumber > 0 && lt.TrackNumber == pos {
					locTitleFull := strings.ToLower(resolver.CleanQueryTextFull(lt.Title))
					locTitleNoParen := strings.ToLower(resolver.CleanQueryText(lt.Title))

					if locTitleFull == dzTitleFull || locTitleNoParen == dzTitleNoParen ||
						(len(locTitleFull) >= 3 && strings.Contains(dzTitleFull, locTitleFull)) ||
						(len(dzTitleFull) >= 3 && strings.Contains(locTitleFull, dzTitleFull)) ||
						(lt.Duration > 0 && rt.Duration > 0 && math.Abs(lt.Duration-float64(rt.Duration)) <= 10.0) {
						isMatch = true
						matchedLocal[j] = true
						break
					}
				}
			}

			// Phase 2: Title match
			if !isMatch {
				for j, lt := range localTracks {
					if matchedLocal[j] {
						continue
					}
					locTitleFull := strings.ToLower(resolver.CleanQueryTextFull(lt.Title))
					if locTitleFull != "" && locTitleFull == dzTitleFull {
						isMatch = true
						matchedLocal[j] = true
						break
					}
					locTitleNoParen := strings.ToLower(resolver.CleanQueryText(lt.Title))
					if locTitleNoParen != "" && locTitleNoParen == dzTitleNoParen && len(locTitleNoParen) >= 4 {
						if lt.Duration <= 0 || rt.Duration <= 0 || math.Abs(lt.Duration-float64(rt.Duration)) <= 15.0 {
							isMatch = true
							matchedLocal[j] = true
							break
						}
					}
				}
			}

			// Phase 3: Position + strict duration
			if !isMatch {
				for j, lt := range localTracks {
					if matchedLocal[j] {
						continue
					}
					if lt.TrackNumber > 0 && lt.TrackNumber == pos {
						if lt.Duration > 0 && rt.Duration > 0 && math.Abs(lt.Duration-float64(rt.Duration)) <= 5.0 {
							isMatch = true
							matchedLocal[j] = true
							break
						}
					}
				}
			}

			if !isMatch {
				artistName := rt.Artist.Name
				if artistName == "" {
					artistName = album.Artist
				}
				discNum := rt.DiskNumber
				if discNum <= 0 {
					discNum = 1
				}
				if _, err := s.database.Conn().ExecContext(ctx, `
					INSERT INTO missing_tracks (
						album_id, title, artist, track_number, disc_number,
						expected_duration, deezer_id, deezer_url, isrc, status, updated_at
					) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'missing', CURRENT_TIMESTAMP)
					ON CONFLICT(album_id, track_number, disc_number) DO NOTHING
				`, album.ID, rt.Title, artistName, pos, discNum, rt.Duration,
					strconv.FormatInt(rt.ID, 10), rt.Link, rt.ISRC); err != nil {
					log.Printf("[Scanner] failed to insert missing track %d for album %d: %v", pos, album.ID, err)
				}
			}
		}

		_, _ = s.database.Conn().ExecContext(ctx, `
			UPDATE albums SET
				is_complete = NOT EXISTS (SELECT 1 FROM missing_tracks WHERE album_id = albums.id)
					AND ? <= (SELECT COUNT(*) FROM tracks WHERE album_id = albums.id),
				expected_tracks_count = ?,
				present_tracks_count = (SELECT COUNT(*) FROM tracks WHERE album_id = albums.id),
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, remoteAlbum.NbTracks, remoteAlbum.NbTracks, album.ID)
	}
}
