package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var ddlSchema string

// DB инкапсулирует подключение к SQLite в WAL-режиме с защитой от блокировок.
type DB struct {
	conn *sql.DB
	mu   sync.RWMutex
}

// Open открывает базу данных SQLite, настраивает пул соединений и PRAGMA, а также накатывает схему.
func Open(dbPath string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть базу данных SQLite (%s): %w", dbPath, err)
	}

	// Настройка пула соединений: 1 writer, множество readers для WAL-режима
	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(time.Hour)

	db := &DB{conn: conn}

	if err := db.initSchema(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("сбой инициализации схемы SQLite: %w", err)
	}

	if err := db.migrateReleaseModel(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("миграция модели релизов: %w", err)
	}
	if err := db.initLibrarySearchKeys(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ключи Unicode-поиска: %w", err)
	}
	if _, err := conn.Exec(`INSERT INTO tracked_artists (name, clean_name, is_followed)
		SELECT MIN(ar.name), music_search_key(ar.name), 1 FROM artists ar
		WHERE NOT EXISTS (SELECT 1 FROM tracked_artists ta WHERE ta.clean_name = music_search_key(ar.name))
		GROUP BY music_search_key(ar.name) ON CONFLICT(name) DO NOTHING`); err != nil {
		conn.Close()
		return nil, fmt.Errorf("регистрация артистов медиатеки: %w", err)
	}
	return db, nil
}

// Close закрывает пул соединений базы данных.
func (db *DB) Close() error {
	return db.conn.Close()
}

// Conn возвращает сырой дескриптор соединения sql.DB.
func (db *DB) Conn() *sql.DB {
	return db.conn
}

// initSchema применяет DDL схему таблиц при запуске и накатывает инкрементальные миграции.
func (db *DB) initSchema() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := db.conn.ExecContext(ctx, ddlSchema); err != nil {
		return err
	}

	// Инкрементальные миграции для существующих баз данных
	_, _ = db.conn.ExecContext(ctx, "ALTER TABLE tracks ADD COLUMN pinned_source_url TEXT;")
	_, _ = db.conn.ExecContext(ctx, "ALTER TABLE missing_tracks ADD COLUMN pinned_source_url TEXT;")
	_, _ = db.conn.ExecContext(ctx, "ALTER TABLE missing_tracks ADD COLUMN updated_at DATETIME DEFAULT CURRENT_TIMESTAMP;")
	_, _ = db.conn.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_radar_releases_artist_status ON radar_releases(clean_artist_name, status);")
	_, _ = db.conn.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_radar_releases_clean_title ON radar_releases(clean_title);")

	return nil
}
