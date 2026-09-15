"""Модуль работы с базой данных SQLite медиатеки (Library Database).

Обеспечивает:
1. Инициализацию схемы таблиц (DDL) в WAL-режиме (Write-Ahead Logging).
2. Асинхронное подключение через aiosqlite для безопасного параллельного чтения.
3. Настройку PRAGMA (WAL, foreign_keys, busy_timeout, synchronous=NORMAL).
"""

import logging
from collections.abc import AsyncGenerator
from contextlib import asynccontextmanager
from pathlib import Path

import aiosqlite

logger = logging.getLogger("music_manager.core.database")

DDL_SCHEMA = """
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;

-- 1. Таблица исполнителей с поддержкой внешних платформ
CREATE TABLE IF NOT EXISTS artists (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    clean_name TEXT NOT NULL,
    deezer_id TEXT,
    spotify_id TEXT,
    youtube_id TEXT,
    yandex_id TEXT,
    genius_id TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_artists_clean ON artists(clean_name);
CREATE INDEX IF NOT EXISTS idx_artists_deezer ON artists(deezer_id);
CREATE INDEX IF NOT EXISTS idx_artists_spotify ON artists(spotify_id);
CREATE INDEX IF NOT EXISTS idx_artists_yandex ON artists(yandex_id);

-- 2. Таблица альбомов / релизов
CREATE TABLE IF NOT EXISTS albums (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    clean_title TEXT NOT NULL,
    album_artist TEXT,
    year INTEGER,
    total_tracks INTEGER DEFAULT 0,
    total_discs INTEGER DEFAULT 1,
    cover_path TEXT,
    has_cover BOOLEAN DEFAULT 0,
    cover_mime TEXT,
    is_complete BOOLEAN DEFAULT 1,
    expected_tracks_count INTEGER DEFAULT 0,
    present_tracks_count INTEGER DEFAULT 0,
    folder_path TEXT NOT NULL,
    is_path_valid BOOLEAN DEFAULT 1,
    deezer_id TEXT,
    spotify_id TEXT,
    youtube_id TEXT,
    yandex_id TEXT,
    genius_id TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(artist_id, title)
);
CREATE INDEX IF NOT EXISTS idx_albums_clean ON albums(clean_title);
CREATE INDEX IF NOT EXISTS idx_albums_complete ON albums(is_complete);
CREATE INDEX IF NOT EXISTS idx_albums_folder ON albums(folder_path);

-- 3. Таблица треков медиатеки
CREATE TABLE IF NOT EXISTS tracks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    clean_title TEXT NOT NULL,
    track_number INTEGER DEFAULT 1,
    track_total INTEGER DEFAULT 1,
    disc_number INTEGER DEFAULT 1,
    disc_total INTEGER DEFAULT 1,
    duration REAL DEFAULT 0.0,
    bitrate INTEGER,
    file_path TEXT NOT NULL UNIQUE,
    file_size INTEGER NOT NULL,
    mtime REAL NOT NULL,
    file_hash TEXT,
    has_embedded_cover BOOLEAN DEFAULT 0,
    lyrics_status TEXT CHECK(lyrics_status IN ('none', 'lrc', 'txt')) DEFAULT 'none',
    lyrics_path TEXT,
    is_path_valid BOOLEAN DEFAULT 1,
    path_violation_reason TEXT,
    suggested_path TEXT,
    deezer_id TEXT,
    spotify_id TEXT,
    isrc TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_tracks_mtime_size ON tracks(file_path, mtime, file_size);
CREATE INDEX IF NOT EXISTS idx_tracks_clean ON tracks(clean_title, artist_id);
CREATE INDEX IF NOT EXISTS idx_tracks_lyrics ON tracks(lyrics_status);
CREATE INDEX IF NOT EXISTS idx_tracks_path_valid ON tracks(is_path_valid);

-- 4. Справочник жанров и нормализация
CREATE TABLE IF NOT EXISTS genres (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    is_valid_en BOOLEAN DEFAULT 1,
    canonical_en_name TEXT
);
CREATE INDEX IF NOT EXISTS idx_genres_valid ON genres(is_valid_en);

CREATE TABLE IF NOT EXISTS track_genres (
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    genre_id INTEGER NOT NULL REFERENCES genres(id) ON DELETE CASCADE,
    PRIMARY KEY(track_id, genre_id)
);

-- 5. Таблица недостающих треков (Missing Tracks)
CREATE TABLE IF NOT EXISTS missing_tracks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    artist TEXT NOT NULL,
    track_number INTEGER NOT NULL,
    disc_number INTEGER DEFAULT 1,
    expected_duration REAL,
    deezer_id TEXT,
    deezer_url TEXT,
    spotify_id TEXT,
    genius_url TEXT,
    isrc TEXT,
    status TEXT CHECK(status IN ('missing', 'queued', 'downloaded', 'ignored')) DEFAULT 'missing',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(album_id, disc_number, track_number)
);
CREATE INDEX IF NOT EXISTS idx_missing_tracks_status ON missing_tracks(album_id, status);

-- 6. Журнал проблем аудита и автоисправлений
CREATE TABLE IF NOT EXISTS audit_issues (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_type TEXT CHECK(entity_type IN ('track', 'album', 'genre', 'file')) NOT NULL,
    entity_id INTEGER NOT NULL,
    issue_type TEXT CHECK(issue_type IN (
        'invalid_path',
        'non_en_genre',
        'missing_lyrics',
        'missing_cover',
        'missing_track',
        'duplicate',
        'corrupt_file'
    )) NOT NULL,
    severity TEXT CHECK(severity IN ('error', 'warning', 'info')) DEFAULT 'warning',
    description TEXT NOT NULL,
    details JSON,
    suggested_action TEXT,
    is_auto_fixed BOOLEAN DEFAULT 0,
    is_resolved BOOLEAN DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_audit_issues_unresolved ON audit_issues(is_resolved, issue_type);
"""


async def init_db(db_path: Path | str) -> None:
    """Инициализирует базу данных SQLite: создает директорию и применяет DDL."""
    path = Path(db_path).expanduser().resolve()
    path.parent.mkdir(parents=True, exist_ok=True)

    async with aiosqlite.connect(str(path)) as db:
        db.text_factory = lambda b: b.decode("utf-8", errors="replace")
        db.row_factory = aiosqlite.Row
        await db.executescript(DDL_SCHEMA)
        await db.commit()
    logger.info("SQLite база данных успешно инициализирована: %s", path)


@asynccontextmanager
async def get_db_connection(db_path: Path | str) -> AsyncGenerator[aiosqlite.Connection, None]:
    """Асинхронный контекстный менеджер для работы с SQLite."""
    path = Path(db_path).expanduser().resolve()
    async with aiosqlite.connect(str(path)) as conn:
        conn.text_factory = lambda b: b.decode("utf-8", errors="replace")
        conn.row_factory = aiosqlite.Row
        await conn.execute("PRAGMA foreign_keys = ON;")
        await conn.execute("PRAGMA busy_timeout = 5000;")
        yield conn
