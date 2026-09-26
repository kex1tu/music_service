-- DDL схема базы данных медиатеки Music Downloader & Tag Manager (SQLite WAL)

PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;

-- 1. Исполнители
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

-- 2. Альбомы / релизы
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

-- 3. Треки
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
    lyrics_status TEXT DEFAULT 'none',
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
CREATE INDEX IF NOT EXISTS idx_tracks_clean ON tracks(clean_title);
CREATE INDEX IF NOT EXISTS idx_tracks_path ON tracks(file_path);
CREATE INDEX IF NOT EXISTS idx_tracks_hash ON tracks(file_hash);
CREATE INDEX IF NOT EXISTS idx_tracks_path_valid ON tracks(is_path_valid);
CREATE INDEX IF NOT EXISTS idx_tracks_lyrics ON tracks(lyrics_status);

-- 4. Жанры
CREATE TABLE IF NOT EXISTS genres (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    is_valid_en BOOLEAN DEFAULT 1,
    canonical_en_name TEXT
);
CREATE INDEX IF NOT EXISTS idx_genres_valid ON genres(is_valid_en);

-- Связь многие-ко-многим: треки и жанры
CREATE TABLE IF NOT EXISTS track_genres (
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    genre_id INTEGER NOT NULL REFERENCES genres(id) ON DELETE CASCADE,
    PRIMARY KEY (track_id, genre_id)
);

-- 5. Недостающие треки альбомов
CREATE TABLE IF NOT EXISTS missing_tracks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    artist TEXT NOT NULL,
    track_number INTEGER NOT NULL,
    disc_number INTEGER DEFAULT 1,
    expected_duration INTEGER,
    deezer_id TEXT,
    deezer_url TEXT,
    spotify_id TEXT,
    genius_url TEXT,
    isrc TEXT,
    status TEXT DEFAULT 'missing',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(album_id, track_number, disc_number)
);

-- 6. Журнал проблем аудита
CREATE TABLE IF NOT EXISTS audit_issues (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_type TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id INTEGER,
    file_path TEXT,
    details TEXT,
    is_resolved BOOLEAN DEFAULT 0,
    is_auto_fixed BOOLEAN DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 7. Очередь задач загрузки
CREATE TABLE IF NOT EXISTS download_jobs (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL,
    source TEXT NOT NULL,
    url TEXT NOT NULL,
    progress REAL DEFAULT 0.0,
    current_step TEXT DEFAULT '',
    total_tracks INTEGER DEFAULT 0,
    completed_tracks INTEGER DEFAULT 0,
    error TEXT,
    target_album_path TEXT,
    is_backfill BOOLEAN DEFAULT 0,
    album_title TEXT,
    artist_name TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_download_jobs_status ON download_jobs(status);
