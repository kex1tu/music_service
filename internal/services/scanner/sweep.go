package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"music-manager/internal/core/safefs"
	"music-manager/internal/db"
)

func (s *Scanner) deleteMissingFiles(ctx context.Context, subtree string, seen, unread map[string]bool) error {
	query := "SELECT id, file_path FROM tracks UNION ALL SELECT 0, file_path FROM library_artist_conflicts GROUP BY file_path"
	var args []any
	if subtree != "" {
		// Only descendants of the exact directory qualify, including names with SQL wildcards.
		query = "SELECT id, file_path FROM (" + query + ") WHERE file_path >= ? AND file_path < ?"
		args = []any{subtree + "/", subtree + "0"}
	}
	rows, err := s.database.Conn().QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	type candidate struct {
		id   int64
		path string
	}
	var missing []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.path); err != nil {
			return err
		}
		if !seen[c.path] && !underUnreadPath(c.path, unread) {
			missing = append(missing, c)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, c := range missing {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		if subtree == "" {
			err = s.sweepTrack(ctx, c.id, c.path)
		} else {
			err = s.removeMissingPath(ctx, c.path)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func underUnreadPath(path string, unread map[string]bool) bool {
	for {
		if unread[path] {
			return true
		}
		if path == "." || path == "/" {
			return false
		}
		path = filepath.Dir(path)
	}
}

func (s *Scanner) sweepTrack(ctx context.Context, id int64, relPath string) error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	if s.scanChanges[relPath] {
		return nil
	}
	missing, err := s.pathMissing(ctx, relPath)
	if err != nil || !missing {
		return err
	}
	if id == 0 {
		_, err := db.DeleteLibraryTracks(ctx, s.database.Conn(), relPath, false)
		return err
	}
	return db.DeleteIndexedTrack(ctx, s.database.Conn(), id, relPath)
}

func (s *Scanner) pathMissing(ctx context.Context, relPath string) (bool, error) {
	pending, err := s.hasPendingChange(ctx, relPath)
	if err != nil || pending {
		return false, err
	}
	full, err := s.resolveIndexedPath(relPath)
	if err != nil {
		return false, err
	}
	_, err = safefs.Lstat(full)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, err
}
