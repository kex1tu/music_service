package scanner

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"music-manager/internal/core/sandbox"
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

// Scanner scans the music library filesystem and keeps SQLite database in sync.
type Scanner struct {
	musicSandbox *sandbox.PathSandbox
	database     *db.DB
	deezerClient *resolver.DeezerClient
	watcher      *fsnotify.Watcher
	isScanning   bool
	mu           sync.Mutex
}

// NewScanner creates a new library Scanner.
func NewScanner(musicSandbox *sandbox.PathSandbox, database *db.DB, deezerClient *resolver.DeezerClient) *Scanner {
	return &Scanner{
		musicSandbox: musicSandbox,
		database:     database,
		deezerClient: deezerClient,
	}
}

// IsScanning returns true if a scan is currently running.
func (s *Scanner) IsScanning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isScanning
}

// ScanAll scans all MP3 files inside MUSIC_DIR and updates SQLite.
func (s *Scanner) ScanAll(ctx context.Context) (int, error) {
	s.mu.Lock()
	if s.isScanning {
		s.mu.Unlock()
		return 0, fmt.Errorf("scan already in progress")
	}
	s.isScanning = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.isScanning = false
		s.mu.Unlock()
	}()

	root := s.musicSandbox.Root()
	count := 0
	seenPaths := make(map[string]bool)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // continue on permission or read errors
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "tmp" || name == "tmp_downloads" {
				return filepath.SkipDir
			}
			return nil
		}

		if !strings.EqualFold(filepath.Ext(path), ".mp3") {
			return nil
		}

		relPath, rErr := s.musicSandbox.ToRelative(path)
		if rErr != nil {
			return nil
		}

		seenPaths[relPath] = true

		if err := s.indexFile(path, relPath); err == nil {
			count++
		}
		return nil
	})

	if err == nil {
		// Clean up files that no longer exist on disk
		s.deleteMissingFiles(ctx, seenPaths)

		// Check Deezer catalog for incomplete albums in background
		if s.deezerClient != nil {
			go s.CheckIncompleteAlbums(context.Background())
		}
	}

	return count, err
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
	if albumTitle == "" {
		albumTitle = filepath.Base(filepath.Dir(fullPath))
		if albumTitle == "." || albumTitle == "/" {
			albumTitle = "Unknown Album"
		}
	}

	albumDir := filepath.Dir(relPath)
	if albumDir == "." {
		albumDir = ""
	}

	// 1. Get or create artist
	artistID, err := s.getOrCreateArtist(artistName)
	if err != nil {
		return err
	}

	// 2. Get or create album
	albumID, err := s.getOrCreateAlbum(albumTitle, artistID, tags.Year, albumDir)
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

	cleanTitle := strings.ToLower(tags.Title)
	query := `
	INSERT INTO tracks (
		album_id, artist_id, title, clean_title, track_number, track_total,
		disc_number, disc_total, duration, bitrate, file_path, file_size,
		mtime, has_embedded_cover, lyrics_status, is_path_valid, path_violation_reason, suggested_path
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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

	// 4. Genre handling & normalization
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

func (s *Scanner) deleteMissingFiles(ctx context.Context, seenPaths map[string]bool) {
	rows, err := s.database.Conn().QueryContext(ctx, "SELECT id, file_path FROM tracks")
	if err != nil {
		return
	}
	defer rows.Close()

	var toDelete []int64
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err == nil {
			if !seenPaths[path] {
				toDelete = append(toDelete, id)
			}
		}
	}

	for _, id := range toDelete {
		_, _ = s.database.Conn().ExecContext(ctx, "DELETE FROM tracks WHERE id = ?", id)
		_, _ = s.database.Conn().ExecContext(ctx, "DELETE FROM audit_issues WHERE entity_type = 'track' AND entity_id = ?", id)
	}

	if len(toDelete) > 0 {
		// Clean orphaned albums and artists
		_, _ = s.database.Conn().ExecContext(ctx, "DELETE FROM albums WHERE (SELECT COUNT(*) FROM tracks WHERE album_id = albums.id) = 0")
		_, _ = s.database.Conn().ExecContext(ctx, "DELETE FROM artists WHERE (SELECT COUNT(*) FROM albums WHERE artist_id = artists.id) = 0")
		_, _ = s.database.Conn().ExecContext(ctx, "DELETE FROM missing_tracks WHERE album_id NOT IN (SELECT id FROM albums)")
	}
}

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

		var remoteAlbum *resolver.DeezerAlbum
		var fetchErr error

		if album.DeezerID != "" {
			remoteAlbum, fetchErr = s.deezerClient.GetAlbum(ctx, album.DeezerID)
		} else {
			query := fmt.Sprintf(`artist:"%s" album:"%s"`, album.Artist, album.Title)
			searchResults, searchErr := s.deezerClient.SearchAlbums(ctx, query)
			if searchErr == nil && len(searchResults) > 0 {
				dzID := strconv.FormatInt(searchResults[0].ID, 10)
				remoteAlbum, fetchErr = s.deezerClient.GetAlbum(ctx, dzID)
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

		// Fetch local tracks
		tRows, err := s.database.Conn().QueryContext(ctx, `
			SELECT track_number, title FROM tracks WHERE album_id = ?
		`, album.ID)
		if err != nil {
			continue
		}

		var localTracks []localTrackItem
		for tRows.Next() {
			var lt localTrackItem
			if err := tRows.Scan(&lt.TrackNumber, &lt.Title); err == nil {
				localTracks = append(localTracks, lt)
			}
		}
		tRows.Close()

		presentByNum := make(map[int]bool)
		presentByTitle := make(map[string]bool)
		for _, lt := range localTracks {
			if lt.TrackNumber > 0 {
				presentByNum[lt.TrackNumber] = true
			}
			cl := cleanString(lt.Title)
			if cl != "" {
				presentByTitle[cl] = true
			}
		}

		// Clear previous missing tracks
		_, _ = s.database.Conn().ExecContext(ctx, "DELETE FROM missing_tracks WHERE album_id = ?", album.ID)

		missingCount := 0
		for _, rt := range remoteAlbum.Tracks.Data {
			pos := rt.TrackPosition
			cleanRt := cleanString(rt.Title)

			isMatch := false
			if pos > 0 && presentByNum[pos] {
				isMatch = true
			} else if presentByTitle[cleanRt] {
				isMatch = true
			}

			if !isMatch {
				missingCount++
				artistName := rt.Artist.Name
				if artistName == "" {
					artistName = album.Artist
				}
				_, _ = s.database.Conn().ExecContext(ctx, `
					INSERT INTO missing_tracks (
						album_id, title, artist, track_number, disc_number,
						expected_duration, deezer_id, deezer_url, isrc, status, updated_at
					) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'missing', CURRENT_TIMESTAMP)
					ON CONFLICT(album_id, track_number, disc_number) DO NOTHING
				`, album.ID, rt.Title, artistName, pos, rt.DiskNumber, rt.Duration,
					strconv.FormatInt(rt.ID, 10), rt.Link, rt.ISRC)
			}
		}

		isComplete := 1
		if missingCount > 0 || (remoteAlbum.NbTracks > 1 && len(localTracks) < remoteAlbum.NbTracks) {
			isComplete = 0
		}
		_, _ = s.database.Conn().ExecContext(ctx, `
			UPDATE albums SET is_complete = ?, expected_tracks_count = ?, present_tracks_count = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?
		`, isComplete, remoteAlbum.NbTracks, len(localTracks), album.ID)
	}
}

func (s *Scanner) getOrCreateArtist(name string) (int64, error) {
	var id int64
	err := s.database.Conn().QueryRow("SELECT id FROM artists WHERE name = ?", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	cleanName := strings.ToLower(name)
	res, err := s.database.Conn().Exec("INSERT INTO artists (name, clean_name) VALUES (?, ?)", name, cleanName)
	if err != nil {
		// Concurrent insert race check
		if err2 := s.database.Conn().QueryRow("SELECT id FROM artists WHERE name = ?", name).Scan(&id); err2 == nil {
			return id, nil
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Scanner) getOrCreateAlbum(title string, artistID int64, year int, albumDir string) (int64, error) {
	var id int64
	err := s.database.Conn().QueryRow("SELECT id FROM albums WHERE title = ? AND artist_id = ?", title, artistID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	cleanTitle := strings.ToLower(title)
	res, err := s.database.Conn().Exec(
		"INSERT INTO albums (title, clean_title, artist_id, year, folder_path) VALUES (?, ?, ?, ?, ?)",
		title, cleanTitle, artistID, year, albumDir,
	)
	if err != nil {
		if err2 := s.database.Conn().QueryRow("SELECT id FROM albums WHERE title = ? AND artist_id = ?", title, artistID).Scan(&id); err2 == nil {
			return id, nil
		}
		return 0, err
	}
	return res.LastInsertId()
}

// RemoveFile removes a deleted file record from the database and cleans orphaned entities.
func (s *Scanner) RemoveFile(relPath string) error {
	var trackID, albumID int64
	err := s.database.Conn().QueryRow("SELECT id, album_id FROM tracks WHERE file_path = ?", relPath).Scan(&trackID, &albumID)
	if err == nil {
		_, _ = s.database.Conn().Exec("DELETE FROM tracks WHERE id = ?", trackID)
		_, _ = s.database.Conn().Exec("DELETE FROM audit_issues WHERE (entity_type = 'track' AND entity_id = ?) OR file_path = ?", trackID, relPath)
		_, _ = s.database.Conn().Exec("DELETE FROM track_genres WHERE track_id = ?", trackID)

		if albumID > 0 {
			var count int
			_ = s.database.Conn().QueryRow("SELECT COUNT(*) FROM tracks WHERE album_id = ?", albumID).Scan(&count)
			if count == 0 {
				_, _ = s.database.Conn().Exec("DELETE FROM albums WHERE id = ?", albumID)
				_, _ = s.database.Conn().Exec("DELETE FROM missing_tracks WHERE album_id = ?", albumID)
			} else {
				_, _ = s.database.Conn().Exec(`
					UPDATE albums SET
						present_tracks_count = ?,
						updated_at = CURRENT_TIMESTAMP
					WHERE id = ?
				`, count, albumID)
			}
		}

		// Clean orphaned artists and missing tracks
		_, _ = s.database.Conn().Exec("DELETE FROM artists WHERE (SELECT COUNT(*) FROM albums WHERE artist_id = artists.id) = 0")
		_, _ = s.database.Conn().Exec("DELETE FROM missing_tracks WHERE album_id NOT IN (SELECT id FROM albums)")
	}
	return nil
}

// StartWatcher starts the background fsnotify filesystem watcher on MUSIC_DIR.
func (s *Scanner) StartWatcher(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}
	s.watcher = watcher

	root := s.musicSandbox.Root()

	// Add root and all subdirectories to watcher
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			name := d.Name()
			if !strings.HasPrefix(name, ".") && name != "tmp" && name != "tmp_downloads" {
				_ = watcher.Add(path)
			}
		}
		return nil
	})

	go func() {
		defer watcher.Close()
		debounceTimer := time.NewTimer(time.Hour)
		debounceTimer.Stop()
		pendingPaths := make(map[string]fsnotify.Op)
		var mu sync.Mutex

		for {
			select {
			case <-ctx.Done():
				return

			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				if strings.HasPrefix(filepath.Base(event.Name), ".") {
					continue
				}

				// If directory created, add to watcher
				if event.Op&fsnotify.Create != 0 {
					if fi, err := os.Stat(event.Name); err == nil && fi.IsDir() {
						_ = watcher.Add(event.Name)
					}
				}

				if strings.EqualFold(filepath.Ext(event.Name), ".mp3") {
					mu.Lock()
					pendingPaths[event.Name] = event.Op
					debounceTimer.Reset(500 * time.Millisecond)
					mu.Unlock()
				}

			case <-debounceTimer.C:
				mu.Lock()
				tasks := pendingPaths
				pendingPaths = make(map[string]fsnotify.Op)
				mu.Unlock()

				for path, op := range tasks {
					rel, err := s.musicSandbox.ToRelative(path)
					if err != nil {
						continue
					}

					if op&fsnotify.Remove != 0 || op&fsnotify.Rename != 0 {
						_ = s.RemoveFile(rel)
					} else if op&(fsnotify.Create|fsnotify.Write) != 0 {
						_ = s.indexFile(path, rel)
					}
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("[Scanner Watcher] Error: %v", err)
			}
		}
	}()

	return nil
}
