package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
	isScanning   bool
	mu           sync.Mutex

	indexMu     sync.Mutex
	scanChanges map[string]bool // paths indexed successfully since the active walk started
	tmpSandbox  *sandbox.PathSandbox
}

// NewScanner creates a new library Scanner.
func NewScanner(musicSandbox *sandbox.PathSandbox, database *db.DB, deezerClient *resolver.DeezerClient, staging ...*sandbox.PathSandbox) *Scanner {
	s := &Scanner{
		musicSandbox: musicSandbox,
		database:     database,
		deezerClient: deezerClient,
	}
	if len(staging) > 0 {
		s.tmpSandbox = staging[0]
	}
	return s
}

// IsScanning returns true if a scan is currently running.
func (s *Scanner) IsScanning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isScanning
}

// ScanAll scans all MP3 files inside MUSIC_DIR and updates SQLite.
func (s *Scanner) ScanAll(ctx context.Context) (int, error) {
	return s.scanAll(ctx, safefs.WalkDir)
}

func excludedLibraryDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "tmp" || name == "tmp_downloads"
}

func (s *Scanner) scanAll(ctx context.Context, walk func(string, fs.WalkDirFunc) error) (int, error) {
	s.mu.Lock()
	if s.isScanning {
		s.mu.Unlock()
		return 0, fmt.Errorf("scan already in progress")
	}
	s.isScanning = true
	s.mu.Unlock()

	s.indexMu.Lock()
	s.scanChanges = make(map[string]bool)
	s.indexMu.Unlock()
	defer func() {
		s.indexMu.Lock()
		s.scanChanges = nil
		s.indexMu.Unlock()
		s.mu.Lock()
		s.isScanning = false
		s.mu.Unlock()
	}()

	root := s.musicSandbox.Root()
	count := 0
	seenPaths := make(map[string]bool)
	unread := make(map[string]bool)
	var firstFailure error
	err := walk(root, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		relPath, err := s.musicSandbox.ToRelative(path)
		if err != nil {
			return err
		}
		if walkErr != nil {
			unread[relPath] = true
			if firstFailure == nil {
				firstFailure = walkErr
			}
			return nil
		}
		if path == root {
			return nil
		}
		if d.IsDir() {
			if excludedLibraryDir(d.Name()) {
				unread[relPath] = true
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".mp3") {
			return nil
		}
		seenPaths[relPath] = true
		if err := s.indexPath(ctx, relPath); err == nil {
			count++
		} else if !errors.Is(err, db.ErrAmbiguousArtist) && !errors.Is(err, errIndexPending) && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, sandbox.ErrPathTraversal) && firstFailure == nil {
			firstFailure = fmt.Errorf("index %q: %w", relPath, err)
		}
		return nil
	})
	if err != nil {
		return count, err
	}

	if err := s.deleteMissingFiles(ctx, "", seenPaths, unread); err != nil {
		return count, errors.Join(firstFailure, err)
	}
	if firstFailure == nil && s.deezerClient != nil {
		go s.CheckIncompleteAlbums(context.Background())
	}
	return count, firstFailure
}

// RemoveFile removes a confirmed deletion from the index.
func (s *Scanner) RemoveFile(relPath string) error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	_, err := db.DeleteLibraryTracks(context.Background(), s.database.Conn(), relPath, false)
	return err
}

func (s *Scanner) removeMissingPath(ctx context.Context, relPath string) error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	missing, err := s.pathMissing(ctx, relPath)
	if err != nil || !missing {
		return err
	}
	_, err = db.DeleteLibraryTracks(ctx, s.database.Conn(), relPath, false)
	return err
}
