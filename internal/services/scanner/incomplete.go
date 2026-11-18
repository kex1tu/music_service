package scanner

import (
	"context"
	"fmt"
	"log"
	"music-manager/internal/db"
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
		   OR EXISTS (SELECT 1 FROM missing_tracks WHERE album_id = al.id)
		   OR al.total_tracks = 0
		   OR EXISTS (
			   SELECT 1 FROM tracks t WHERE t.album_id = al.id
			   GROUP BY t.album_id HAVING MAX(t.disc_total) > COUNT(DISTINCT t.disc_number)
		   )
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
	if err := rows.Err(); err != nil {
		log.Printf("[Scanner] failed to read incomplete albums: %v", err)
		return
	}
	rows.Close()

	for _, album := range albums {
		select {
		case <-ctx.Done():
			return
		default:
		}

		var localCount int
		if err := s.database.Conn().QueryRowContext(ctx, "SELECT COUNT(*) FROM tracks WHERE album_id = ?", album.ID).Scan(&localCount); err != nil || localCount == 0 {
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
				best := selectBestDeezerAlbumScanner(searchResults, cleanArtist, album.Title, localCount)
				if best != nil {
					remoteAlbum, fetchErr = s.deezerClient.GetAlbum(ctx, strconv.FormatInt(best.ID, 10))
				}
			}
			if remoteAlbum == nil {
				simpleQuery := fmt.Sprintf("%s %s", cleanArtist, album.Title)
				searchResults, searchErr = s.deezerClient.SearchAlbums(ctx, simpleQuery)
				if searchErr == nil && len(searchResults) > 0 {
					best := selectBestDeezerAlbumScanner(searchResults, cleanArtist, album.Title, localCount)
					if best != nil {
						remoteAlbum, fetchErr = s.deezerClient.GetAlbum(ctx, strconv.FormatInt(best.ID, 10))
					}
				}
			}
			if remoteAlbum == nil {
				searchResults, searchErr = s.deezerClient.SearchAlbums(ctx, album.Title)
				if searchErr == nil && len(searchResults) > 0 {
					best := selectBestDeezerAlbumScanner(searchResults, cleanArtist, album.Title, localCount)
					if best != nil {
						remoteAlbum, fetchErr = s.deezerClient.GetAlbum(ctx, strconv.FormatInt(best.ID, 10))
					}
				}
			}
		}

		if fetchErr != nil || remoteAlbum == nil || remoteAlbum.Tracks == nil {
			continue
		}

		catalog, err := resolver.DeezerCatalogTracks(remoteAlbum)
		if err == nil {
			_, err = db.VerifyCatalog(ctx, s.database.Conn(), album.ID, strconv.FormatInt(remoteAlbum.ID, 10), catalog)
		}
		if err != nil {
			log.Printf("[Scanner] failed to verify catalog for album %d: %v", album.ID, err)
		}
	}
}
