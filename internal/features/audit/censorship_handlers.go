package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"music-manager/internal/core/httputil"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/services/censorcheck"
)

func (m *Module) censorAvailable(w http.ResponseWriter) bool {
	if m.service.censorship == nil {
		httputil.RespondError(w, 503, "censorship audit unavailable")
		return false
	}
	return true
}
func (m *Module) CensorshipRuns(w http.ResponseWriter, r *http.Request) {
	if !m.censorAvailable(w) {
		return
	}
	a := m.service.censorship
	query := r.URL.Query()
	limit := 25
	if raw := query.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			httputil.RespondError(w, 400, "limit must be an integer between 1 and 100")
			return
		}
	}
	runs, next, err := a.Runs(r.Context(), limit, query.Get("cursor"))
	if err != nil {
		if errors.Is(err, errCensorshipCursor) || errors.Is(err, errCensorshipInput) {
			httputil.RespondError(w, 400, err.Error())
			return
		}
		httputil.RespondError(w, 500, "failed to read audit jobs")
		return
	}
	providers := []string{"local"}
	for _, p := range []string{"gemini", "deepgram"} {
		if a.clouds[p] != nil && a.dailySeconds > 0 {
			providers = append(providers, p)
		}
	}
	quota, err := a.quota(r.Context(), providers[1:], time.Now())
	if err != nil {
		httputil.RespondError(w, 500, "failed to read cloud quota")
		return
	}
	var active bool
	if err := a.db.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM censorship_runs WHERE state='running')").Scan(&active); err != nil {
		httputil.RespondError(w, 500, "failed to read audit state")
		return
	}
	body := map[string]any{"runs": runs, "providers": providers, "daily_audio_seconds": a.dailySeconds,
		"next_cursor": next, "quota": quota, "active": active}
	if id := query.Get("run_id"); id != "" {
		run, err := a.Run(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			httputil.RespondError(w, 404, "audit run not found")
			return
		}
		if err != nil {
			httputil.RespondError(w, 500, "failed to read audit run")
			return
		}
		body["selected_run"] = run
	}
	httputil.RespondJSON(w, 200, body)
}
func (m *Module) StartCensorship(w http.ResponseWriter, r *http.Request) {
	if !m.censorAvailable(w) {
		return
	}
	var o CensorshipOptions
	if httputil.DecodeJSON(r, &o) != nil {
		httputil.RespondError(w, 400, "invalid audit request")
		return
	}
	id, e := m.service.censorship.Start(r.Context(), o)
	if errors.Is(e, errCensorshipInput) {
		httputil.RespondError(w, 400, e.Error())
		return
	}
	if e != nil {
		httputil.RespondError(w, 400, "Cannot start audit: check selection, sandbox paths and provider configuration")
		return
	}
	httputil.RespondJSON(w, 202, map[string]string{"id": id})
}
func (m *Module) ControlCensorship(w http.ResponseWriter, r *http.Request) {
	if !m.censorAvailable(w) {
		return
	}
	e := m.service.censorship.Control(r.Context(), chi.URLParam(r, "runID"), chi.URLParam(r, "action"))
	if e != nil {
		code := 409
		if errors.Is(e, sql.ErrNoRows) {
			code = 404
		}
		httputil.RespondError(w, code, "audit action unavailable")
		return
	}
	httputil.RespondJSON(w, 200, map[string]bool{"ok": true})
}
func (m *Module) CensorshipItems(w http.ResponseWriter, r *http.Request) {
	if !m.censorAvailable(w) {
		return
	}
	items, e := m.service.censorship.Items(r.Context(), chi.URLParam(r, "runID"), r.URL.Query().Get("verdict"), httputil.QueryInt(r, "limit", 100), httputil.QueryInt(r, "offset", 0))
	if e != nil {
		httputil.RespondError(w, 500, "failed to read audit results")
		return
	}
	httputil.RespondJSON(w, 200, map[string]any{"items": items})
}
func (m *Module) DecideCensorship(w http.ResponseWriter, r *http.Request) {
	if !m.censorAvailable(w) {
		return
	}
	id, e := strconv.ParseInt(chi.URLParam(r, "itemID"), 10, 64)
	if e != nil {
		httputil.RespondError(w, 400, "invalid item id")
		return
	}
	var body struct {
		Decision *string `json:"decision"`
		Note     *string `json:"note"`
	}
	if httputil.DecodeJSON(r, &body) != nil {
		httputil.RespondError(w, 400, "invalid decision")
		return
	}
	if e = m.service.censorship.saveDecision(r.Context(), id, body.Decision, body.Note); e != nil {
		switch {
		case errors.Is(e, sql.ErrNoRows):
			httputil.RespondError(w, 404, "audit item not found")
		case errors.Is(e, ErrAuditState):
			httputil.RespondError(w, 409, "Для решения нужен завершённый результат с аудиохэшем")
		case errors.Is(e, errCensorshipInput):
			httputil.RespondError(w, 400, "invalid decision")
		default:
			httputil.RespondError(w, 500, "failed to save decision")
		}
		return
	}
	httputil.RespondJSON(w, 200, map[string]bool{"ok": true})
}
func (m *Module) CensorshipAudio(w http.ResponseWriter, r *http.Request) {
	if !m.censorAvailable(w) {
		return
	}
	id, e := strconv.ParseInt(chi.URLParam(r, "itemID"), 10, 64)
	if e != nil {
		httputil.RespondError(w, 400, "invalid item id")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	a := m.service.censorship
	ctx := r.Context()
	ref := r.URL.Query().Get("reference") == "true"
	path, hash, e := a.audioBinding(ctx, id, ref)
	if e != nil {
		respondCensorshipAudioError(w, e)
		return
	}
	f, info, e := a.verifiedAudio(ctx, path, hash)
	if e != nil {
		respondCensorshipAudioError(w, e)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "audio/mpeg")
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func (a *CensorshipAudit) audioBinding(ctx context.Context, id int64, reference bool) (string, string, error) {
	var target, ref, raw string
	err := a.db.QueryRowContext(ctx, `
		SELECT coalesce(current_file_path,file_path),coalesce(current_reference_path,reference_path),result_json
		FROM censorship_items WHERE id=? AND state='done'
	`, id).Scan(&target, &ref, &raw)
	if err != nil {
		return "", "", err
	}
	var result CensorshipResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return "", "", err
	}
	if reference {
		return ref, result.ReferenceAudioHash, nil
	}
	return target, result.AudioHash, nil
}

func (a *CensorshipAudit) verifiedAudio(ctx context.Context, path, hash string) (*os.File, os.FileInfo, error) {
	if path == "" || hash == "" {
		return nil, nil, errCensorshipAudio
	}
	f, err := censorcheck.OpenAudio(a.sb, path)
	if err != nil {
		return nil, nil, errCensorshipAudio
	}
	before, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, errCensorshipAudio
	}
	actual, digestErr := censorcheck.AudioDigest(ctx, f)
	info, statErr := f.Stat()
	if digestErr != nil || statErr != nil || actual != hash || info.Size() != before.Size() || !info.ModTime().Equal(before.ModTime()) {
		f.Close()
		return nil, nil, errCensorshipAudio
	}
	return f, info, nil
}

