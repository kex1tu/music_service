package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
)

type libraryWatcher struct {
	scanner *Scanner
	watcher *fsnotify.Watcher
	dirs    map[string]struct{} // relative paths registered with fsnotify
}

// StartWatcher starts the background fsnotify filesystem watcher on MUSIC_DIR.
func (s *Scanner) StartWatcher(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}
	w := &libraryWatcher{scanner: s, watcher: watcher, dirs: make(map[string]struct{})}
	root, err := s.musicSandbox.Resolve(".", true, sandbox.LiteralPath)
	if err == nil {
		err = w.watchTree(ctx, root, false)
	}
	if err != nil {
		watcher.Close()
		return fmt.Errorf("watch music directory: %w", err)
	}
	go w.run(ctx)
	return nil
}

func (w *libraryWatcher) watchTree(ctx context.Context, root string, indexTracks bool) error {
	return safefs.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if path == root {
				return err
			}
			logWatcherError(path, err)
			return nil
		}
		rel, err := w.scanner.musicSandbox.ToRelative(path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if rel != "." && excludedLibraryDir(entry.Name()) {
				return filepath.SkipDir
			}
			if err := w.watcher.Add(path); err != nil {
				if path == root {
					return err
				}
				logWatcherError(path, err)
			} else {
				w.dirs[rel] = struct{}{}
			}
		} else if indexTracks && strings.EqualFold(filepath.Ext(path), ".mp3") {
			logWatcherError(rel, w.scanner.indexPath(ctx, rel))
		}
		return nil
	})
}

func (w *libraryWatcher) run(ctx context.Context) {
	defer w.watcher.Close()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	pending := make(map[string]bool) // paths to reconcile, and whether they were directories

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if w.queueEvent(pending, event) {
				timer.Reset(500 * time.Millisecond)
			}
		case <-timer.C:
			for rel, wasDir := range pending {
				if ctx.Err() != nil {
					return
				}
				logWatcherError(rel, w.reconcile(ctx, rel, wasDir))
			}
			clear(pending)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			logWatcherError("fsnotify", err)
		}
	}
}

func (w *libraryWatcher) queueEvent(pending map[string]bool, event fsnotify.Event) bool {
	if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename|fsnotify.Remove) == 0 {
		return false
	}
	rel, err := w.scanner.musicSandbox.ToRelative(event.Name)
	if err != nil || rel == "." {
		return false
	}
	for _, component := range strings.Split(rel, "/") {
		if excludedLibraryDir(component) {
			return false
		}
	}

	_, watched := w.dirs[rel]
	ext := strings.ToLower(filepath.Ext(rel))
	switch {
	case watched:
		if event.Op&(fsnotify.Rename|fsnotify.Remove) != 0 {
			// Descendant watches still refer to the old directory tree.
			w.forgetTree(rel)
		}
	case ext == ".mp3" || ext == ".lrc" || ext == ".txt":
	case event.Has(fsnotify.Create):
		full, err := w.scanner.resolveIndexedPath(rel)
		if err != nil {
			return false
		}
		if info, err := safefs.Lstat(full); err != nil || !info.IsDir() {
			return false
		}
	default:
		return false
	}
	pending[rel] = watched || pending[rel]
	return true
}

func (w *libraryWatcher) forgetTree(parent string) {
	for rel := range w.dirs {
		if rel != parent && !strings.HasPrefix(rel, parent+"/") {
			continue
		}
		path := filepath.Join(w.scanner.musicSandbox.Root(), filepath.FromSlash(rel))
		if err := w.watcher.Remove(path); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
			logWatcherError(path, err)
		}
		delete(w.dirs, rel)
	}
}

func (w *libraryWatcher) reconcile(ctx context.Context, rel string, wasDir bool) error {
	full, err := w.scanner.resolveIndexedPath(rel)
	if err != nil {
		return err
	}
	info, err := safefs.Lstat(full)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return err
	}

	// fsnotify may describe several operations on one path in a single batch.
	// Reconcile the index with what exists when the debounce expires.
	isDir := !missing && info.IsDir()
	if isDir {
		if err := w.watchTree(ctx, full, true); err != nil {
			return err
		}
	}
	if wasDir {
		if err := w.scanner.deleteMissingFiles(ctx, rel, nil, nil); err != nil {
			return err
		}
	}
	if isDir {
		return nil
	}

	switch strings.ToLower(filepath.Ext(rel)) {
	case ".mp3":
		if missing {
			return w.scanner.removeMissingPath(ctx, rel)
		}
		return w.scanner.indexPath(ctx, rel)
	case ".lrc", ".txt":
		return w.refreshLyrics(ctx, rel)
	default:
		return nil
	}
}

func (w *libraryWatcher) refreshLyrics(ctx context.Context, rel string) error {
	folder := filepath.Dir(rel)
	dir, err := w.scanner.musicSandbox.Resolve(folder, true, sandbox.LiteralPath)
	if err != nil {
		return err
	}
	entries, err := safefs.ReadDir(dir)
	if err != nil {
		return err
	}
	name := filepath.Base(rel)
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	for _, entry := range entries {
		ext := filepath.Ext(entry.Name())
		if !entry.IsDir() && strings.EqualFold(ext, ".mp3") && strings.TrimSuffix(entry.Name(), ext) == stem {
			track := filepath.ToSlash(filepath.Join(folder, entry.Name()))
			logWatcherError(track, w.scanner.indexPath(ctx, track))
		}
	}
	return nil
}

func logWatcherError(path string, err error) {
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errIndexPending) && !errors.Is(err, context.Canceled) {
		log.Printf("[Scanner Watcher] %q: %v", path, err)
	}
}
