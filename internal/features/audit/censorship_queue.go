package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/services/censorcheck"
)

// NewCensorshipAudit owns one worker and resumes only previously running local
// jobs. Cloud jobs restart in waiting state to avoid ambiguous paid retries.
func NewCensorshipAudit(db *sql.DB, sb *sandbox.PathSandbox, clouds map[string]censorcheck.Transcriber, dailySeconds int) (*CensorshipAudit, error) {
	if db == nil || sb == nil {
		return nil, fmt.Errorf("audit storage and sandbox required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &CensorshipAudit{db: db, sb: sb, clouds: map[string]censorcheck.Transcriber{}, dailySeconds: dailySeconds, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
	for p, c := range clouds {
		a.clouds[p] = c
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		cancel()
		return nil, e
	}
	defer tx.Rollback()
	for _, q := range []string{
		"UPDATE censorship_runs SET state='waiting' WHERE provider!='local' AND state='running'",
		"UPDATE censorship_items SET state='queued',attempt_token='' WHERE state='running'",
	} {
		if _, e = tx.ExecContext(ctx, q); e != nil {
			cancel()
			return nil, e
		}
	}
	if e = tx.Commit(); e != nil {
		cancel()
		return nil, e
	}
	a.wg.Add(1)
	go a.loop()
	return a, nil
}
func (a *CensorshipAudit) Close() {
	a.cancel()
	a.mu.Lock()
	if a.activeCancel != nil {
		a.activeCancel()
	}
	a.mu.Unlock()
	a.wg.Wait()
}
func (a *CensorshipAudit) notify() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *CensorshipAudit) Start(ctx context.Context, o CensorshipOptions) (string, error) {
	if o.Provider == "" {
		o.Provider = "local"
	}
	if o.Provider != "local" && a.clouds[o.Provider] == nil {
		return "", fmt.Errorf("provider disabled or not configured")
	}
	if o.Provider != "local" && a.dailySeconds <= 0 {
		return "", fmt.Errorf("cloud budget is disabled")
	}
	paths := append([]string(nil), o.Paths...)
	if len(paths) == 0 {
		root, e := a.sb.Resolve(o.Path, true)
		if e != nil {
			return "", e
		}
		e = safefs.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".mp3") {
				rel, e := a.sb.ToRelative(p)
				if e != nil {
					return e
				}
				paths = append(paths, rel)
			}
			return nil
		})
		if e != nil {
			return "", e
		}
	}
	unique := []string{}
	seen := map[string]bool{}
	for _, p := range paths {
		full, e := a.sb.Resolve(p, false)
		if e != nil {
			return "", e
		}
		rel, e := a.sb.ToRelative(full)
		if e != nil {
			return "", e
		}
		info, e := safefs.Stat(full)
		if e != nil {
			return "", fmt.Errorf("selected track unavailable")
		}
		if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(rel), ".mp3") {
			return "", fmt.Errorf("only MP3 files can be audited")
		}
		if !seen[rel] {
			seen[rel] = true
			unique = append(unique, rel)
		}
	}
	if len(unique) == 0 {
		return "", fmt.Errorf("no MP3 tracks selected")
	}
	for _, ref := range o.References {
		resolved, e := a.sb.Resolve(ref, false)
		if e != nil {
			return "", e
		}
		info, e := safefs.Stat(resolved)
		if e != nil || !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(ref), ".mp3") {
			return "", fmt.Errorf("reference must be an available MP3")
		}
	}
	o.Paths = nil
	raw, e := json.Marshal(o)
	if e != nil {
		return "", e
	}
	id := uuid.NewString()
	tx, e := a.db.BeginTx(ctx, nil)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "INSERT INTO censorship_runs(id,state,provider,options_json) VALUES(?,'running',?,?)", id, o.Provider, string(raw)); e != nil {
		return "", e
	}
	stmt, e := tx.PrepareContext(ctx, "INSERT INTO censorship_items(run_id,file_path,reference_path) VALUES(?,?,?)")
	if e != nil {
		return "", e
	}
	defer stmt.Close()
	for _, p := range unique {
		if _, e = stmt.ExecContext(ctx, id, p, o.References[p]); e != nil {
			return "", e
		}
	}
	if e = tx.Commit(); e != nil {
		return "", e
	}
	a.notify()
	return id, nil
}

