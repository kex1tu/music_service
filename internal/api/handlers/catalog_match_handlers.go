package handlers

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"

	"path/filepath"
	"strconv"
	"strings"

	"music-manager/internal/core/safefs"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

func selectBestDeezerAlbum(candidates []resolver.DeezerAlbum, dbArtist, dbAlbum string, localTrackCount int) *resolver.DeezerAlbum {
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
			continue // Title must match or contain the query
		}

		// Artist similarity
		if normTargetArtist != "" {
			if candArtist == normTargetArtist || resolver.AreArtistsEquivalent(cand.Artist.Name, primaryArtist) {
				score += 30.0
			} else if strings.Contains(candArtist, normTargetArtist) || strings.Contains(normTargetArtist, candArtist) {
				score += 20.0
			} else {
				// Check transliteration
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
			// Severe penalty for 1-track single if local library has multiple tracks
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

func (h *FileHandler) CatalogMatch(w http.ResponseWriter, r *http.Request) {
	albumPath := r.URL.Query().Get("path")
	deezerID := r.URL.Query().Get("deezer_album_id")

	if albumPath == "" {
		http.Error(w, `{"error":"missing path query param"}`, http.StatusBadRequest)
		return
	}

	// 1. Query database for album metadata
	var albumID int64
	var dbArtist, dbAlbum, dbDeezerID string
	var dbYear int
	_ = h.database.Conn().QueryRowContext(r.Context(), `
		SELECT al.id, ar.name, al.title, COALESCE(al.year, 0), COALESCE(al.deezer_id, '')
		FROM albums al
		JOIN artists ar ON al.artist_id = ar.id
		WHERE al.folder_path = ? OR al.folder_path = ?
		LIMIT 1
	`, albumPath, filepath.Clean(albumPath)).Scan(&albumID, &dbArtist, &dbAlbum, &dbYear, &dbDeezerID)

	if deezerID == "" && dbDeezerID != "" {
		deezerID = dbDeezerID
	}

	// 2. Read local tracks in album directory
	type localTrack struct {
		TrackNumber int
		Title       string
		Path        string
		Duration    float64
	}
	var localTracks []localTrack
	if albumID > 0 {
		tRows, tErr := h.database.Conn().QueryContext(r.Context(), "SELECT track_number, title, file_path, duration FROM tracks WHERE album_id = ?", albumID)
		if tErr == nil {
			for tRows.Next() {
				var lt localTrack
				if err := tRows.Scan(&lt.TrackNumber, &lt.Title, &lt.Path, &lt.Duration); err == nil {
					localTracks = append(localTracks, lt)
				}
			}
			tRows.Close()
		}
	}

	if len(localTracks) == 0 {
		if fullPath, err := h.musicSandbox.Resolve(albumPath, false); err == nil {
			entries, _ := safefs.ReadDir(fullPath)
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp3") {
					itemRel := filepath.Join(albumPath, e.Name())
					itemFull := filepath.Join(fullPath, e.Name())
					if t, err := id3.ReadTags(itemFull, itemRel); err == nil {
						localTracks = append(localTracks, localTrack{
							TrackNumber: t.TrackNumber,
							Title:       t.Title,
							Path:        itemRel,
							Duration:    t.Duration,
						})
					}
				}
			}
		}
	}

	var matchedAlbum *resolver.DeezerAlbum
	var err error

	if deezerID != "" {
		matchedAlbum, err = h.deezer.GetAlbum(r.Context(), deezerID)
	}

	primaryArtist := resolver.ExtractPrimaryArtist(dbArtist)
	if primaryArtist == "" {
		primaryArtist = dbArtist
	}

	if matchedAlbum == nil {
		// Try searching by artist and album
		if primaryArtist != "" && dbAlbum != "" {
			q := fmt.Sprintf(`artist:"%s" album:"%s"`, primaryArtist, dbAlbum)
			albums, searchErr := h.deezer.SearchAlbums(r.Context(), q)
			if searchErr == nil && len(albums) > 0 {
				best := selectBestDeezerAlbum(albums, primaryArtist, dbAlbum, len(localTracks))
				if best != nil {
					matchedAlbum, err = h.deezer.GetAlbum(r.Context(), strconv.FormatInt(best.ID, 10))
				}
			}
		}
	}

	if matchedAlbum == nil && primaryArtist != "" && dbAlbum != "" {
		simpleQ := fmt.Sprintf("%s %s", primaryArtist, dbAlbum)
		albums, searchErr := h.deezer.SearchAlbums(r.Context(), simpleQ)
		if searchErr == nil && len(albums) > 0 {
			best := selectBestDeezerAlbum(albums, primaryArtist, dbAlbum, len(localTracks))
			if best != nil {
				matchedAlbum, err = h.deezer.GetAlbum(r.Context(), strconv.FormatInt(best.ID, 10))
			}
		}
	}

	if matchedAlbum == nil && dbAlbum != "" {
		albums, searchErr := h.deezer.SearchAlbums(r.Context(), dbAlbum)
		if searchErr == nil && len(albums) > 0 {
			best := selectBestDeezerAlbum(albums, primaryArtist, dbAlbum, len(localTracks))
			if best != nil {
				matchedAlbum, err = h.deezer.GetAlbum(r.Context(), strconv.FormatInt(best.ID, 10))
			}
		}
	}

	if matchedAlbum == nil {
		// Fallback: search by folder base name
		folderName := filepath.Base(albumPath)
		// If folder has format "Album - Artist", split it
		if parts := strings.Split(folderName, " - "); len(parts) >= 2 {
			fArtist := resolver.ExtractPrimaryArtist(strings.TrimSpace(parts[1]))
			fAlbum := strings.TrimSpace(parts[0])
			q := fmt.Sprintf(`artist:"%s" album:"%s"`, fArtist, fAlbum)
			albums, searchErr := h.deezer.SearchAlbums(r.Context(), q)
			if searchErr == nil && len(albums) > 0 {
				best := selectBestDeezerAlbum(albums, fArtist, fAlbum, len(localTracks))
				if best != nil {
					matchedAlbum, err = h.deezer.GetAlbum(r.Context(), strconv.FormatInt(best.ID, 10))
				}
			}
		}
		if matchedAlbum == nil {
			albums, searchErr := h.deezer.SearchAlbums(r.Context(), folderName)
			if searchErr == nil && len(albums) > 0 {
				best := selectBestDeezerAlbum(albums, primaryArtist, folderName, len(localTracks))
				if best != nil {
					matchedAlbum, err = h.deezer.GetAlbum(r.Context(), strconv.FormatInt(best.ID, 10))
				}
			}
		}
	}

	if err != nil || matchedAlbum == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"path":                  albumPath,
			"album":                 filepath.Base(albumPath),
			"present_count":         len(localTracks),
			"expected_count":        0,
			"missing_track_numbers": []int{},
			"missing_tracks":        []any{},
			"all_tracks":            []any{},
			"is_catalog_matched":    false,
		})
		return
	}

	cover := matchedAlbum.BestCoverURL()
	year := matchedAlbum.ReleaseYear()
	expectedCount := matchedAlbum.NbTracks

	// 1-to-1 track matching
	matchedLocal := make(map[int]bool) // localTracks index -> matched

	var allTracks []map[string]any
	var missingTracks []map[string]any
	var missingNums []int
	presentCount := 0

	// Determine canonical local artist & album
	canonicalArtist := dbArtist
	canonicalAlbum := dbAlbum

	if canonicalArtist == "" || canonicalAlbum == "" {
		for _, lt := range localTracks {
			if fullPath, err := h.musicSandbox.Resolve(lt.Path, false); err == nil {
				if t, err := id3.ReadTags(fullPath, lt.Path); err == nil {
					if canonicalArtist == "" {
						if t.AlbumArtist != "" {
							canonicalArtist = t.AlbumArtist
						} else if t.Artist != "" {
							canonicalArtist = t.Artist
						}
					}
					if canonicalAlbum == "" && t.Album != "" {
						canonicalAlbum = t.Album
					}
					if canonicalArtist != "" && canonicalAlbum != "" {
						break
					}
				}
			}
		}
	}

	finalArtist := matchedAlbum.Artist.Name
	if canonicalArtist != "" && (finalArtist == "" || resolver.AreArtistsEquivalent(finalArtist, canonicalArtist)) {
		finalArtist = canonicalArtist
	}

	finalAlbumTitle := matchedAlbum.Title
	if canonicalAlbum != "" && (finalAlbumTitle == "" || resolver.CleanQueryText(finalAlbumTitle) == resolver.CleanQueryText(canonicalAlbum)) {
		finalAlbumTitle = canonicalAlbum
	}

	if matchedAlbum.Tracks != nil {
		for i, t := range matchedAlbum.Tracks.Data {
			pos := t.TrackPosition
			if pos == 0 {
				pos = i + 1
			}
			dzTitleFull := strings.ToLower(resolver.CleanQueryTextFull(t.Title))
			dzTitleNoParen := strings.ToLower(resolver.CleanQueryText(t.Title))

			isPresent := false
			localPath := ""

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
						(lt.Duration > 0 && t.Duration > 0 && math.Abs(lt.Duration-float64(t.Duration)) <= 10.0) {
						isPresent = true
						localPath = lt.Path
						matchedLocal[j] = true
						break
					}
				}
			}

			// Phase 2: Exact/High similarity Title match (for unnumbered or displaced tracks)
			if !isPresent {
				for j, lt := range localTracks {
					if matchedLocal[j] {
						continue
					}
					locTitleFull := strings.ToLower(resolver.CleanQueryTextFull(lt.Title))
					if locTitleFull != "" && locTitleFull == dzTitleFull {
						isPresent = true
						localPath = lt.Path
						matchedLocal[j] = true
						break
					}
					locTitleNoParen := strings.ToLower(resolver.CleanQueryText(lt.Title))
					if locTitleNoParen != "" && locTitleNoParen == dzTitleNoParen && len(locTitleNoParen) >= 4 {
						if lt.Duration <= 0 || t.Duration <= 0 || math.Abs(lt.Duration-float64(t.Duration)) <= 15.0 {
							isPresent = true
							localPath = lt.Path
							matchedLocal[j] = true
							break
						}
					}
				}
			}

			// Phase 3: Position match with strict duration (±5s)
			if !isPresent {
				for j, lt := range localTracks {
					if matchedLocal[j] {
						continue
					}
					if lt.TrackNumber > 0 && lt.TrackNumber == pos {
						if lt.Duration > 0 && t.Duration > 0 && math.Abs(lt.Duration-float64(t.Duration)) <= 5.0 {
							isPresent = true
							localPath = lt.Path
							matchedLocal[j] = true
							break
						}
					}
				}
			}

			tArtist := t.Artist.Name
			if tArtist == "" || tArtist == matchedAlbum.Artist.Name || resolver.AreArtistsEquivalent(tArtist, finalArtist) {
				tArtist = finalArtist
			} else if matchedAlbum.Artist.Name != "" && strings.HasPrefix(strings.ToLower(tArtist), strings.ToLower(matchedAlbum.Artist.Name)) {
				tArtist = strings.Replace(tArtist, matchedAlbum.Artist.Name, finalArtist, 1)
			}

			discNum := t.DiskNumber
			if discNum <= 0 {
				discNum = 1
			}

			trackInfo := map[string]any{
				"track_number": pos,
				"disc_number":  discNum,
				"title":        id3.CleanTitle(t.Title, ""),
				"artist":       tArtist,
				"duration":     t.Duration,
				"deezer_id":    strconv.FormatInt(t.ID, 10),
				"deezer_url":   fmt.Sprintf("https://www.deezer.com/track/%d", t.ID),
				"is_present":   isPresent,
				"local_path":   localPath,
			}
			allTracks = append(allTracks, trackInfo)
			if isPresent {
				presentCount++
			} else {
				missingTracks = append(missingTracks, trackInfo)
				missingNums = append(missingNums, pos)
			}
		}
	}

	// 3. Update database with verification results
	isComplete := 1
	if len(missingTracks) > 0 || (expectedCount > 1 && presentCount < expectedCount) {
		isComplete = 0
	}

	if albumID > 0 {
		_, _ = h.database.Conn().ExecContext(r.Context(), `
			UPDATE albums SET
				expected_tracks_count = ?,
				total_tracks = ?,
				present_tracks_count = ?,
				deezer_id = ?,
				is_complete = ?,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, expectedCount, expectedCount, presentCount, strconv.FormatInt(matchedAlbum.ID, 10), isComplete, albumID)

		// Repopulate missing_tracks table
		_, _ = h.database.Conn().ExecContext(r.Context(), "DELETE FROM missing_tracks WHERE album_id = ?", albumID)
		for _, mt := range missingTracks {
			tNum, _ := mt["track_number"].(int)
			dNum, _ := mt["disc_number"].(int)
			if dNum <= 0 {
				dNum = 1
			}
			tit, _ := mt["title"].(string)
			art, _ := mt["artist"].(string)
			dur, _ := mt["duration"].(int)
			dzID, _ := mt["deezer_id"].(string)
			dzURL, _ := mt["deezer_url"].(string)

			_, _ = h.database.Conn().ExecContext(r.Context(), `
				INSERT INTO missing_tracks (
					album_id, title, artist, track_number, disc_number,
					expected_duration, deezer_id, deezer_url, status, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'missing', CURRENT_TIMESTAMP)
				ON CONFLICT(album_id, track_number, disc_number) DO UPDATE SET
					title = excluded.title,
					artist = excluded.artist,
					deezer_id = excluded.deezer_id,
					deezer_url = excluded.deezer_url,
					updated_at = CURRENT_TIMESTAMP
			`, albumID, tit, art, tNum, dNum, dur, dzID, dzURL)
		}
	}

	resp := map[string]any{
		"path":                  albumPath,
		"album":                 finalAlbumTitle,
		"artist":                finalArtist,
		"year":                  year,
		"has_cover":             cover != "",
		"cover_url":             cover,
		"present_count":         presentCount,
		"expected_count":        expectedCount,
		"missing_track_numbers": missingNums,
		"missing_tracks":        missingTracks,
		"all_tracks":            allTracks,
		"is_catalog_matched":    true,
		"deezer_album_id":       strconv.FormatInt(matchedAlbum.ID, 10),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
