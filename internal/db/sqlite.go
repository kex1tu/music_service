package db

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

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

	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("сбой инициализации схемы SQLite: %w", err)
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
