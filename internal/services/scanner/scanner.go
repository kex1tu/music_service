package scanner

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/id3"
)

// Scanner scans the music library filesystem and keeps SQLite database in sync.
type Scanner struct {
	musicSandbox *sandbox.PathSandbox
	database     *db.DB
	watcher      *fsnotify.Watcher
	isScanning   bool
	mu           sync.Mutex
}

// NewScanner creates a new library Scanner.
func NewScanner(musicSandbox *sandbox.PathSandbox, database *db.DB) *Scanner {
	return &Scanner{
		musicSandbox: musicSandbox,
		database:     database,
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

		if err := s.indexFile(path, relPath); err == nil {
			count++
		}
		return nil
	})

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

	cleanTitle := strings.ToLower(tags.Title)
	query := `
	INSERT INTO tracks (
		album_id, artist_id, title, clean_title, track_number, track_total,
		disc_number, disc_total, duration, bitrate, file_path, file_size,
		mtime, has_embedded_cover, lyrics_status
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		lyrics_status = excluded.lyrics_status;
	`

	_, err = s.database.Conn().Exec(
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
	)

	return err
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

// RemoveFile removes a deleted file record from the database.
func (s *Scanner) RemoveFile(relPath string) error {
	_, err := s.database.Conn().Exec("DELETE FROM tracks WHERE file_path = ?", relPath)
	return err
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
