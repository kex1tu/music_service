package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

func (a *CensorshipAudit) Runs(ctx context.Context) ([]CensorshipRun, error) {
	rows, e := a.db.QueryContext(ctx, `SELECT r.id,r.state,r.provider,r.created_at,count(i.id),coalesce(sum(i.state='done'),0),coalesce(sum(i.state='error'),0) FROM censorship_runs r LEFT JOIN censorship_items i ON i.run_id=r.id GROUP BY r.id ORDER BY r.created_at DESC,r.id LIMIT 25`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []CensorshipRun{}
	for rows.Next() {
		var r CensorshipRun
		if e = rows.Scan(&r.ID, &r.State, &r.Provider, &r.Created, &r.Total, &r.Done, &r.Errors); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (a *CensorshipAudit) Items(ctx context.Context, run, verdict string, limit, offset int) ([]CensorshipItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, e := a.db.QueryContext(ctx, `SELECT id,file_path,reference_path,state,verdict,result_json,error,manual_decision,manual_note FROM censorship_items WHERE run_id=? AND (?='' OR verdict=?) ORDER BY id LIMIT ? OFFSET ?`, run, verdict, verdict, limit, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []CensorshipItem{}
	for rows.Next() {
		var i CensorshipItem
		var raw string
		if e = rows.Scan(&i.ID, &i.Path, &i.Reference, &i.State, &i.Verdict, &raw, &i.Error, &i.Decision, &i.Note); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &i.Result); e != nil {
			return nil, e
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
func (a *CensorshipAudit) Decide(ctx context.Context, id int64, decision, note string) error {
	switch decision {
	case "confirmed_censored", "cleared", "uncertain", "":
	default:
		return fmt.Errorf("invalid manual decision")
	}
	if len(note) > 4000 {
		return fmt.Errorf("note too long")
	}
	res, e := a.db.ExecContext(ctx, "UPDATE censorship_items SET manual_decision=?,manual_note=? WHERE id=?", decision, note, id)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
