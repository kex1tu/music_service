package audit

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

var (
	errCensorshipCursor = errors.New("неверный курсор истории")
	errCensorshipInput  = errors.New("некорректные параметры проверки")
	errCensorshipAudio  = errors.New("аудио недоступно или отличается от проверенной записи")
)

const censorshipRunCounts = `
	SELECT r.id,r.state,r.provider,r.created_at,
	       (SELECT count(*) FROM censorship_items WHERE run_id=r.id),
	       (SELECT count(*) FROM censorship_items WHERE run_id=r.id AND state='done'),
	       (SELECT count(*) FROM censorship_items WHERE run_id=r.id AND state='error')
`

type censorshipCursor struct {
	Created string
	ID      string
}

func scanCensorshipRun(row interface{ Scan(...any) error }) (CensorshipRun, error) {
	var r CensorshipRun
	err := row.Scan(&r.ID, &r.State, &r.Provider, &r.Created, &r.Total, &r.Done, &r.Errors)
	return r, err
}

func (a *CensorshipAudit) Runs(ctx context.Context, limit int, cursor string) ([]CensorshipRun, string, error) {
	if limit < 1 || limit > 100 {
		return nil, "", errCensorshipInput
	}
	filter := ""
	args := []any{}
	if cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		var boundary censorshipCursor
		if err != nil || len(cursor) > 512 || json.Unmarshal(data, &boundary) != nil ||
			boundary.Created == "" || boundary.ID == "" {
			return nil, "", errCensorshipCursor
		}
		filter = "WHERE (created_at,id)<(?,?)"
		args = append(args, boundary.Created, boundary.ID)
	}
	args = append(args, limit+1)
	rows, err := a.db.QueryContext(ctx, `
		WITH page AS (
			SELECT id,state,provider,created_at FROM censorship_runs
			`+filter+` ORDER BY created_at DESC,id DESC LIMIT ?
		)
		`+censorshipRunCounts+` FROM page r ORDER BY created_at DESC,id DESC
	`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []CensorshipRun{}
	for rows.Next() {
		r, err := scanCensorshipRun(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var next string
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		data, err := json.Marshal(censorshipCursor{Created: last.Created, ID: last.ID})
		if err != nil {
			return nil, "", err
		}
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	return out, next, nil
}

func (a *CensorshipAudit) Run(ctx context.Context, id string) (CensorshipRun, error) {
	return scanCensorshipRun(a.db.QueryRowContext(ctx, censorshipRunCounts+`
		FROM censorship_runs r WHERE r.id=?
	`, id))
}

type censorshipQuota struct {
	Provider  string `json:"provider"`
	Day       string `json:"day"`
	Limit     int    `json:"limit_seconds"`
	Reserved  int    `json:"reserved_seconds"`
	Remaining int    `json:"remaining_seconds"`
}

func (a *CensorshipAudit) quota(ctx context.Context, providers []string, now time.Time) ([]censorshipQuota, error) {
	out := []censorshipQuota{}
	day := now.UTC().Format(time.DateOnly)
	for _, provider := range providers {
		q := censorshipQuota{Provider: provider, Day: day, Limit: a.dailySeconds}
		err := a.db.QueryRowContext(ctx, `
			SELECT coalesce((SELECT seconds FROM censorship_cloud_usage WHERE provider=? AND day=?),0)
		`, provider, day).Scan(&q.Reserved)
		if err != nil {
			return nil, err
		}
		q.Remaining = max(0, q.Limit-q.Reserved)
		out = append(out, q)
	}
	return out, nil
}

func (a *CensorshipAudit) pruneCensorshipCache(ctx context.Context, now time.Time) error {
	const batch = 500
	cutoff := now.Add(-90 * 24 * time.Hour).Unix()
	for {
		res, err := a.db.ExecContext(ctx, `
			DELETE FROM censorship_cache WHERE cache_key IN (
				SELECT cache_key FROM censorship_cache WHERE last_used_at<?
				ORDER BY last_used_at LIMIT ?
			)
		`, cutoff, batch)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n < batch {
			return err
		}
	}
}

func (a *CensorshipAudit) Items(ctx context.Context, run, verdict string, limit, offset int) ([]CensorshipItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := a.db.QueryContext(ctx, `
		WITH page AS (
			SELECT * FROM censorship_items WHERE run_id=? AND (?='' OR verdict=?) ORDER BY id LIMIT ? OFFSET ?
		)
		SELECT i.id,i.file_path,i.reference_path,coalesce(i.current_file_path,i.file_path),
		       coalesce(i.current_reference_path,i.reference_path),i.state,i.verdict,i.result_json,
		       i.error,i.manual_decision,i.manual_note,i.manual_audio_hash,i.audio_hash,
		       coalesce(i.decided_at,''),p.run_id,p.manual_decision,p.decided_at
		FROM page i LEFT JOIN censorship_items p ON p.id=(
			SELECT prior.id FROM censorship_items prior
			WHERE prior.manual_audio_hash=i.audio_hash AND prior.manual_audio_hash!=''
			  AND prior.manual_decision!='' AND prior.run_id!=i.run_id AND prior.id<i.id
			ORDER BY prior.decided_at DESC,prior.id DESC LIMIT 1
		)
		ORDER BY i.id
	`, run, verdict, verdict, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CensorshipItem{}
	for rows.Next() {
		var i CensorshipItem
		var raw, manualHash, audioHash string
		var priorRun, priorDecision, priorDate sql.NullString
		if err := rows.Scan(&i.ID, &i.Path, &i.Reference, &i.CurrentPath, &i.CurrentReference, &i.State,
			&i.Verdict, &raw, &i.Error, &i.Decision, &i.Note, &manualHash, &audioHash, &i.DecidedAt,
			&priorRun, &priorDecision, &priorDate); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &i.Result); err != nil {
			return nil, err
		}
		for j := range i.Result.Evidence {
			if i.Result.Evidence[j].Origin == "reference_comparison" {
				i.Result.Evidence[j].Reference = i.CurrentReference
			}
		}
		i.DecisionVerified = i.Decision != "" && manualHash != "" && manualHash == audioHash
		if priorRun.Valid {
			i.PreviousDecision = &CensorshipDecision{RunID: priorRun.String, Decision: priorDecision.String, DecidedAt: priorDate.String}
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (a *CensorshipAudit) Decide(ctx context.Context, id int64, decision, note string) error {
	return a.saveDecision(ctx, id, &decision, &note)
}

func (a *CensorshipAudit) saveDecision(ctx context.Context, id int64, decision, note *string) error {
	if decision != nil {
		switch *decision {
		case "confirmed_censored", "cleared", "uncertain", "":
		default:
			return fmt.Errorf("%w: invalid manual decision", errCensorshipInput)
		}
	}
	if note != nil && utf8.RuneCountInString(*note) > 4000 {
		return fmt.Errorf("%w: note too long", errCensorshipInput)
	}
	res, err := a.db.ExecContext(ctx, `
		UPDATE censorship_items
		SET manual_note=coalesce(?2,manual_note),
		    manual_decision=coalesce(?1,manual_decision),
		    manual_audio_hash=CASE WHEN ?1 IS NULL THEN manual_audio_hash WHEN ?1='' THEN '' ELSE audio_hash END,
		    decided_at=CASE WHEN ?1 IS NULL THEN decided_at WHEN ?1='' THEN NULL
				ELSE strftime('%Y-%m-%dT%H:%M:%fZ','now') END
		WHERE id=?3 AND (?1 IS NULL OR ?1='' OR (state='done' AND audio_hash!=''))
	`, decision, note, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n > 0 {
		return err
	}
	var exists bool
	if err := a.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM censorship_items WHERE id=?)", id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return sql.ErrNoRows
	}
	return ErrAuditState
}
