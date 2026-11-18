package radio

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"

	"music-manager/internal/core/safefs"

	_ "modernc.org/sqlite"
)

func (s *NavidromeStats) IsStarred(relPath string) bool {
	return s != nil && s.StarredPaths[path.Clean(relPath)]
}

func (s *NavidromeStats) GetPlayCount(relPath string) int {
	if s == nil {
		return 0
	}
	return s.PlayCounts[path.Clean(relPath)]
}

func (s *NavidromeStats) GetLastPlayed(relPath string) time.Time {
	if s == nil {
		return time.Time{}
	}
	return s.LastPlayed[path.Clean(relPath)]
}

// NavidromeReader reads listening annotations from a read-only Navidrome database.
// musicRoot is the path to Music Manager's library as seen by Navidrome.
type NavidromeReader struct {
	dbPath    string
	musicRoot string
}

func NewNavidromeReader(dbPath, musicRoot string) *NavidromeReader {
	return &NavidromeReader{dbPath: strings.TrimSpace(dbPath), musicRoot: navidromePath(musicRoot)}
}

func (r *NavidromeReader) IsAvailable() bool {
	if r == nil || r.dbPath == "" {
		return false
	}
	info, err := safefs.Stat(r.dbPath)
	return err == nil && info.Mode().IsRegular()
}

func navidromePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if hasDriveRoot(p) {
		return p[:2] + path.Clean(p[2:])
	}
	return path.Clean(p)
}

func hasDriveRoot(p string) bool {
	return len(p) >= 3 && p[1:3] == ":/"
}

func isAbsoluteNavidromePath(p string) bool {
	return path.IsAbs(p) || hasDriveRoot(p)
}

// libraryRelative converts absolute Navidrome paths and paths relative to a
// Navidrome library to keys relative to the common root.
func (r *NavidromeReader) libraryRelative(filePath, libraryRoot string) string {
	filePath = navidromePath(filePath)
	if !isAbsoluteNavidromePath(filePath) {
		if filePath == "." || filePath == ".." || strings.HasPrefix(filePath, "../") {
			return ""
		}
		filePath = navidromePath(strings.TrimSuffix(libraryRoot, "/") + "/" + filePath)
	}
	prefix := strings.TrimSuffix(r.musicRoot, "/") + "/"
	if !strings.HasPrefix(filePath, prefix) {
		return ""
	}
	return strings.TrimPrefix(filePath, prefix)
}

// GetUserListeningStats aggregates all listeners: any star, the sum of play
// counts and the latest play date, keyed by the complete relative file path.
func (r *NavidromeReader) GetUserListeningStats(ctx context.Context) (*NavidromeStats, error) {
	if !r.IsAvailable() {
		return nil, fmt.Errorf("navidrome database unavailable")
	}
	if !isAbsoluteNavidromePath(r.musicRoot) {
		return nil, fmt.Errorf("NAVIDROME_MUSIC_ROOT must be absolute")
	}
	dbPath, err := filepath.Abs(r.dbPath)
	if err != nil {
		return nil, err
	}
	dsn := url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
	conn, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	var hasLibraries bool
	err = conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pragma_table_info('media_file') WHERE name='library_id')").Scan(&hasLibraries)
	if err != nil {
		return nil, err
	}
	rootColumn, libraryJoin := "''", ""
	if hasLibraries {
		rootColumn, libraryJoin = "l.path", "LEFT JOIN library l ON l.id = mf.library_id"
	}
	rows, err := conn.QueryContext(ctx, `
		SELECT mf.path, `+rootColumn+`, COALESCE(a.starred, 0), COALESCE(a.play_count, 0), a.play_date
		FROM annotation a
		JOIN media_file mf ON mf.id = a.item_id
		`+libraryJoin+`
		WHERE a.item_type = 'media_file' AND (a.starred = 1 OR a.play_count > 0)`)
	if err != nil {
		return nil, fmt.Errorf("navidrome annotation query: %w", err)
	}
	defer rows.Close()

	stats := &NavidromeStats{
		StarredPaths: make(map[string]bool),
		PlayCounts:   make(map[string]int),
		LastPlayed:   make(map[string]time.Time),
	}
	for rows.Next() {
		var filePath string
		var libraryRoot sql.NullString
		var starred bool
		var plays int
		var last sql.NullTime
		if err := rows.Scan(&filePath, &libraryRoot, &starred, &plays, &last); err != nil {
			return nil, fmt.Errorf("navidrome annotation row: %w", err)
		}
		root := r.musicRoot
		if hasLibraries {
			root = navidromePath(libraryRoot.String)
			if !isAbsoluteNavidromePath(root) {
				return nil, fmt.Errorf("navidrome media file has no absolute library root")
			}
		}
		rel := r.libraryRelative(filePath, root)
		if rel == "" {
			continue
		}
		if starred {
			stats.StarredPaths[rel] = true
		}
		if plays > 0 {
			stats.PlayCounts[rel] += plays
			if last.Valid && last.Time.After(stats.LastPlayed[rel]) {
				stats.LastPlayed[rel] = last.Time
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}

func (r *NavidromeReader) loadStats(ctx context.Context) (*NavidromeStats, error) {
	if r == nil || r.dbPath == "" {
		return nil, nil
	}
	stats, err := r.GetUserListeningStats(ctx)
	if err != nil && ctx.Err() == nil {
		log.Printf("[Radio] Rotation without Navidrome listening statistics: %v", err)
	}
	return stats, err
}

// ListeningStatus reports whether the configured database can provide stats.
func (r *NavidromeReader) ListeningStatus(ctx context.Context) (available bool, warning string) {
	stats, err := r.loadStats(ctx)
	if err != nil {
		return false, "База Navidrome недоступна или несовместима. Радио работает без внешней статистики."
	}
	return stats != nil, ""
}
