package db

import (
	"context"
	"database/sql"
)

func migrateRemoveRadio(ctx context.Context, conn *sql.Conn) error {
	// The trigger belongs to artists, so SQLite does not drop it with the
	// radio tables. Retain the two relinks still used by Radar.
	_, err := conn.ExecContext(ctx, `
		DROP TRIGGER link_new_artist;
		DROP TABLE radio_history;
		DROP TABLE artist_relations;
		CREATE TRIGGER link_new_artist AFTER INSERT ON artists BEGIN
			UPDATE tracked_artists SET artist_id = NEW.id
			WHERE artist_id IS NULL AND music_search_key(name) = NEW.clean_name
				AND COALESCE(`+exactArtistIDLookup("tracked_artists.name")+`, CASE WHEN (
					SELECT COUNT(*) FROM tracked_artists siblings WHERE siblings.clean_name = music_search_key(tracked_artists.name)
				) <= 1 THEN `+normalizedArtistIDLookup("tracked_artists.name")+` END) = NEW.id;
			UPDATE OR IGNORE radar_release_artists SET artist_id = NEW.id
			WHERE artist_id IS NULL AND music_search_key(credit_name) = NEW.clean_name
				AND `+ArtistIDLookup("radar_release_artists.credit_name")+` = NEW.id;
		END;
	`)
	return err
}
