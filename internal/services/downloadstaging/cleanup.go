// Package downloadstaging manages retention of temporary downloads shared by both downloaders.
package downloadstaging

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
)

// HasPendingImport reports whether staged audio or lyrics still need to be imported.
// The raw directory only contains inputs for conversion.
func HasPendingImport(sb *sandbox.PathSandbox, id string) (bool, error) {
	if sb == nil {
		return false, errors.New("staging sandbox not configured")
	}
	dir, err := sb.Resolve(id, false)
	if err != nil {
		return false, err
	}
	var pending bool
	err = safefs.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if path == dir && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() && path == filepath.Join(dir, "raw") {
			return fs.SkipDir
		}
		switch strings.ToLower(filepath.Ext(entry.Name())) {
		case ".mp3", ".lrc", ".txt":
			pending = true
			return fs.SkipAll
		}
		return nil
	})
	return pending, err
}

// CleanOldDirs reclaims old staging directories eligible under the job retention policy.
// Files awaiting import are retained even when the job record is unavailable.
func CleanOldDirs(sb *sandbox.PathSandbox, maxAge time.Duration, canReclaim func(string) bool) {
	if sb == nil {
		return
	}
	root, err := sb.Resolve(".", true)
	if err != nil {
		return
	}
	dirs, err := safefs.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range dirs {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		dir, err := sb.Resolve(id, false)
		if err != nil || dir != filepath.Join(root, id) {
			continue
		}
		info, err := safefs.Lstat(dir)
		if err != nil || !info.IsDir() || !info.ModTime().Before(cutoff) || !canReclaim(id) {
			continue
		}
		if pending, err := HasPendingImport(sb, id); err == nil && !pending {
			_ = safefs.RemoveAll(dir)
		}
	}
}

// TrackMissing reports a converted path that no longer exists in staging.
func TrackMissing(sb *sandbox.PathSandbox, path string) bool {
	if sb == nil || path == "" {
		return false
	}
	path = strings.TrimPrefix(strings.TrimPrefix(path, "tmp_downloads/"), "tmp/")
	full, err := sb.Resolve(path, false)
	if err != nil {
		return false
	}
	_, err = safefs.Lstat(full)
	return errors.Is(err, fs.ErrNotExist)
}
