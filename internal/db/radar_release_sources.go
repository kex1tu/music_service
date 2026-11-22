package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

type RadarCredit struct {
	Name       string `json:"name"`
	ExternalID string `json:"external_id,omitempty"`
}

type RadarSourceMetadata struct {
	Credits          []RadarCredit
	Title            string
	ArtistName       string
	MultipleEditions bool
}

// Group-only data is useful for its source row. The edition card keeps its
// more precise display title, credits, download URL and reconciliation status.
const radarGroupRefresh = `(?10 = '' AND ?12 = '' AND ?11 <> ''
	AND (COALESCE(radar_releases.deezer_id, '') <> '' OR COALESCE(radar_releases.shazam_id, '') <> ''))`

// A concrete catalog ID wins. A MusicBrainz group can promote a group-only
// card to a concrete edition, but never makes two concrete editions identical.
const radarSourceMatch = `(
	SELECT COALESCE((
		SELECT r.identity_key
		FROM radar_release_sources s JOIN radar_releases r ON r.id = s.release_id
		WHERE (s.provider = 'deezer' AND s.external_id = NULLIF(?10, ''))
		   OR (s.provider = 'shazam' AND s.external_id = NULLIF(?12, '') AND
		       (?10 = '' OR COALESCE(r.deezer_id, '') IN ('', ?10)))
		ORDER BY s.provider = 'deezer' DESC, r.id
		LIMIT 1
	), (
		SELECT MIN(r.identity_key)
		FROM radar_release_sources s JOIN radar_releases r ON r.id = s.release_id
		WHERE s.provider = 'musicbrainz' AND s.external_id = NULLIF(?11, '')
		  AND ((?10 = '' AND ?12 = '') OR (?20 = 0 AND NOT EXISTS (
			SELECT 1 FROM radar_release_sources edition
			WHERE edition.release_id = r.id AND edition.provider <> 'musicbrainz'
		  ) AND NOT EXISTS (
			SELECT 1 FROM radar_release_sources peer
			WHERE peer.provider = 'musicbrainz' AND peer.external_id = s.external_id AND peer.release_id <> r.id
		  )))
		HAVING COUNT(*) = 1
	))
)`

// Once a link identifies a sole concrete edition, fold an older group-only
// card into it. The concrete card keeps its ID and inherits terminal status.
func mergeRadarGroupPlaceholder(ctx context.Context, tx *sql.Tx, r *RadarReleaseEntity) error {
	if r.MusicBrainzID == "" || (r.DeezerID == "" && r.ShazamID == "") ||
		(r.MusicBrainzMetadata != nil && r.MusicBrainzMetadata.MultipleEditions) {
		return nil
	}
	var oldID int64
	var oldStatus string
	err := tx.QueryRowContext(ctx, `
		SELECT old.id, old.status FROM radar_release_sources s JOIN radar_releases old ON old.id = s.release_id
		WHERE s.provider = 'musicbrainz' AND s.external_id = ?1 AND old.id <> ?2
		  AND NOT EXISTS (SELECT 1 FROM radar_release_sources edition
			  WHERE edition.release_id = old.id AND edition.provider <> 'musicbrainz')
		  AND (SELECT COUNT(*) FROM radar_release_sources peers
			  WHERE peers.provider = 'musicbrainz' AND peers.external_id = ?1) = 2
	`, r.MusicBrainzID, r.ID).Scan(&oldID, &oldStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if oldStatus == "ignored" || (oldStatus == "downloaded" && r.Status != "ignored") {
		if _, err := tx.ExecContext(ctx, "UPDATE radar_releases SET status = ?, is_radar = 0 WHERE id = ?", oldStatus, r.ID); err != nil {
			return err
		}
		r.Status, r.IsRadar = oldStatus, false
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO radar_release_artists(release_id, credit_name, artist_id)
		SELECT ?1, credit_name, artist_id FROM radar_release_artists WHERE release_id = ?2 ON CONFLICT DO NOTHING;
		INSERT INTO radar_release_sources(release_id, provider, external_id, source_url, title, artist_name, tracklist, credits_json)
		SELECT ?1, provider, external_id, source_url, title, artist_name, tracklist, credits_json
		FROM radar_release_sources WHERE release_id = ?2
		ON CONFLICT(release_id, provider, external_id) DO UPDATE
		SET credits_json = COALESCE(radar_release_sources.credits_json, excluded.credits_json);
		DELETE FROM radar_releases WHERE id = ?2;
	`, r.ID, oldID)
	return err
}

func saveRadarReleaseSources(ctx context.Context, tx *sql.Tx, r *RadarReleaseEntity, tracklist any) error {
	sources := []struct{ provider, id, host, defaultURL string }{
		{"deezer", r.DeezerID, "deezer.com", "https://www.deezer.com/album/" + r.DeezerID},
		{"musicbrainz", r.MusicBrainzID, "musicbrainz.org", "https://musicbrainz.org/release-group/" + r.MusicBrainzID},
		{"shazam", r.ShazamID, "shazam.com", ""},
	}
	primary := ""
	for _, source := range sources {
		if source.id != "" {
			primary = source.provider
			break
		}
	}
	parsedURL, _ := url.Parse(r.SourceURL)
	host := ""
	if parsedURL != nil {
		host = strings.ToLower(parsedURL.Hostname())
	}
	for _, source := range sources {
		if source.id == "" {
			continue
		}
		sourceURL := source.defaultURL
		if host == source.host || strings.HasSuffix(host, "."+source.host) {
			sourceURL = r.SourceURL
		}
		var sourceTracklist any
		metadata := RadarSourceMetadata{Title: r.Title, ArtistName: r.ArtistName}
		if primary == source.provider {
			metadata.Credits = releaseCredits(r)
		}
		hasOwnMetadata := primary == source.provider
		if source.provider == "musicbrainz" && r.MusicBrainzMetadata != nil {
			credits := metadata.Credits
			metadata = *r.MusicBrainzMetadata
			if metadata.Credits == nil {
				metadata.Credits = credits
			}
			hasOwnMetadata = true
		}
		if primary == source.provider {
			sourceTracklist = tracklist
		}
		var creditsJSON any
		if metadata.Credits != nil {
			payload, err := json.Marshal(metadata.Credits)
			if err != nil {
				return err
			}
			creditsJSON = string(payload)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO radar_release_sources(release_id, provider, external_id, source_url, title, artist_name, tracklist, credits_json)
			VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?9)
			ON CONFLICT(release_id, provider, external_id) DO UPDATE SET
				source_url = COALESCE(NULLIF(excluded.source_url, ''), radar_release_sources.source_url),
				title = CASE WHEN ?8 THEN excluded.title ELSE radar_release_sources.title END,
				artist_name = CASE WHEN ?8 THEN excluded.artist_name ELSE radar_release_sources.artist_name END,
				tracklist = COALESCE(excluded.tracklist, radar_release_sources.tracklist),
			credits_json = COALESCE(excluded.credits_json, radar_release_sources.credits_json)
		`, r.ID, source.provider, source.id, sourceURL, metadata.Title, metadata.ArtistName, sourceTracklist, hasOwnMetadata, creditsJSON); err != nil {
			return err
		}
	}
	return nil
}

