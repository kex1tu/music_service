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
    pinned_source_url TEXT,
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
    pinned_source_url TEXT,
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

-- 8. Кэш доцензурных версий SoundCloud
CREATE TABLE IF NOT EXISTS soundcloud_uncensored_cache (
    cache_key TEXT PRIMARY KEY,
    track_title TEXT NOT NULL,
    artist_name TEXT NOT NULL,
    selected_url TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,              -- marked, likely, unverified, manual, link_dead
    duration INTEGER NOT NULL,
    is_manual BOOLEAN DEFAULT 0,
    candidates_json TEXT NOT NULL DEFAULT '[]',
    checked_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_sc_cache_key ON soundcloud_uncensored_cache(cache_key);

-- 9. Системные настройки и OAuth токены
CREATE TABLE IF NOT EXISTS app_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT '',
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 10. Постоянные ручные привязки источников треков (Track Source Pins)
CREATE TABLE IF NOT EXISTS pinned_track_sources (
    cache_key TEXT PRIMARY KEY,
    artist_name TEXT NOT NULL,
    track_title TEXT NOT NULL,
    isrc TEXT,
    pinned_url TEXT NOT NULL,
    source_type TEXT NOT NULL, -- youtube, ytmusic, soundcloud, bandcamp, direct
    duration INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_pinned_sources_artist_title ON pinned_track_sources(artist_name, track_title);

-- 11. Персистентные настройки отслеживания артистов (не сбрасываются при рестарте)
CREATE TABLE IF NOT EXISTS tracked_artists (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    clean_name TEXT NOT NULL,
    is_followed BOOLEAN DEFAULT 1,
    auto_download BOOLEAN DEFAULT 0,
    custom_search_alias TEXT,
    deezer_id TEXT,
    musicbrainz_id TEXT,
    yandex_id TEXT,
    last_checked_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_tracked_artists_clean ON tracked_artists(clean_name);
CREATE INDEX IF NOT EXISTS idx_tracked_artists_followed ON tracked_artists(is_followed);

-- 12. Релизы Радара и дискографий (новинки и пробелы)
CREATE TABLE IF NOT EXISTS radar_releases (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    artist_name TEXT NOT NULL,
    clean_artist_name TEXT NOT NULL,
    title TEXT NOT NULL,
    clean_title TEXT NOT NULL,
    release_type TEXT NOT NULL, -- 'album', 'single', 'ep', 'compilation'
    release_date TEXT,          -- 'YYYY-MM-DD' or 'YYYY'
    year INTEGER,
    cover_url TEXT,
    track_count INTEGER DEFAULT 1,
    deezer_id TEXT,
    musicbrainz_id TEXT,
    shazam_id TEXT,
    source_url TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'new', -- 'new', 'in_library', 'version_exists', 'downloaded', 'ignored'
    is_radar BOOLEAN DEFAULT 0,         -- 1 если дата релиза за последние 2 года
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(clean_artist_name, clean_title, release_type)
);
CREATE INDEX IF NOT EXISTS idx_radar_releases_artist ON radar_releases(clean_artist_name);
CREATE INDEX IF NOT EXISTS idx_radar_releases_status ON radar_releases(status);
CREATE INDEX IF NOT EXISTS idx_radar_releases_is_radar ON radar_releases(is_radar);
CREATE INDEX IF NOT EXISTS idx_radar_releases_artist_status ON radar_releases(clean_artist_name, status);
CREATE INDEX IF NOT EXISTS idx_radar_releases_clean_title ON radar_releases(clean_title);

-- 13. Граф связей и похожих артистов (для Моей Волны и рекомендаций)
CREATE TABLE IF NOT EXISTS artist_relations (
    artist_name TEXT NOT NULL,
    related_name TEXT NOT NULL,
    score REAL NOT NULL, -- 0.0 to 1.0
    source TEXT NOT NULL, -- 'deezer', 'lastfm', 'manual'
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (artist_name, related_name)
);
CREATE INDEX IF NOT EXISTS idx_artist_rel_source ON artist_relations(artist_name);

-- 14. История воспроизведения радиостанций (анти-повторы)
CREATE TABLE IF NOT EXISTS radio_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    track_id INTEGER REFERENCES tracks(id) ON DELETE CASCADE,
    station_type TEXT NOT NULL, -- 'wave', 'genre', 'artist'
    station_param TEXT,         -- жанр или имя артиста
    played_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_radio_hist_track_time ON radio_history(track_id, played_at);