func (a *CensorshipAudit) Control(ctx context.Context, id, action string) error {
	state := ""
	switch action {
	case "pause":
		state = "paused"
	case "resume", "retry":
		state = "running"
	case "cancel":
		state = "cancelled"
	default:
		return ErrAuditState
	}
	tx, e := a.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var current string
	if e = tx.QueryRowContext(ctx, "SELECT state FROM censorship_runs WHERE id=?", id).Scan(&current); e != nil {
		return e
	}
	if current == "cancelled" || (current == "completed" && action != "retry") {
		return ErrAuditState
	}
	if action == "pause" || action == "cancel" {
		a.mu.Lock()
		if a.activeRun == id && a.activeCancel != nil {
			a.activeCancel()
		}
		a.mu.Unlock()
		next := "queued"
		if action == "cancel" {
			next = "cancelled"
		}
		if _, e = tx.ExecContext(ctx, "UPDATE censorship_items SET state=?,attempt_token='' WHERE run_id=? AND state IN ('running','queued')", next, id); e != nil {
			return e
		}
	}
	if action == "retry" {
		if _, e = tx.ExecContext(ctx, "UPDATE censorship_items SET state='queued',error='',attempt_token='' WHERE run_id=? AND state='error'", id); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, "UPDATE censorship_runs SET state=?,updated_at=CURRENT_TIMESTAMP WHERE id=?", state, id); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	a.notify()
	return nil
}

func (a *CensorshipAudit) loop() {
	defer a.wg.Done()
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-timer.C:
		case <-a.wake:
		}
		for a.ctx.Err() == nil {
			worked, e := a.step()
			if e != nil && a.ctx.Err() == nil {
				log.Printf("[Censorship] queue/storage failure: %v", e)
			}
			if e != nil || !worked {
				break
			}
		}
	}
}
func (a *CensorshipAudit) step() (bool, error) {
	var id int64
	var run, path, ref, provider string
	e := a.db.QueryRowContext(a.ctx, `SELECT i.id,i.run_id,i.file_path,i.reference_path,r.provider FROM censorship_items i JOIN censorship_runs r ON r.id=i.run_id WHERE i.state='queued' AND r.state='running' ORDER BY r.created_at,i.id LIMIT 1`).Scan(&id, &run, &path, &ref, &provider)
	if errors.Is(e, sql.ErrNoRows) {
		_, e = a.db.ExecContext(a.ctx, `UPDATE censorship_runs SET state='completed',updated_at=CURRENT_TIMESTAMP WHERE state='running' AND NOT EXISTS(SELECT 1 FROM censorship_items WHERE run_id=censorship_runs.id AND state IN ('queued','running'))`)
		return false, e
	}
	if e != nil {
		return false, e
	}
	token := uuid.NewString()
	res, e := a.db.ExecContext(a.ctx, "UPDATE censorship_items SET state='running',attempt_token=? WHERE id=? AND state='queued' AND EXISTS(SELECT 1 FROM censorship_runs WHERE id=? AND state='running')", token, id, run)
	if e != nil {
		return false, e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return true, nil
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	a.activeRun = run
	a.activeCancel = cancel
	a.mu.Unlock()
	// Pause may have raced with publication of activeCancel; state/token guards
	// and cancellation checks prevent stale results from being committed.
	var active int
	if e = a.db.QueryRowContext(ctx, "SELECT count(*) FROM censorship_items i JOIN censorship_runs r ON r.id=i.run_id WHERE i.id=? AND i.attempt_token=? AND i.state='running' AND r.state='running'", id, token).Scan(&active); e != nil || active == 0 {
		cancel()
	}
	result, analysisErr := a.analyze(ctx, path, ref, provider)
	wasCancelled := ctx.Err() != nil
	cancel()
	a.mu.Lock()
	a.activeRun = ""
	a.activeCancel = nil
	a.mu.Unlock()
	if wasCancelled {
		return true, nil
	}
	if errors.Is(analysisErr, censorcheck.ErrCloudWaiting) {
		tx, e := a.db.BeginTx(a.ctx, nil)
		if e != nil {
			return false, e
		}
		defer tx.Rollback()
		res, e = tx.ExecContext(a.ctx, "UPDATE censorship_items SET state='queued',attempt_token='',error=? WHERE id=? AND attempt_token=? AND state='running'", analysisErr.Error(), id, token)
		if e != nil {
			return false, e
		}
		n, _ = res.RowsAffected()
		if n > 0 {
			if _, e = tx.ExecContext(a.ctx, "UPDATE censorship_runs SET state='waiting' WHERE id=? AND state='running'", run); e != nil {
				return false, e
			}
		}
		return true, tx.Commit()
	}
	state, errText := "done", ""
	if analysisErr != nil {
		state = "error"
		errText = "Не удалось прочитать или проанализировать аудио; файл сохранён"
	}
	raw, _ := json.Marshal(result)
	tx, e := a.db.BeginTx(a.ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	res, e = tx.ExecContext(a.ctx, "UPDATE censorship_items SET state=?,verdict=?,result_json=?,error=?,attempt_token='' WHERE id=? AND attempt_token=? AND state='running'", state, result.Verdict, string(raw), errText, id, token)
	if e != nil {
		return false, e
	}
	n, _ = res.RowsAffected()
	if n > 0 {
		for _, ev := range result.Evidence {
			b, _ := json.Marshal(ev)
			if _, e = tx.ExecContext(a.ctx, "INSERT INTO censorship_evidence(item_id,evidence_json) VALUES(?,?)", id, string(b)); e != nil {
				return false, e
			}
		}
	}
	return true, tx.Commit()
}
