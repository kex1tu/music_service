package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"log"

	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
)

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

	err := safefs.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
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

		resolvedPath, rErr := s.musicSandbox.Resolve(relPath, false)
		if rErr != nil {
			return nil
		}

		seenPaths[relPath] = true

		if err := s.indexFile(resolvedPath, relPath); err == nil {
			count++
		}
		return nil
	})

	if err == nil {
		// Clean up files that no longer exist on disk and remove orphaned records
		s.deleteMissingFiles(ctx, seenPaths)

		// Recompute statistics for all albums
		_, _ = s.database.Conn().ExecContext(ctx, `
			UPDATE albums SET
				present_tracks_count = (SELECT COUNT(*) FROM tracks WHERE album_id = albums.id),
				total_tracks = CASE
					WHEN total_tracks <= 0 THEN COALESCE((SELECT MAX(track_total) FROM tracks WHERE album_id = albums.id), 0)
					ELSE total_tracks
				END,
				updated_at = CURRENT_TIMESTAMP
		`)

		_, _ = s.database.Conn().ExecContext(ctx, `
			UPDATE albums SET
				is_complete = CASE
					WHEN (SELECT COUNT(*) FROM missing_tracks WHERE album_id = albums.id) > 0 THEN 0
					WHEN total_tracks > 1 AND present_tracks_count < total_tracks THEN 0
					WHEN (total_tracks <= 1 OR total_tracks IS NULL) AND present_tracks_count >= 1 THEN 1
					ELSE 1
				END,
				updated_at = CURRENT_TIMESTAMP
		`)

		// Check Deezer catalog for incomplete albums in background
		if s.deezerClient != nil {
			go s.CheckIncompleteAlbums(context.Background())
		}
	}

	return count, err
}

// RemoveFile removes a deleted file record from the database and cleans orphaned entities.
func (s *Scanner) RemoveFile(relPath string) error {
	var trackID, albumID int64
	err := s.database.Conn().QueryRow("SELECT id, album_id FROM tracks WHERE file_path = ?", relPath).Scan(&trackID, &albumID)
	if err == nil {
		if _, err := s.database.Conn().Exec("DELETE FROM tracks WHERE id = ?", trackID); err != nil {
			log.Printf("[Scanner] RemoveFile: failed to delete track %d: %v", trackID, err)
		}
		if _, err := s.database.Conn().Exec("DELETE FROM audit_issues WHERE (entity_type = 'track' AND entity_id = ?) OR file_path = ?", trackID, relPath); err != nil {
			log.Printf("[Scanner] RemoveFile: failed to clean audit_issues for track %d: %v", trackID, err)
		}
		if _, err := s.database.Conn().Exec("DELETE FROM track_genres WHERE track_id = ?", trackID); err != nil {
			log.Printf("[Scanner] RemoveFile: failed to clean track_genres for track %d: %v", trackID, err)
		}

		if albumID > 0 {
			var count int
			_ = s.database.Conn().QueryRow("SELECT COUNT(*) FROM tracks WHERE album_id = ?", albumID).Scan(&count)
			if count == 0 {
				if _, err := s.database.Conn().Exec("DELETE FROM albums WHERE id = ?", albumID); err != nil {
					log.Printf("[Scanner] RemoveFile: failed to delete album %d: %v", albumID, err)
				}
				if _, err := s.database.Conn().Exec("DELETE FROM missing_tracks WHERE album_id = ?", albumID); err != nil {
					log.Printf("[Scanner] RemoveFile: failed to clean missing_tracks for album %d: %v", albumID, err)
				}
			} else {
				if _, err := s.database.Conn().Exec(`
					UPDATE albums SET
						present_tracks_count = ?,
						updated_at = CURRENT_TIMESTAMP
					WHERE id = ?
				`, count, albumID); err != nil {
					log.Printf("[Scanner] RemoveFile: failed to update present_tracks_count for album %d: %v", albumID, err)
				}
			}
		}

		// Clean orphaned artists and missing tracks
		if _, err := s.database.Conn().Exec("DELETE FROM artists WHERE (SELECT COUNT(*) FROM albums WHERE artist_id = artists.id) = 0"); err != nil {
			log.Printf("[Scanner] RemoveFile: failed to clean orphaned artists: %v", err)
		}
		if _, err := s.database.Conn().Exec("DELETE FROM missing_tracks WHERE album_id NOT IN (SELECT id FROM albums)"); err != nil {
			log.Printf("[Scanner] RemoveFile: failed to clean orphaned missing_tracks: %v", err)
		}
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
	_ = safefs.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
					if fi, err := safefs.Stat(event.Name); err == nil && fi.IsDir() {
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
