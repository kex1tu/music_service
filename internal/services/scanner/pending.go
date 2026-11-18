package scanner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
)

var errIndexPending = errors.New("library index change is pending")

type pendingChange struct {
	source     string
	target     string
	staging    bool
	merged     bool
	deleteKind int
}

func (s *Scanner) resolveIndexedPath(path string) (string, error) {
	return resolveStoredPath(s.musicSandbox, path)
}

// ChangeTrack records the actual destination before applying a file change.
// Source and target are resolved absolute paths supplied by the file operation.
// For a tags-only change, pass the same path as source and target.
func (s *Scanner) ChangeTrack(ctx context.Context, source, target string, merged bool, change func() error) error {
	if !strings.EqualFold(filepath.Ext(target), ".mp3") || !strings.EqualFold(filepath.Ext(source), ".mp3") {
		return change()
	}
	s.indexMu.Lock()
	defer s.indexMu.Unlock()

	p := pendingChange{merged: merged}
	var err error
	p.target, err = s.musicSandbox.ToRelative(target)
	if err != nil {
		return err
	}
	if _, err := s.resolveIndexedPath(p.target); err != nil {
		return err
	}
	if source != target && s.tmpSandbox != nil && strings.HasPrefix(source, s.tmpSandbox.Root()+string(filepath.Separator)) {
		p.source, err = s.tmpSandbox.ToRelative(source)
		p.staging = true
	} else {
		p.source, err = s.musicSandbox.ToRelative(source)
	}
	if err != nil {
		return err
	}
	if _, err := s.sourcePath(p); err != nil {
		return err
	}

	for _, path := range []string{p.source, p.target} {
		pending, err := s.hasPendingChange(ctx, path)
		if err != nil {
			return err
		}
		if pending {
			return fmt.Errorf("%w: %s", errIndexPending, path)
		}
	}
	_, err = s.database.Conn().ExecContext(ctx, `
		INSERT INTO library_index_pending (target_path, source_path, source_is_staging, merge_into_target)
		VALUES (?, ?, ?, ?)
	`, p.target, p.source, p.staging, p.merged)
	if err != nil {
		return fmt.Errorf("record index change for %q: %w", p.target, err)
	}

	fileErr := change()
	// The filesystem may already have changed when an HTTP client disconnects.
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	indexErr := s.reconcileChange(reconcileCtx, p)
	if indexErr != nil {
		indexErr = fmt.Errorf("index reconciliation queued for %q: %w", p.target, indexErr)
	}
	return errors.Join(fileErr, indexErr)
}