func releaseCredits(r *RadarReleaseEntity) []RadarCredit {
	if r.ProviderCredits != nil {
		return r.ProviderCredits
	}
	if r.CatalogCredits == nil {
		return nil
	}
	result := make([]RadarCredit, 0, len(r.CatalogCredits))
	for _, name := range r.CatalogCredits {
		result = append(result, RadarCredit{Name: name})
	}
	return result
}

func refreshRadarCatalogCredits(ctx context.Context, tx *sql.Tx, r *RadarReleaseEntity) error {
	// Keep migrated credits until the selected source has supplied a snapshot.
	var authoritative, hasSources bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM radar_release_sources s WHERE s.release_id = rr.id
			AND s.credits_json IS NOT NULL AND (
				(s.provider = 'deezer' AND s.external_id = NULLIF(rr.deezer_id, ''))
				OR (COALESCE(rr.deezer_id, '') = '' AND s.provider = 'shazam' AND s.external_id = NULLIF(rr.shazam_id, ''))
				OR (COALESCE(rr.deezer_id, '') = '' AND COALESCE(rr.shazam_id, '') = ''
					AND s.provider = 'musicbrainz' AND s.external_id = NULLIF(rr.musicbrainz_id, ''))
			)), EXISTS(SELECT 1 FROM radar_release_sources s WHERE s.release_id = rr.id)
		FROM radar_releases rr WHERE rr.id = ?`, r.ID).Scan(&authoritative, &hasSources); err != nil {
		return err
	}
	if !hasSources && releaseCredits(r) == nil {
		return nil
	}
	if authoritative || !hasSources {
		if _, err := tx.ExecContext(ctx, "DELETE FROM radar_release_artists WHERE release_id = ?", r.ID); err != nil {
			return err
		}
	}
	if !hasSources {
		for _, credit := range releaseCredits(r) {
			if err := insertRadarCredit(ctx, tx, r.ID, "", credit); err != nil {
				return err
			}
		}
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT provider, credits_json FROM radar_release_sources
		WHERE release_id = ? AND credits_json IS NOT NULL ORDER BY provider, external_id`, r.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var provider, payload string
		if err := rows.Scan(&provider, &payload); err != nil {
			return err
		}
		var credits []RadarCredit
		if err := json.Unmarshal([]byte(payload), &credits); err != nil {
			return err
		}
		for _, credit := range credits {
			if err := insertRadarCredit(ctx, tx, r.ID, provider, credit); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}

func insertRadarCredit(ctx context.Context, tx *sql.Tx, releaseID int64, provider string, credit RadarCredit) error {
	name := strings.TrimSpace(credit.Name)
	if name == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		WITH provider_artists AS (
			SELECT DISTINCT ar.id, COALESCE(NULLIF(ar.deezer_id, ''), NULLIF(ta.deezer_id, '')) AS external_id
			FROM artists ar LEFT JOIN tracked_artists ta ON ta.artist_id = ar.id WHERE ?3 = 'deezer'
			UNION SELECT artist_id, NULLIF(musicbrainz_id, '') FROM tracked_artists
			WHERE ?3 = 'musicbrainz' AND artist_id IS NOT NULL
		), provider_match AS (
			SELECT CASE WHEN COUNT(DISTINCT id) = 1 THEN MIN(id) END AS id,
				COUNT(DISTINCT id) AS matches FROM provider_artists WHERE external_id = NULLIF(?4, '')
		), named(id) AS (SELECT `+ArtistIDLookup("?2")+`)
		INSERT INTO radar_release_artists(release_id, credit_name, artist_id)
		SELECT ?1, ?2,
			CASE WHEN provider_match.matches > 0 THEN provider_match.id
				WHEN ?4 = '' OR NOT EXISTS(SELECT 1 FROM provider_artists WHERE id = named.id AND external_id IS NOT NULL)
				THEN named.id END
		FROM named CROSS JOIN provider_match WHERE true ON CONFLICT DO NOTHING
	`, releaseID, name, provider, credit.ExternalID)
	return err
}
