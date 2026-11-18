CREATE INDEX IF NOT EXISTS idx_tracks_artist ON tracks(artist_id);
CREATE INDEX IF NOT EXISTS idx_audit_issues_entity ON audit_issues(entity_type, entity_id);

-- Filesystem changes awaiting a matching library-index transaction.
CREATE TABLE IF NOT EXISTS library_index_pending (
    target_path TEXT PRIMARY KEY,
    source_path TEXT NOT NULL,
    source_is_staging INTEGER NOT NULL,
    merge_into_target INTEGER NOT NULL,
    delete_kind INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_library_index_pending_source ON library_index_pending(source_path)
    WHERE source_is_staging = 0;
