package db

import (
	"context"
	"database/sql"
)

func migrateRadarReleaseSources(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `
		CREATE TABLE radar_release_sources (
			release_id INTEGER NOT NULL REFERENCES radar_releases(id) ON DELETE CASCADE,
			provider TEXT NOT NULL,
			external_id TEXT NOT NULL CHECK (external_id <> ''),
			source_url TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL,
			artist_name TEXT NOT NULL,
			tracklist TEXT,
			PRIMARY KEY (release_id, provider, external_id)
		);
		CREATE INDEX idx_radar_release_sources_lookup ON radar_release_sources(provider, external_id, release_id);

		INSERT INTO radar_release_sources (release_id, provider, external_id, source_url, title, artist_name, tracklist)
		SELECT id, 'deezer', deezer_id, CASE WHEN source_url LIKE '%deezer.com/%' THEN source_url
			ELSE 'https://www.deezer.com/album/' || deezer_id END, title, artist_name, tracklist
		FROM radar_releases WHERE deezer_id <> ''
		UNION ALL
		SELECT id, 'musicbrainz', musicbrainz_id, 'https://musicbrainz.org/release-group/' || musicbrainz_id,
			title, artist_name, CASE WHEN COALESCE(deezer_id, '') = '' AND source_url LIKE '%musicbrainz.org/%' THEN tracklist END
		FROM radar_releases WHERE musicbrainz_id <> ''
		UNION ALL
		SELECT id, 'shazam', shazam_id, CASE WHEN source_url LIKE '%shazam.com/%' THEN source_url ELSE '' END,
			title, artist_name, CASE WHEN source_url LIKE '%shazam.com/%' THEN tracklist END
		FROM radar_releases WHERE shazam_id <> '';

		ALTER TABLE radar_releases ADD COLUMN has_catalog_credits INTEGER NOT NULL DEFAULT 0;
		DROP TRIGGER link_radar_artists_insert;
		DROP TRIGGER link_radar_artists_update;
		CREATE TRIGGER link_radar_artists_insert AFTER INSERT ON radar_releases
		WHEN NEW.has_catalog_credits = 0 BEGIN `+linkLegacyRadarCredit+` END;
		CREATE TRIGGER link_radar_artists_update AFTER UPDATE OF artist_name ON radar_releases
		WHEN NEW.has_catalog_credits = 0 AND NEW.artist_name IS NOT OLD.artist_name
		BEGIN `+linkLegacyRadarCredit+` END;
	`)
	return err
}

var linkLegacyRadarCredit = `
	INSERT INTO radar_release_artists (release_id, credit_name, artist_id)
	SELECT NEW.id, names.value, ` + ArtistIDLookup("names.value") + `
	FROM json_each(music_artist_credit_parts(NEW.artist_name)) names
	WHERE names.value <> '' AND (names.value = NEW.artist_name OR ` + ArtistIDLookup("NEW.artist_name") + ` IS NULL)
	ON CONFLICT DO NOTHING;
`
