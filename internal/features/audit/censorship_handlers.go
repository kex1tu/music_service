package audit

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"music-manager/internal/core/httputil"
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
	runs, e := m.service.censorship.Runs(r.Context())
	if e != nil {
		httputil.RespondError(w, 500, "failed to read audit jobs")
		return
	}
	providers := []string{"local"}
	for _, p := range []string{"gemini", "deepgram"} {
		if m.service.censorship.clouds[p] != nil && m.service.censorship.dailySeconds > 0 {
			providers = append(providers, p)
		}
	}
	httputil.RespondJSON(w, 200, map[string]any{"runs": runs, "providers": providers, "daily_audio_seconds": m.service.censorship.dailySeconds})
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
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if httputil.DecodeJSON(r, &body) != nil {
		httputil.RespondError(w, 400, "invalid decision")
		return
	}
	if e = m.service.censorship.Decide(r.Context(), id, body.Decision, body.Note); e != nil {
		httputil.RespondError(w, 400, "decision unavailable")
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
	var path, reference, raw string
	e = m.service.censorship.db.QueryRowContext(r.Context(), "SELECT file_path,reference_path,result_json FROM censorship_items WHERE id=? AND state='done'", id).Scan(&path, &reference, &raw)
	if errors.Is(e, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	var result CensorshipResult
	if e != nil || json.Unmarshal([]byte(raw), &result) != nil {
		httputil.RespondError(w, http.StatusInternalServerError, "failed to read audit evidence")
		return
	}
	expected := result.AudioHash
	if r.URL.Query().Get("reference") == "true" {
		path, expected = reference, result.ReferenceAudioHash
	}
	if path == "" || expected == "" {
		httputil.RespondError(w, http.StatusConflict, "Аудиозапись не привязана к этому результату")
		return
	}
	f, e := censorcheck.OpenAudio(m.service.censorship.sb, path)
	if e != nil {
		httputil.RespondError(w, http.StatusConflict, "Аудиозапись этого результата недоступна по сохранённому пути")
		return
	}
	defer f.Close()
	before, e := f.Stat()
	if e != nil {
		httputil.RespondError(w, http.StatusConflict, "Не удалось проверить аудиозапись этого результата")
		return
	}
	actual, digestErr := censorcheck.AudioDigest(r.Context(), f)
	info, statErr := f.Stat()
	if digestErr != nil || statErr != nil || actual != expected || info.Size() != before.Size() || !info.ModTime().Equal(before.ModTime()) {
		httputil.RespondError(w, http.StatusConflict, "Аудиозапись изменилась или недоступна для проверки")
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
func (m *Module) registerCensorshipRoutes(r chi.Router) {
	r.Get("/censorship", m.CensorshipRuns)
	r.Post("/censorship", m.StartCensorship)
	r.Get("/censorship/{runID}/items", m.CensorshipItems)
	r.Post("/censorship/{runID}/{action}", m.ControlCensorship)
	r.Post("/censorship-items/{itemID}/decision", m.DecideCensorship)
	r.Get("/censorship-items/{itemID}/audio", m.CensorshipAudio)
	r.Head("/censorship-items/{itemID}/audio", m.CensorshipAudio)
}
