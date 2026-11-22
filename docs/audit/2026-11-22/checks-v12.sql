-- Только чтение. Открывать согласованную копию v12 в mode=ro.
-- Колонки check_name и affected: ноль означает, что проверка не нашла строк.
-- Полная физическая проверка требует SQLite с функциями приложения.
BEGIN;
SELECT 'schema_version' AS metric, user_version AS value FROM pragma_user_version;
SELECT 'foreign_key_violations' AS check_name, count(*) AS affected FROM pragma_foreign_key_check
UNION ALL
SELECT 'orphan_track_issue_references', count(*) FROM audit_issues i
 WHERE i.entity_type='track' AND i.entity_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM tracks t WHERE t.id=i.entity_id)
UNION ALL
SELECT 'checked_album_counters', count(*) FROM albums a
 WHERE a.catalog_checked_at IS NOT NULL AND
 (a.expected_tracks_count IS NOT (SELECT count(*) FROM release_tracklist r WHERE r.album_id=a.id)
 OR a.present_tracks_count IS NOT (SELECT count(*) FROM release_tracklist r WHERE r.album_id=a.id AND r.is_present=1)
 OR a.is_complete IS NOT (a.expected_tracks_count>0 AND a.expected_tracks_count=a.present_tracks_count))
UNION ALL
SELECT 'numbered_position_collisions', count(*) FROM
 (SELECT 1 FROM tracks WHERE track_number>0 GROUP BY album_id, disc_number, track_number HAVING count(*)>1)
UNION ALL
SELECT 'shared_local_deezer_artist_ids', count(*) FROM
 (SELECT 1 FROM artists WHERE nullif(deezer_id,'') IS NOT NULL GROUP BY deezer_id HAVING count(*)>1)
UNION ALL
SELECT 'shared_concrete_deezer_release_ids', count(*) FROM
 (SELECT 1 FROM radar_release_sources WHERE provider='deezer' GROUP BY external_id HAVING count(*)>1)
UNION ALL
SELECT 'album_credit_position_gaps', count(*) FROM
 (SELECT 1 FROM album_artists GROUP BY album_id HAVING min(position)<>1 OR max(position)<>count(*))
UNION ALL
SELECT 'invalid_job_snapshot_json', count(*) FROM download_jobs
 WHERE selection_json IS NOT NULL AND NOT json_valid(selection_json)
UNION ALL
SELECT 'invalid_censorship_result_json', count(*) FROM censorship_items WHERE NOT json_valid(result_json)
UNION ALL
SELECT 'invalid_radar_tracklist_json', count(*) FROM radar_releases WHERE tracklist IS NOT NULL AND NOT json_valid(tracklist);
-- Отдельные наблюдения. Совпадение имени или несколько настроек требуют интерпретации.
SELECT 'homonymous_artist_keys' AS observation, count(*) AS groups_found FROM
 (SELECT 1 FROM artists GROUP BY clean_name HAVING count(*)>1)
UNION ALL
SELECT 'multiple_settings_per_artist', count(*) FROM
 (SELECT 1 FROM tracked_artists WHERE artist_id IS NOT NULL GROUP BY artist_id HAVING count(*)>1)
UNION ALL
SELECT 'external_credits_without_local_id', count(*) FROM radar_release_artists WHERE artist_id IS NULL;
COMMIT;
