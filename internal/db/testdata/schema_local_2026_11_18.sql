-- Schema only, from the local SQLite database audited on 2026-11-18.

CREATE TABLE artists (
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

CREATE INDEX idx_artists_clean ON artists(clean_name);

CREATE INDEX idx_artists_deezer ON artists(deezer_id);

CREATE INDEX idx_artists_spotify ON artists(spotify_id);

CREATE INDEX idx_artists_yandex ON artists(yandex_id);

CREATE TABLE albums (
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
    yandex_id TEXT,
    genius_id TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(artist_id, title)
);

CREATE INDEX idx_albums_clean ON albums(clean_title);

CREATE INDEX idx_albums_complete ON albums(is_complete);

CREATE INDEX idx_albums_folder ON albums(folder_path);

CREATE TABLE tracks (
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
, pinned_source_url TEXT);

CREATE INDEX idx_tracks_mtime_size ON tracks(file_path, mtime, file_size);

CREATE INDEX idx_tracks_clean ON tracks(clean_title, artist_id);

CREATE INDEX idx_tracks_lyrics ON tracks(lyrics_status);

CREATE INDEX idx_tracks_path_valid ON tracks(is_path_valid);

CREATE TABLE genres (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    is_valid_en BOOLEAN DEFAULT 1,
    canonical_en_name TEXT
);

CREATE INDEX idx_genres_valid ON genres(is_valid_en);

CREATE TABLE track_genres (
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    genre_id INTEGER NOT NULL REFERENCES genres(id) ON DELETE CASCADE,
    PRIMARY KEY(track_id, genre_id)
);

CREATE TABLE missing_tracks (
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
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, pinned_source_url TEXT,
    UNIQUE(album_id, disc_number, track_number)
);

CREATE INDEX idx_missing_tracks_status ON missing_tracks(album_id, status);

CREATE TABLE audit_issues (
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

CREATE INDEX idx_audit_issues_unresolved ON audit_issues(is_resolved, issue_type);

CREATE INDEX idx_tracks_path ON tracks(file_path);

CREATE TABLE download_jobs (
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

CREATE INDEX idx_download_jobs_status ON download_jobs(status);

CREATE INDEX idx_tracks_hash ON tracks(file_hash);

CREATE TABLE soundcloud_uncensored_cache (
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

CREATE INDEX idx_sc_cache_key ON soundcloud_uncensored_cache(cache_key);

CREATE TABLE app_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT '',
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE pinned_track_sources (
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

CREATE INDEX idx_pinned_sources_artist_title ON pinned_track_sources(artist_name, track_title);

CREATE TABLE tracked_artists (
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

CREATE INDEX idx_tracked_artists_clean ON tracked_artists(clean_name);

CREATE INDEX idx_tracked_artists_followed ON tracked_artists(is_followed);

CREATE TABLE radar_releases (
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

CREATE INDEX idx_radar_releases_artist ON radar_releases(clean_artist_name);

CREATE INDEX idx_radar_releases_status ON radar_releases(status);

CREATE INDEX idx_radar_releases_is_radar ON radar_releases(is_radar);

CREATE TABLE artist_relations (
    artist_name TEXT NOT NULL,
    related_name TEXT NOT NULL,
    score REAL NOT NULL, -- 0.0 to 1.0
    source TEXT NOT NULL, -- 'deezer', 'lastfm', 'manual'
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (artist_name, related_name)
);

CREATE INDEX idx_artist_rel_source ON artist_relations(artist_name);

CREATE TABLE radio_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    track_id INTEGER REFERENCES tracks(id) ON DELETE CASCADE,
    station_type TEXT NOT NULL, -- 'wave', 'genre', 'artist'
    station_param TEXT,         -- жанр или имя артиста
    played_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_radio_hist_track_time ON radio_history(track_id, played_at);

CREATE INDEX idx_radar_releases_artist_status ON radar_releases(clean_artist_name, status);

CREATE INDEX idx_radar_releases_clean_title ON radar_releases(clean_title);
