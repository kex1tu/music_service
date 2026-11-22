package db

import (
	"context"
	"database/sql"
)

func migrateCensorshipHistory(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `
		ALTER TABLE censorship_items ADD COLUMN current_file_path TEXT;
		ALTER TABLE censorship_items ADD COLUMN current_reference_path TEXT;
		ALTER TABLE censorship_items ADD COLUMN audio_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE censorship_items ADD COLUMN manual_audio_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE censorship_items ADD COLUMN decided_at TEXT;
		UPDATE censorship_items
		SET current_file_path=file_path, current_reference_path=reference_path,
		    audio_hash=CASE WHEN json_valid(result_json) AND state='done' THEN
				CASE WHEN json_type(result_json,'$.audio_hash')='text'
				     THEN json_extract(result_json,'$.audio_hash') ELSE '' END
				ELSE '' END;
		CREATE INDEX idx_censorship_current_file ON censorship_items(coalesce(current_file_path,file_path));
		CREATE INDEX idx_censorship_current_reference ON censorship_items(coalesce(current_reference_path,reference_path));
		CREATE INDEX idx_censorship_manual_audio ON censorship_items(manual_audio_hash,decided_at DESC,id DESC)
		    WHERE manual_decision!='' AND manual_audio_hash!='';
		CREATE INDEX idx_censorship_history ON censorship_runs(created_at DESC,id DESC);
		CREATE INDEX idx_censorship_active ON censorship_runs(state);
		ALTER TABLE censorship_cache ADD COLUMN last_used_at INTEGER NOT NULL DEFAULT 0;
		UPDATE censorship_cache SET last_used_at=unixepoch();
		CREATE INDEX idx_censorship_cache_last_used ON censorship_cache(last_used_at);

		CREATE TABLE censorship_evidence_archive (
		    item_id INTEGER PRIMARY KEY REFERENCES censorship_items(id) ON DELETE CASCADE,
		    result_json TEXT NOT NULL,
		    evidence_rows_json TEXT NOT NULL
		);
		CREATE INDEX idx_censorship_evidence_migration ON censorship_evidence(item_id,id);
		INSERT INTO censorship_evidence_archive(item_id,result_json,evidence_rows_json)
		SELECT i.id,i.result_json,(
		    SELECT json_group_array(evidence_json) FROM (
		        SELECT evidence_json FROM censorship_evidence WHERE item_id=i.id ORDER BY id
		    )
		)
		FROM censorship_items i
		WHERE EXISTS (
		    SELECT value,count(*) FROM json_each(CASE WHEN json_valid(i.result_json) THEN i.result_json ELSE '{}' END,'$.evidence')
		    GROUP BY value
		    EXCEPT
		    SELECT CASE WHEN json_valid(evidence_json) THEN json(evidence_json) ELSE evidence_json END,count(*)
		    FROM censorship_evidence WHERE item_id=i.id GROUP BY 1
		) OR EXISTS (
		    SELECT CASE WHEN json_valid(evidence_json) THEN json(evidence_json) ELSE evidence_json END,count(*)
		    FROM censorship_evidence WHERE item_id=i.id GROUP BY 1
		    EXCEPT
		    SELECT value,count(*) FROM json_each(CASE WHEN json_valid(i.result_json) THEN i.result_json ELSE '{}' END,'$.evidence')
		    GROUP BY value
		);
		DROP TABLE censorship_evidence;
	`)
	return err
}
