package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Keep the v1 schema intact. These references are installed for databases at v4,
// including fresh databases that have just passed through the preceding versions.
func migrateArtistIdentity(ctx context.Context, conn *sql.Conn) error {
	type link struct {
		id, name string
		durable  bool
	}
	tables := []struct {
		name  string
		links []link
	}{
		{"tracked_artists", []link{{"artist_id", "name", true}}},
		{"pinned_track_sources", []link{{"artist_id", "artist_name", true}}},
		{"artist_relations", []link{{"artist_id", "artist_name", true}, {"related_artist_id", "related_name", true}}},
		{"missing_tracks", []link{{"artist_id", "artist", false}}},
		{"download_jobs", []link{{"artist_id", "artist_name", false}}},
		{"soundcloud_uncensored_cache", []link{{"artist_id", "artist_name", false}}},
		{"radio_history", []link{{"station_artist_id", "station_param", false}}},
	}

	var relink []string
	for _, table := range tables {
		// The queue and radio have frequent writes that already own their
		// artist lookup. Avoid compiling a trigger on every play and snapshot.
		linkInWriter := table.name == "download_jobs" || table.name == "radio_history"
		lookup := ArtistIDLookup
		if table.name == "artist_relations" {
			// Older relation keys were already normalized. Their original
			// spelling cannot distinguish existing artists with the same key.
			lookup = normalizedArtistIDLookup
		}
		if table.name == "tracked_artists" {
			lookup = func(nameSQL string) string {
				// Legacy rows with distinct provider settings can share a
				// normalized name. Only a literal match can link those rows.
				return `COALESCE(` + exactArtistIDLookup(nameSQL) + `, CASE WHEN (
					SELECT COUNT(*) FROM tracked_artists siblings WHERE siblings.clean_name = music_search_key(` + nameSQL + `)
				) <= 1 THEN ` + normalizedArtistIDLookup(nameSQL) + ` END)`
			}
		}
		var updates, sources, changes []string
		for _, column := range table.links {
			onDelete := "SET NULL"
			if column.durable {
				onDelete = "RESTRICT"
			}
			if _, err := conn.ExecContext(ctx, fmt.Sprintf(`
				ALTER TABLE %[1]s ADD COLUMN %[2]s INTEGER REFERENCES artists(id) ON DELETE %[3]s;
				CREATE INDEX idx_%[1]s_%[2]s ON %[1]s(%[2]s) WHERE %[2]s IS NOT NULL;`,
				table.name, column.id, onDelete)); err != nil {
				return fmt.Errorf("%s.%s: %w", table.name, column.id, err)
			}

			name, newName, oldName := table.name+"."+column.name, "NEW."+column.name, "OLD."+column.name
			sources = append(sources, column.name)
			if table.name == "radio_history" {
				name = "CASE WHEN radio_history.station_type = 'artist' THEN radio_history.station_param END"
				newName = "CASE WHEN NEW.station_type = 'artist' THEN NEW.station_param END"
				oldName = "CASE WHEN OLD.station_type = 'artist' THEN OLD.station_param END"
			}
			if _, err := conn.ExecContext(ctx, "UPDATE "+table.name+" SET "+column.id+" = "+lookup(name)); err != nil {
				return fmt.Errorf("существующие ссылки %s.%s: %w", table.name, column.id, err)
			}
			relink = append(relink, fmt.Sprintf(`
				UPDATE %[1]s SET %[2]s = NEW.id
				WHERE %[2]s IS NULL AND music_search_key(%[3]s) = NEW.clean_name
					AND %[4]s = NEW.id;`, table.name, column.id, name, lookup(name)))
			expression := strings.ReplaceAll(name, table.name+".", "")
			if _, err := conn.ExecContext(ctx, fmt.Sprintf(`
				CREATE INDEX idx_%[1]s_%[2]s_unlinked ON %[1]s(music_search_key(%[3]s)) WHERE %[2]s IS NULL`,
				table.name, column.id, expression)); err != nil {
				return err
			}
			if linkInWriter {
				continue
			}
			changes = append(changes, "("+newName+") IS NOT ("+oldName+")")
			updates = append(updates, fmt.Sprintf(`
				%[1]s = CASE
					WHEN %[2]s IS %[3]s OR NEW.%[1]s IS NOT OLD.%[1]s THEN NEW.%[1]s
					ELSE %[4]s END`, column.id, newName, oldName, lookup(newName)))
			linkInsert := fmt.Sprintf(`
				CREATE TRIGGER link_%[1]s_%[2]s_insert AFTER INSERT ON %[1]s
				WHEN NEW.%[2]s IS NULL AND trim(%[4]s) != ''
				BEGIN
					UPDATE %[1]s SET %[2]s = %[3]s WHERE rowid = NEW.rowid;
				END;`, table.name, column.id, lookup(newName), newName)
			if _, err := conn.ExecContext(ctx, linkInsert); err != nil {
				return err
			}
		}
		if linkInWriter {
			continue
		}
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(`
			CREATE TRIGGER link_%s_update AFTER UPDATE OF %s ON %s WHEN %s
			BEGIN
				UPDATE %s SET %s WHERE rowid = NEW.rowid;
			END;`, table.name, strings.Join(sources, ", "), table.name, strings.Join(changes, " OR "), table.name, strings.Join(updates, ", "))); err != nil {
			return fmt.Errorf("обновление связи %s: %w", table.name, err)
		}
	}

	if _, err := conn.ExecContext(ctx, `
		CREATE TABLE radar_release_artists (
			release_id INTEGER NOT NULL REFERENCES radar_releases(id) ON DELETE CASCADE,
			credit_name TEXT NOT NULL,
			artist_id INTEGER REFERENCES artists(id) ON DELETE RESTRICT,
			PRIMARY KEY (release_id, credit_name)
		);
		CREATE INDEX idx_tracks_artist_id ON tracks(artist_id);
		CREATE INDEX idx_albums_artist_id ON albums(artist_id);
		CREATE UNIQUE INDEX idx_radar_release_artists_artist ON radar_release_artists(artist_id, release_id) WHERE artist_id IS NOT NULL;
		CREATE INDEX idx_radar_release_artists_unlinked ON radar_release_artists(music_search_key(credit_name)) WHERE artist_id IS NULL;
		CREATE INDEX idx_pinned_artist_title ON pinned_track_sources(artist_id, music_search_key(track_title))
		WHERE artist_id IS NOT NULL;
	`); err != nil {
		return err
	}

	// Keep all credited participants. The catalog's text is also retained for
	// display and for releases whose credits cannot yet be linked to a local artist.
	radarLinks := func(releaseID, credit, source string) string {
		return `
			INSERT INTO radar_release_artists (release_id, credit_name, artist_id)
			SELECT ` + releaseID + `, names.value, ` + ArtistIDLookup("names.value") + `
			FROM ` + source + `json_each(music_artist_credit_parts(` + credit + `)) AS names
			WHERE names.value != ''
				AND (names.value = ` + credit + ` OR ` + ArtistIDLookup(credit) + ` IS NULL)
			ON CONFLICT DO NOTHING;`
	}
	if _, err := conn.ExecContext(ctx, radarLinks("rr.id", "rr.artist_name", "radar_releases rr JOIN ")); err != nil {
		return err
	}
	runtimeLinks := radarLinks("NEW.id", "NEW.artist_name", "")
	if _, err := conn.ExecContext(ctx, `
		CREATE TRIGGER link_radar_artists_insert AFTER INSERT ON radar_releases
		BEGIN `+runtimeLinks+` END;
		CREATE TRIGGER link_radar_artists_update AFTER UPDATE OF artist_name ON radar_releases
		WHEN NEW.artist_name IS NOT OLD.artist_name
		BEGIN `+runtimeLinks+` END;`); err != nil {
		return err
	}
	relink = append(relink, `
		UPDATE OR IGNORE radar_release_artists SET artist_id = NEW.id
		WHERE artist_id IS NULL AND music_search_key(credit_name) = NEW.clean_name
			AND `+ArtistIDLookup("radar_release_artists.credit_name")+` = NEW.id;`)
	_, err := conn.ExecContext(ctx, "CREATE TRIGGER link_new_artist AFTER INSERT ON artists BEGIN "+strings.Join(relink, "\n")+" END;")
	return err
}