func (s *Scanner) hasPendingChange(ctx context.Context, path string) (bool, error) {
	var pending bool
	err := s.database.Conn().QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM library_index_pending WHERE target_path = ?
			UNION ALL
			SELECT 1 FROM library_index_pending WHERE source_is_staging = 0 AND source_path = ?
			UNION ALL
			SELECT 1 FROM library_index_pending WHERE delete_kind = 2
				AND substr(?, 1, length(target_path) + 1) = target_path || '/'
		)
	`, path, path, path).Scan(&pending)
	return pending, err
}

func (s *Scanner) sourcePath(p pendingChange) (string, error) {
	if p.staging {
		if s.tmpSandbox == nil {
			return "", errors.New("staging sandbox unavailable")
		}
		return resolveStoredPath(s.tmpSandbox, p.source)
	}
	return s.resolveIndexedPath(p.source)
}

func resolveStoredPath(sb *sandbox.PathSandbox, path string) (string, error) {
	full, err := sb.Resolve(path, false, sandbox.LiteralPath)
	if err != nil {
		return "", err
	}
	if relative, err := sb.ToRelative(full); err != nil || relative != path {
		return "", fmt.Errorf("stored path does not round trip: %q", path)
	}
	return full, nil
}

func (s *Scanner) reconcileChange(ctx context.Context, p pendingChange) error {
	source, err := s.sourcePath(p)
	if err != nil {
		return err
	}
	target, err := s.resolveIndexedPath(p.target)
	if err != nil {
		return err
	}
	_, sourceErr := safefs.Lstat(source)
	if sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist) {
		return sourceErr
	}
	_, targetErr := safefs.Lstat(target)
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		return targetErr
	}

	if source == target && targetErr == nil {
		return s.indexFileLocked(ctx, p.target, "", p.target, false)
	}
	if sourceErr != nil && targetErr == nil {
		old := p.source
		if p.staging {
			old = ""
		}
		return s.indexFileLocked(ctx, p.target, old, p.target, p.merged)
	}
	if sourceErr == nil && !p.staging {
		// If both paths exist, they are separate files. The source keeps its ID.
		if source != target && targetErr == nil {
			if err := s.indexFileLocked(ctx, p.target, "", "", false); err != nil {
				return err
			}
		}
		return s.indexFileLocked(ctx, p.source, "", p.target, false)
	}
	if sourceErr == nil && targetErr == nil {
		return s.indexFileLocked(ctx, p.target, "", p.target, false)
	}
	if !p.staging {
		_, err := s.reconcileDeletion(ctx, p.source, false, p.target)
		return err
	}
	_, err = s.database.Conn().ExecContext(ctx, "DELETE FROM library_index_pending WHERE target_path = ?", p.target)
	return err
}

// RunIndexRecovery retries recorded changes independently of the filesystem watcher.
func (s *Scanner) RunIndexRecovery(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.RetryPending(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[Scanner] pending index changes: %v", err)
			}
		}
	}
}

// RetryPending reconciles filesystem intents left by an interrupted request or process.
func (s *Scanner) RetryPending(ctx context.Context) error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()

	rows, err := s.database.Conn().QueryContext(ctx, `
		SELECT source_path, target_path, source_is_staging, merge_into_target, delete_kind FROM library_index_pending ORDER BY rowid
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var changes []pendingChange
	for rows.Next() {
		var p pendingChange
		if err := rows.Scan(&p.source, &p.target, &p.staging, &p.merged, &p.deleteKind); err != nil {
			return err
		}
		changes = append(changes, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	var failures []error
	for _, p := range changes {
		var err error
		if p.deleteKind != 0 {
			_, err = s.reconcileDeletion(ctx, p.target, p.deleteKind == 2, p.target)
		} else {
			err = s.reconcileChange(ctx, p)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%q: %w", p.target, err))
		}
	}
	return errors.Join(failures...)
}

// DeletePath journals a requested file or directory deletion before applying it.
func (s *Scanner) DeletePath(ctx context.Context, fullPath string, directory bool, remove func() error) (int64, error) {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()

	path, err := s.musicSandbox.ToRelative(fullPath)
	if err != nil {
		return 0, err
	}
	if _, err := s.resolveIndexedPath(path); err != nil {
		return 0, err
	}
	var pending bool
	err = s.database.Conn().QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM library_index_pending
			WHERE target_path = ? OR (source_is_staging = 0 AND source_path = ?)
				OR (? AND target_path >= ? AND target_path < ?)
				OR (? AND source_is_staging = 0 AND source_path >= ? AND source_path < ?)
		)
	`, path, path, directory, path+"/", path+"0", directory, path+"/", path+"0").Scan(&pending)
	if err != nil {
		return 0, err
	}
	if pending {
		return 0, fmt.Errorf("%w: %s", errIndexPending, path)
	}
	kind := 1
	if directory {
		kind = 2
	}
	if _, err := s.database.Conn().ExecContext(ctx, `
		INSERT INTO library_index_pending (target_path, source_path, source_is_staging, merge_into_target, delete_kind)
		VALUES (?, '', 0, 0, ?)
	`, path, kind); err != nil {
		return 0, fmt.Errorf("record deletion for %q: %w", path, err)
	}

	fileErr := remove()
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	count, err := s.reconcileDeletion(reconcileCtx, path, directory, path)
	if err != nil {
		err = fmt.Errorf("index reconciliation queued for deletion of %q: %w", path, err)
	}
	return count, errors.Join(fileErr, err)
}

func (s *Scanner) reconcileDeletion(ctx context.Context, path string, directory bool, intentPath string) (int64, error) {
	full, err := s.resolveIndexedPath(path)
	if err != nil {
		return 0, err
	}
	_, statErr := safefs.Lstat(full)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return 0, statErr
	}
	tx, err := s.database.Conn().BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var count int64
	if statErr != nil {
		count, err = db.DeleteLibraryPathTx(ctx, tx, path, directory)
		if err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM library_index_pending WHERE target_path = ?", intentPath); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}
