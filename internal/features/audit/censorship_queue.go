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

const (
	censorshipAttemptTimeout = 10 * time.Minute
	censorshipLeaseDuration  = 15 * time.Minute
)

// The v1 token starts with a fixed-width Unix deadline so it can be compared
// in SQLite alongside legacy tokens, which have no lease.
func censorshipLeasePrefix(deadline time.Time) string {
	return fmt.Sprintf("v1:%020d:", deadline.Unix())
}

const recoverCensorshipItems = `
	UPDATE censorship_items SET state='queued',attempt_token=''
	WHERE state='running' AND (attempt_token NOT GLOB 'v1:*' OR attempt_token < ?)`

// NewCensorshipAudit resumes expired attempts. Legacy cloud runs lacking a
// lease still require explicit confirmation before another paid attempt.
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
	if _, e = tx.ExecContext(ctx, `
		UPDATE censorship_runs SET state='waiting'
		WHERE provider!='local' AND state='running' AND EXISTS (
		    SELECT 1 FROM censorship_items WHERE run_id=censorship_runs.id
		    AND state='running' AND attempt_token NOT GLOB 'v1:*'
		)`); e != nil {
		cancel()
		return nil, e
	}
	if _, e = tx.ExecContext(ctx, recoverCensorshipItems, censorshipLeasePrefix(time.Now())); e != nil {
		cancel()
		return nil, e
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
	res, e := tx.ExecContext(ctx, `
		UPDATE censorship_runs SET state=?,updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND state!='cancelled' AND (state!='completed' OR ?='retry')`, state, id, action)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		var current string
		if err := tx.QueryRowContext(ctx, "SELECT state FROM censorship_runs WHERE id=?", id).Scan(&current); err != nil {
			return err
		}
		return ErrAuditState
	}
	if action == "pause" || action == "cancel" {
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
	if e = tx.Commit(); e != nil {
		return e
	}
	if action == "pause" || action == "cancel" {
		a.mu.Lock()
		if a.activeRun == id && a.activeCancel != nil {
			a.activeCancel()
		}
		a.mu.Unlock()
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
	now := time.Now()
	deadline := now.Add(censorshipLeaseDuration)
	token := censorshipLeasePrefix(deadline) + uuid.NewString()
	// A single write claims queued or expired work. Another worker can only
	// finalize or release its own token.
	err := a.db.QueryRowContext(a.ctx, `
		UPDATE censorship_items SET state='running',attempt_token=?
		WHERE id=(
		    SELECT i.id FROM censorship_items i
		    JOIN censorship_runs r ON r.id=i.run_id
		    WHERE r.state='running' AND (i.state='queued' OR
		      (i.state='running' AND (i.attempt_token NOT GLOB 'v1:*' OR i.attempt_token < ?)))
		    ORDER BY r.created_at,i.id LIMIT 1
		)
		RETURNING id,run_id,file_path,reference_path,
		          (SELECT provider FROM censorship_runs WHERE id=censorship_items.run_id)`,
		token, censorshipLeasePrefix(now)).Scan(&id, &run, &path, &ref, &provider)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = a.db.ExecContext(a.ctx, `UPDATE censorship_runs SET state='completed',updated_at=CURRENT_TIMESTAMP WHERE state='running' AND NOT EXISTS(SELECT 1 FROM censorship_items WHERE run_id=censorship_runs.id AND state IN ('queued','running'))`)
		return false, err
	}
	if err != nil {
		return false, err
	}

	ctx, cancel := context.WithDeadline(a.ctx, now.Add(censorshipAttemptTimeout))
	a.mu.Lock()
	a.activeRun = run
	a.activeCancel = cancel
	a.mu.Unlock()
	// Pause or cancellation may have committed before activeCancel was set.
	var active int
	err = a.db.QueryRowContext(ctx, `
		SELECT count(*) FROM censorship_items i JOIN censorship_runs r ON r.id=i.run_id
		WHERE i.id=? AND i.attempt_token=? AND i.state='running' AND r.state='running'`, id, token).Scan(&active)
	var result CensorshipResult
	var analysisErr error
	if err == nil && active > 0 {
		result, analysisErr = a.analyze(ctx, path, ref, provider)
	}
	interrupted := ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded)
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	cancel()
	a.mu.Lock()
	a.activeRun = ""
	a.activeCancel = nil
	a.mu.Unlock()

	if err != nil || active == 0 || interrupted {
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(a.ctx), time.Second)
		defer releaseCancel()
		_, releaseErr := a.db.ExecContext(releaseCtx, `
			UPDATE censorship_items SET state='queued',attempt_token=''
			WHERE id=? AND attempt_token=? AND state='running'`, id, token)
		return true, errors.Join(err, releaseErr)
	}
	if timedOut {
		analysisErr = context.DeadlineExceeded
	}
	// Keep retrying the same result without repeating analysis or transcription.
	finishCtx, finishCancel := context.WithTimeout(a.ctx, 8*time.Second)
	defer finishCancel()
	return true, retryCensorshipWrite(finishCtx, func() error {
		return a.finishAttempt(finishCtx, id, run, token, result, analysisErr)
	})
}

func (a *CensorshipAudit) finishAttempt(ctx context.Context, id int64, run, token string, result CensorshipResult, analysisErr error) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if errors.Is(analysisErr, censorcheck.ErrCloudWaiting) {
		res, err := tx.ExecContext(ctx, `
			UPDATE censorship_items SET state='queued',attempt_token='',error=?
			WHERE id=? AND attempt_token=? AND state='running'`, analysisErr.Error(), id, token)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n > 0 {
			if _, err := tx.ExecContext(ctx, "UPDATE censorship_runs SET state='waiting' WHERE id=? AND state='running'", run); err != nil {
				return err
			}
		}
		return tx.Commit()
	}

	state, errText := "done", ""
	if analysisErr != nil {
		state = "error"
		errText = "Не удалось прочитать или проанализировать аудио; файл сохранён"
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE censorship_items SET state=?,verdict=?,result_json=?,error=?,attempt_token=''
		WHERE id=? AND attempt_token=? AND state='running'`, state, result.Verdict, string(raw), errText, id, token)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		for _, ev := range result.Evidence {
			encoded, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO censorship_evidence(item_id,evidence_json) VALUES(?,?)", id, string(encoded)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func retryCensorshipWrite(ctx context.Context, write func() error) error {
	for {
		err := write()
		if err == nil {
			return nil
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
}
