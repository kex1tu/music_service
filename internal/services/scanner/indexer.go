package scanner

import (
	"context"
	"fmt"

	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
	"music-manager/internal/db"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
)

// IndexFile indexes or re-indexes a single MP3 file in the SQLite database.
func (s *Scanner) IndexFile(_ string, relPath string) error {
	return s.indexPath(context.Background(), relPath)
}

func (s *Scanner) indexPath(ctx context.Context, relPath string) error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	pending, err := s.hasPendingChange(ctx, relPath)
	if err != nil {
		return err
	}
	if pending {
		return errIndexPending
	}
	return s.indexFileLocked(ctx, relPath, "", "", false)
}

// indexFileLocked reads the file before opening the SQLite write transaction.
func (s *Scanner) indexFileLocked(ctx context.Context, relPath, oldPath, pendingPath string, merged bool) error {
	fullPath, err := s.resolveIndexedPath(relPath)
	if err != nil {
		return err
	}
	tags, err := id3.ReadTags(fullPath, relPath)
	if err != nil {
		return err
	}

	st, err := safefs.Stat(fullPath)
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
	tags.DiscNumber = db.LocalDiscNumber(filepath.ToSlash(albumDir), tags.DiscNumber)

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
	tx, err := s.database.Conn().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	previousAlbum, previousArtist, err := db.PrepareLibraryTrackUpdate(ctx, tx, oldPath, relPath, merged)
	if err != nil {
		return err
	}

	// 1. Get or create primary artist for the track (avoiding composite 'Artist 1\\Artist 2' in artists table)
	primaryTrackArtist := resolver.ExtractPrimaryArtist(artistName)
	if primaryTrackArtist == "" {
		primaryTrackArtist = "Unknown Artist"
	}
	artistID, err := getOrCreateArtist(ctx, tx, primaryTrackArtist)
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
	albumArtistID, err := getOrCreateArtist(ctx, tx, effectiveAlbumArtist)
	if err != nil {
		return err
	}

	displayAlbumArtist := strings.TrimSpace(albumArtist)
	if displayAlbumArtist == "" {
		displayAlbumArtist = effectiveAlbumArtist
	}
	albumID, err := getOrCreateAlbum(ctx, tx, albumTitle, albumArtistID, displayAlbumArtist, tags.Year, albumDir, tags.HasCover)
	if err != nil {
		return err
	}

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
		suggested_path = excluded.suggested_path,
        updated_at = CURRENT_TIMESTAMP
	RETURNING id;
	`

	var trackID int64
	err = tx.QueryRowContext(ctx,
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
	if _, err := tx.ExecContext(ctx, "DELETE FROM audit_issues WHERE entity_id = ? AND entity_type = 'track'", trackID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM track_genres WHERE track_id = ?", trackID); err != nil {
		return err
	}

	if !isPathValid {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO audit_issues (issue_type, entity_type, entity_id, file_path, details)
			VALUES ('invalid_path', 'track', ?, ?, ?)
		`, trackID, relPath, pathViolationReason); err != nil {
			return err
		}
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
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO genres (name, is_valid_en, canonical_en_name) VALUES (?, ?, ?)
				ON CONFLICT(name) DO UPDATE SET is_valid_en = excluded.is_valid_en, canonical_en_name = excluded.canonical_en_name
				RETURNING id
			`, rawName, isValidEn, canonName).Scan(&genreID); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `
				INSERT INTO track_genres (track_id, genre_id) VALUES (?, ?)
				ON CONFLICT(track_id, genre_id) DO NOTHING
			`, trackID, genreID)
			if err != nil {
				return err
			}
		}

		if hasAnyCyrillic || hasAnyNonCanonical {
			fullRes := id3.AnalyzeAndNormalizeGenre(genreStr)
			isAutoFixed := 0
			if isAllAutoFixable && fullRes.IsAutoFixable {
				isAutoFixed = 1
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO audit_issues (issue_type, entity_type, entity_id, file_path, details, is_auto_fixed, is_resolved)
				VALUES ('non_en_genre', 'track', ?, ?, ?, ?, ?)
			`, trackID, relPath, "Русскоязычный / неканонический жанр: "+genreStr+" -> "+fullRes.CanonicalGenre, isAutoFixed, isAutoFixed); err != nil {
				return err
			}
		}
	}

	// 5. Audit issues for cover & lyrics
	if !tags.HasCover {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO audit_issues (issue_type, entity_type, entity_id, file_path, details)
			VALUES ('missing_cover', 'track', ?, ?, 'Отсутствует графическая обложка')
		`, trackID, relPath); err != nil {
			return err
		}
	}
	if tags.LyricsStatus == "none" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO audit_issues (issue_type, entity_type, entity_id, file_path, details)
			VALUES ('missing_lyrics', 'track', ?, ?, 'Отсутствует текст песни (.lrc/.txt)')
		`, trackID, relPath); err != nil {
			return err
		}
	}

	if err := db.RefreshLibraryAlbum(ctx, tx, albumID); err != nil {
		return err
	}
	if previousAlbum != 0 && previousAlbum != albumID {
		if err := db.RefreshLibraryAlbum(ctx, tx, previousAlbum); err != nil {
			return err
		}
	}
	if previousArtist != 0 && previousArtist != artistID {
		if err := db.PruneLibraryArtist(ctx, tx, previousArtist); err != nil {
			return err
		}
	}
	if pendingPath != "" {
		if _, err := tx.ExecContext(ctx, "DELETE FROM library_index_pending WHERE target_path = ?", pendingPath); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if s.scanChanges != nil {
		s.scanChanges[relPath] = true
	}
	return nil
}
