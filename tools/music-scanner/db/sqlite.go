package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

type DB struct {
	conn *sql.DB
	mu   sync.Mutex
}

type FileMtimeEntry struct {
	Mtime float64
	Size  int64
}

type TrackAuditRecord struct {
	RelPath              string  `json:"rel_path"`
	Title                string  `json:"title"`
	CleanTitle           string  `json:"clean_title,omitempty"`
	Artist               string  `json:"artist"`
	CleanArtist          string  `json:"clean_artist,omitempty"`
	Album                string  `json:"album"`
	CleanAlbum           string  `json:"clean_album,omitempty"`
	AlbumArtist          string  `json:"album_artist"`
	Year                 int     `json:"year"`
	TrackNumber          int     `json:"track_number"`
	TrackTotal           int     `json:"track_total"`
	DiscNumber           int     `json:"disc_number"`
	DiscTotal            int     `json:"disc_total"`
	Duration             float64 `json:"duration"`
	Bitrate              int     `json:"bitrate"`
	FileSize             int64   `json:"size"`
	Mtime                float64 `json:"mtime"`
	HasCover             bool    `json:"has_cover"`
	CoverPath            string  `json:"cover_path,omitempty"`
	LyricsStatus         string  `json:"lyrics_status"`
	LyricsPath           string  `json:"lyrics_path,omitempty"`
	IsPathValid          bool    `json:"is_path_valid"`
	PathViolationReason  string  `json:"path_violation_reason,omitempty"`
	SuggestedPath        string  `json:"suggested_path,omitempty"`
	Genre                string  `json:"genre"`
	GenreIsNonEn         bool    `json:"genre_is_non_en"`
	GenreCanonical       string  `json:"genre_canonical,omitempty"`
	GenreIsAutoFixed     bool    `json:"genre_is_auto_fixed"`
}

type MissingTrackRecord struct {
	AlbumID          int64
	Title            string
	Artist           string
	TrackNumber      int
	DiscNumber       int
	ExpectedDuration float64
	DeezerID         string
	DeezerURL        string
	SpotifyID        string
	GeniusURL        string
	ISRC             string
}

type IncompleteAlbumInfo struct {
	ID          int64
	Artist      string
	Title       string
	TotalTracks int
	DeezerID    string
}

// OpenDB открывает и настраивает SQLite базу данных.
func OpenDB(dbPath string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)", dbPath)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)

	if _, err := conn.Exec("PRAGMA journal_mode = WAL; PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;"); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to set pragmas: %w", err)
	}

	if _, err := conn.Exec(DDLSchema); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to apply ddl schema: %w", err)
	}

	return &DB{conn: conn}, nil
}

func (db *DB) Close() error {
	return db.conn.Close()
}

// GetExistingMtimes возвращает снимок существующих путей, mtime и размеров файлов для быстрого диффа.
func (db *DB) GetExistingMtimes() (map[string]FileMtimeEntry, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	rows, err := db.conn.Query("SELECT file_path, mtime, file_size FROM tracks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]FileMtimeEntry)
	for rows.Next() {
		var path string
		var mtime float64
		var size int64
		if err := rows.Scan(&path, &mtime, &size); err != nil {
			return nil, err
		}
		result[path] = FileMtimeEntry{Mtime: mtime, Size: size}
	}
	return result, rows.Err()
}
