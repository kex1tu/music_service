package scanner

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
)

func (s *Scanner) refreshAlbumFolderCover(ctx context.Context, tx *sql.Tx, albumID int64, root string) error {
	var saved string
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(cover_path, '') FROM albums WHERE id = ?", albumID).Scan(&saved); err != nil {
		return err
	}
	cover := ""
	for _, path := range []string{saved, filepath.Join(root, "cover.jpg"), filepath.Join(root, "cover.jpeg"),
		filepath.Join(root, "cover.png"), filepath.Join(root, "folder.jpg"), filepath.Join(root, "folder.png")} {
		if path == "" || filepath.Dir(path) != root {
			continue
		}
		full, err := s.resolveIndexedPath(path)
		if err != nil {
			continue
		}
		stat, err := safefs.Stat(full)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if stat.Mode().IsRegular() {
			cover = path
			break
		}
	}
	if cover == saved {
		return nil
	}
	mime := "image/jpeg"
	if strings.EqualFold(filepath.Ext(cover), ".png") {
		mime = "image/png"
	}
	_, err := tx.ExecContext(ctx, `UPDATE albums SET cover_path = NULLIF(?1, ''),
		cover_mime = CASE WHEN ?1 <> '' THEN ?2 END WHERE id = ?3`, cover, mime, albumID)
	return err
}
