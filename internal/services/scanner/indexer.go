package scanner

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"music-manager/internal/db"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
)

var cleanMetaRegex = regexp.MustCompile(`[\p{P}\s_]+`)

func cleanString(s string) string {
	s = strings.ToLower(s)
	return cleanMetaRegex.ReplaceAllString(s, "")
}

// IndexFile indexes or re-indexes a single MP3 file in the SQLite database.
func (s *Scanner) IndexFile(fullPath, relPath string) error {
	return s.indexFile(fullPath, relPath)
}

func (s *Scanner) indexFile(fullPath, relPath string) error {
	tags, err := id3.ReadTags(fullPath, relPath)
	if err != nil {
		return err
	}

	st, err := os.Stat(fullPath)
	if err != nil {
		return err
	}

	artistName := tags.Artist
	if artistName == "" {
		artistName = "Unknown Artist"
	}
	albumArtist := tags.AlbumArtist
	if albumArtist == "" {
		albumArtist = artistName
	}
	albumTitle := tags.Album
	if albumTitle == "" || strings.EqualFold(albumTitle, "single") || strings.EqualFold(albumTitle, "singles") {
		if strings.TrimSpace(tags.Title) != "" {
			albumTitle = strings.TrimSpace(tags.Title)
		} else {
			albumTitle = filepath.Base(filepath.Dir(fullPath))
			if albumTitle == "." || albumTitle == "/" {
				albumTitle = "Unknown Album"
			}
		}
	}

	albumDir := filepath.Dir(relPath)
	if albumDir == "." {
		albumDir = ""
	}

	// 1. Get or create primary artist for the track (avoiding composite 'Artist 1\\Artist 2' in artists table)
	primaryTrackArtist := resolver.ExtractPrimaryArtist(artistName)
	if primaryTrackArtist == "" {
		primaryTrackArtist = "Unknown Artist"
	}
	artistID, err := s.getOrCreateArtist(primaryTrackArtist)
	if err != nil {
		return err
	}

	// 2. Get or create primary artist for the album and resolve album ID
	effectiveAlbumArtist := resolver.ExtractPrimaryArtist(albumArtist)
	if effectiveAlbumArtist == "" {
		effectiveAlbumArtist = resolver.ExtractPrimaryArtist(artistName)
	}
	if effectiveAlbumArtist == "" {
		effectiveAlbumArtist = "Unknown Artist"
	}
	albumArtistID, err := s.getOrCreateArtist(effectiveAlbumArtist)
	if err != nil {
		return err
	}

	albumID, err := s.getOrCreateAlbum(albumTitle, albumArtistID, effectiveAlbumArtist, tags.Year, albumDir, tags.HasCover)
	if err != nil {
		return err
	}


	// 3. Compute canonical path and validate path conformity
	effAlbum := strings.TrimSpace(tags.Album)
	if effAlbum == "" || strings.EqualFold(effAlbum, "single") || strings.EqualFold(effAlbum, "singles") {
		if strings.TrimSpace(tags.Title) != "" {
			effAlbum = strings.TrimSpace(tags.Title)
		} else {
			effAlbum = "Unknown Album"
		}
	}
	effArtist := strings.TrimSpace(tags.AlbumArtist)
	if effArtist == "" {
		effArtist = strings.TrimSpace(tags.Artist)
	}
	if effArtist == "" {
		effArtist = "Unknown Artist"
	}
	_, suggestedPath := organizer.ComputeTargetPath(effArtist, effAlbum, tags.TrackNumber, tags.Title, filepath.Base(fullPath))

	isPathValid := true
	pathViolationReason := ""
	cleanRel := filepath.Clean(filepath.ToSlash(relPath))
	cleanSug := filepath.Clean(filepath.ToSlash(suggestedPath))
	if cleanRel != cleanSug {
		isPathValid = false
		pathViolationReason = fmt.Sprintf("Путь не соответствует каноническому шаблону (текущий: '%s', ожидается: '%s')", cleanRel, cleanSug)
	}

	cleanTitle := id3.CleanReleaseTitle(tags.Title)
	if cleanTitle == "" {
		cleanTitle = strings.ToLower(tags.Title)
	}
	fileHash := db.TrackHash(relPath)
	query := `
	INSERT INTO tracks (
		album_id, artist_id, title, clean_title, track_number, track_total,
		disc_number, disc_total, duration, bitrate, file_path, file_size,
		mtime, file_hash, has_embedded_cover, lyrics_status, is_path_valid, path_violation_reason, suggested_path
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(file_path) DO UPDATE SET
		album_id = excluded.album_id,
		artist_id = excluded.artist_id,
		title = excluded.title,
		clean_title = excluded.clean_title,
		track_number = excluded.track_number,
		track_total = excluded.track_total,
		disc_number = excluded.disc_number,
		disc_total = excluded.disc_total,
		duration = excluded.duration,
		bitrate = excluded.bitrate,
		file_size = excluded.file_size,
		mtime = excluded.mtime,
		file_hash = excluded.file_hash,
		has_embedded_cover = excluded.has_embedded_cover,
		lyrics_status = excluded.lyrics_status,
		is_path_valid = excluded.is_path_valid,
		path_violation_reason = excluded.path_violation_reason,
		suggested_path = excluded.suggested_path
	RETURNING id;
	`

	var trackID int64
	err = s.database.Conn().QueryRow(
		query,
		albumID,
		artistID,
		tags.Title,
		cleanTitle,
		tags.TrackNumber,
		tags.TrackTotal,
		tags.DiscNumber,
		tags.DiscTotal,
		tags.Duration,
		tags.Bitrate,
		relPath,
		st.Size(),
		float64(st.ModTime().Unix()),
		fileHash,
		tags.HasCover,
		tags.LyricsStatus,
		isPathValid,
		pathViolationReason,
		suggestedPath,
	).Scan(&trackID)
	if err != nil {
		return err
	}

	// 4. Clear old issues for this track and re-populate
	_, _ = s.database.Conn().Exec("DELETE FROM audit_issues WHERE entity_id = ? AND entity_type = 'track'", trackID)
	_, _ = s.database.Conn().Exec("DELETE FROM track_genres WHERE track_id = ?", trackID)

	if !isPathValid {
		_, _ = s.database.Conn().Exec(`
			INSERT INTO audit_issues (issue_type, entity_type, entity_id, file_path, details)
			VALUES ('invalid_path', 'track', ?, ?, ?)
		`, trackID, relPath, pathViolationReason)
	}

	// Genre handling & normalization
	genreStr := strings.TrimSpace(tags.Genre)
	if genreStr != "" {
		genres := id3.ParseGenres(genreStr)
		if len(genres) == 0 {
			genres = []string{genreStr}
		}

		hasAnyCyrillic := false
		hasAnyNonCanonical := false
		isAllAutoFixable := true

		for _, g := range genres {
			genreRes := id3.AnalyzeAndNormalizeGenre(g)
			if genreRes.HasCyrillic {
				hasAnyCyrillic = true
			}
			if genreRes.IsNonCanonical {
				hasAnyNonCanonical = true
			}
			if !genreRes.IsAutoFixable && genreRes.IsNonCanonical {
				isAllAutoFixable = false
			}

			rawName := g
			canonName := genreRes.CanonicalGenre
			if canonName == "" {
				canonName = g
			}
			isValidEn := !genreRes.HasCyrillic && !genreRes.IsNonCanonical

			var genreID int64
			err := s.database.Conn().QueryRow("SELECT id FROM genres WHERE name = ?", rawName).Scan(&genreID)
			if err == sql.ErrNoRows {
				res, insErr := s.database.Conn().Exec(
					"INSERT INTO genres (name, is_valid_en, canonical_en_name) VALUES (?, ?, ?)",
					rawName, isValidEn, canonName,
				)
				if insErr == nil {
					genreID, _ = res.LastInsertId()
				}
			} else if err == nil {
				_, _ = s.database.Conn().Exec(
					"UPDATE genres SET is_valid_en = ?, canonical_en_name = ? WHERE id = ?",
					isValidEn, canonName, genreID,
				)
			}

			if genreID > 0 {
				_, _ = s.database.Conn().Exec("INSERT OR IGNORE INTO track_genres (track_id, genre_id) VALUES (?, ?)", trackID, genreID)
			}
		}

		if hasAnyCyrillic || hasAnyNonCanonical {
			fullRes := id3.AnalyzeAndNormalizeGenre(genreStr)
			isAutoFixed := 0
			if isAllAutoFixable && fullRes.IsAutoFixable {
				isAutoFixed = 1
			}
			_, _ = s.database.Conn().Exec(`
				INSERT INTO audit_issues (issue_type, entity_type, entity_id, file_path, details, is_auto_fixed, is_resolved)
				VALUES ('non_en_genre', 'track', ?, ?, ?, ?, ?)
			`, trackID, relPath, "Русскоязычный / неканонический жанр: "+genreStr+" -> "+fullRes.CanonicalGenre, isAutoFixed, isAutoFixed)
		}
	}

	// 5. Audit issues for cover & lyrics
	if !tags.HasCover {
		_, _ = s.database.Conn().Exec(`
			INSERT INTO audit_issues (issue_type, entity_type, entity_id, file_path, details)
			VALUES ('missing_cover', 'track', ?, ?, 'Отсутствует графическая обложка')
		`, trackID, relPath)
	}
	if tags.LyricsStatus == "none" {
		_, _ = s.database.Conn().Exec(`
			INSERT INTO audit_issues (issue_type, entity_type, entity_id, file_path, details)
			VALUES ('missing_lyrics', 'track', ?, ?, 'Отсутствует текст песни (.lrc/.txt)')
		`, trackID, relPath)
	}

	// 6. Update album statistics
	_, _ = s.database.Conn().Exec(`
		UPDATE albums SET
			present_tracks_count = (SELECT COUNT(*) FROM tracks WHERE album_id = ?),
			total_tracks = CASE
				WHEN total_tracks <= 0 THEN COALESCE((SELECT MAX(track_total) FROM tracks WHERE album_id = ?), 0)
				ELSE total_tracks
			END,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, albumID, albumID, albumID)

	_, _ = s.database.Conn().Exec(`
		UPDATE albums SET
			is_complete = CASE
				WHEN (SELECT COUNT(*) FROM missing_tracks WHERE album_id = ?) > 0 THEN 0
				WHEN total_tracks > 1 AND present_tracks_count < total_tracks THEN 0
				WHEN (total_tracks <= 1 OR total_tracks IS NULL) AND present_tracks_count >= 1 THEN 1
				ELSE 1
			END,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, albumID, albumID)

	return nil
}