func respondCensorshipAudioError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		httputil.RespondError(w, 404, "audit item not found")
	case errors.Is(err, errCensorshipInput):
		httputil.RespondError(w, 400, "Укажите относительный путь к MP3 в медиатеке")
	case errors.Is(err, errCensorshipAudio):
		httputil.RespondError(w, 409, errCensorshipAudio.Error())
	default:
		httputil.RespondError(w, 500, "failed to read audit evidence")
	}
}

func (m *Module) RelinkCensorshipAudio(w http.ResponseWriter, r *http.Request) {
	if !m.censorAvailable(w) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "itemID"), 10, 64)
	var body struct {
		Role string `json:"role"`
		Path string `json:"path"`
	}
	if err != nil || httputil.DecodeJSON(r, &body) != nil || (body.Role != "target" && body.Role != "reference") {
		respondCensorshipAudioError(w, errCensorshipInput)
		return
	}
	if err := m.service.censorship.relinkAudio(r.Context(), id, body.Role == "reference", body.Path); err != nil {
		respondCensorshipAudioError(w, err)
		return
	}
	httputil.RespondJSON(w, 200, map[string]bool{"ok": true})
}

func (a *CensorshipAudit) relinkAudio(ctx context.Context, id int64, reference bool, path string) error {
	if filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".mp3") {
		return errCensorshipInput
	}
	full, err := a.sb.Resolve(path, false, sandbox.LiteralPath)
	if err != nil {
		return errCensorshipInput
	}
	rel, err := a.sb.ToRelative(full)
	if err != nil {
		return errCensorshipInput
	}
	previous, hash, err := a.audioBinding(ctx, id, reference)
	if err != nil {
		return err
	}
	f, _, err := a.verifiedAudio(ctx, rel, hash)
	if err != nil {
		return err
	}
	defer f.Close()
	pathColumn, originalColumn, hashField := "current_file_path", "file_path", "$.audio_hash"
	if reference {
		pathColumn, originalColumn, hashField = "current_reference_path", "reference_path", "$.reference_audio_hash"
	}
	res, err := a.db.ExecContext(ctx, `
		UPDATE censorship_items SET `+pathColumn+`=?
		WHERE id=? AND state='done' AND json_extract(result_json,?)=?
		AND coalesce(`+pathColumn+`,`+originalColumn+`)=?
	`, rel, id, hashField, hash, previous)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errCensorshipAudio
	}
	return nil
}

func (m *Module) registerCensorshipRoutes(r chi.Router) {
	r.Get("/censorship", m.CensorshipRuns)
	r.Post("/censorship", m.StartCensorship)
	r.Get("/censorship/{runID}/items", m.CensorshipItems)
	r.Post("/censorship/{runID}/{action}", m.ControlCensorship)
	r.Post("/censorship-items/{itemID}/decision", m.DecideCensorship)
	r.Post("/censorship-items/{itemID}/relink", m.RelinkCensorshipAudio)
	r.Get("/censorship-items/{itemID}/audio", m.CensorshipAudio)
	r.Head("/censorship-items/{itemID}/audio", m.CensorshipAudio)
}
