package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/censorcheck"
	"music-manager/internal/services/resolver"
)

func digest(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:", len(p))
		io.WriteString(h, p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (a *CensorshipAudit) readLyrics(path string) (string, error) {
	p, e := a.sb.Resolve(strings.TrimSuffix(path, filepath.Ext(path))+".lrc", false, sandbox.LiteralPath)
	if e != nil {
		return "", e
	}
	f, e := safefs.Open(p)
	if e != nil {
		return "", e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return "", e
	}
	if info.Size() > 4<<20 {
		return "", fmt.Errorf("lyrics exceed audit limit")
	}
	b, e := io.ReadAll(io.LimitReader(f, 4<<20))
	return string(b), e
}

func (a *CensorshipAudit) analyze(ctx context.Context, path, reference, provider string) (CensorshipResult, error) {
	r := CensorshipResult{Verdict: "insufficient_data", Algorithm: censorcheck.AlgorithmVersion, Coverage: "limited", Evidence: []CensorshipEvidence{}, Limitations: []string{"Признаки редактирования не доказывают намеренность; требуется прослушивание"}}
	target, e := censorcheck.OpenAudio(a.sb, path)
	if e != nil {
		return r, e
	}
	defer target.Close()
	audioHash, e := censorcheck.AudioDigest(ctx, target)
	if e != nil {
		return r, e
	}
	lyrics, lyricsErr := a.readLyrics(path)
	if lyricsErr != nil {
		r.Limitations = append(r.Limitations, "Синхронизированный текст недоступен")
	}
	spots := censorcheck.ExtractAuditSpots(lyrics)
	var duration float64
	if len(spots) > 0 {
		duration, e = censorcheck.AudioDuration(ctx, target)
		if e != nil {
			return r, e
		}
	}
	windows := censorcheck.AuditWindows(spots, duration)

	var refFile *os.File
	referenceHash := ""
	referenceState := "absent"
	if reference != "" {
		referenceState = "unused"
	}
	if reference != "" && len(windows) > 0 {
		refFile, e = censorcheck.OpenAudio(a.sb, reference)
		if e != nil {
			return r, e
		}
		defer refFile.Close()
		tags, _ := id3.ReadTagsFromFile(target, "")
		rt, _ := id3.ReadTagsFromFile(refFile, "")
		referenceOK := false
		if tags != nil && rt != nil && tags.Artist != "" && rt.Artist != "" && tags.Title != "" && rt.Title != "" {
			refDuration, err := censorcheck.AudioDuration(ctx, refFile)
			if err != nil {
				return r, err
			}
			ev := resolver.EvaluateTrack(tags.Title, tags.Artist, int(duration), rt.Title, rt.Artist, int(refDuration))
			referenceOK = ev.Compatible && ev.TitleSimilarity >= 0.9 && ev.ArtistSimilarity >= 0.9
		}
		if referenceOK {
			referenceState = "used"
			referenceHash, e = censorcheck.AudioDigest(ctx, refFile)
			if e != nil {
				return r, e
			}
		} else {
			r.Limitations = append(r.Limitations, "Эталон не использован: совместимость записи не подтверждена")
		}
	} else if reference != "" {
		r.Limitations = append(r.Limitations, "Эталон не использован: нет текстовых контрольных точек")
	}
	identity := "local"
	if provider != "local" {
		if a.clouds[provider] == nil {
			return r, censorcheck.ErrCloudWaiting
		}
		identity = a.clouds[provider].Identity()
	}
	intervals, err := json.Marshal(windows)
	if err != nil {
		return r, err
	}
	key := digest("audit-result-v2", audioHash, lyrics, string(intervals), fmt.Sprint(lyricsErr == nil),
		referenceState, referenceHash, identity, censorcheck.AlgorithmVersion)
	var cached string
	e = a.db.QueryRowContext(ctx, "SELECT result_json FROM censorship_cache WHERE cache_key=?", key).Scan(&cached)
	if e == nil {
		var result CensorshipResult
		if json.Unmarshal([]byte(cached), &result) == nil && result.AudioHash == audioHash && result.ReferenceAudioHash == referenceHash {
			_, err := a.db.ExecContext(ctx, "UPDATE censorship_cache SET last_used_at=unixepoch() WHERE cache_key=?", key)
			return result, err
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return r, e
	}
	if len(windows) == 0 {
		r.Coverage = "audio_signals_only"
		r.Limitations = append(r.Limitations, "Нет пригодных текстовых контрольных точек; замены слов могут быть пропущены")
		signals, err := censorcheck.ScanSignals(ctx, target)
		if err != nil {
			return r, err
		}
		for _, s := range signals {
			r.Evidence = append(r.Evidence, CensorshipEvidence{Start: s.Start, End: s.End, Kind: s.Kind, Origin: "local_signal"})
		}
	} else {
		r.Coverage = "selected_lyric_windows"
		r.TotalWindows = len(windows)
		for _, w := range windows {
			pcm, err := censorcheck.DecodeWindow(ctx, target, w)
			if err != nil {
				return r, err
			}
			for _, s := range censorcheck.InspectPCM(pcm, w.Start) {
				r.Evidence = append(r.Evidence, CensorshipEvidence{Start: s.Start, End: s.End, Kind: s.Kind, Origin: "local_signal"})
			}
			if referenceHash != "" {
				refPCM, err := censorcheck.DecodeWindow(ctx, refFile, w)
				if err != nil {
					return r, err
				}
				if alignedDifference(pcm, refPCM) {
					r.Evidence = append(r.Evidence, CensorshipEvidence{Start: w.Start, End: w.End, Kind: "possible_audio_edit", Origin: "reference_comparison"})
				}
			}
			if provider != "local" {
				tr, err := a.transcribe(ctx, provider, pcm)
				if err != nil {
					return r, err
				}
				seen := map[string]bool{}
				for _, s := range w.Spots {
					if seen[s.LineText] {
						continue
					}
					seen[s.LineText] = true
					kind := compareWords(s.LineText, tr.Text)
					r.Evidence = append(r.Evidence, CensorshipEvidence{Start: w.Start, End: w.End, Kind: kind, Expected: s.LineText, Heard: tr.Text, Origin: "transcription_vs_lyrics", Transcript: &tr})
				}
			}
			r.CheckedWindows++
		}
	}
	preserved, unresolved := false, false
	for _, ev := range r.Evidence {
		if strings.HasPrefix(ev.Kind, "possible_") {
			r.Verdict = "suspected"
		}
		if ev.Kind == "words_preserved" {
			preserved = true
		}
		if ev.Kind == "unresolved_transcript" {
			unresolved = true
		}
	}
	if r.Verdict != "suspected" && preserved && !unresolved && provider != "local" {
		r.Verdict = "checked_words_preserved"
	}
	r.Limitations = append(r.Limitations, "Проверенные участки не подтверждают отсутствие цензуры во всём треке")
	sort.SliceStable(r.Evidence, func(i, j int) bool { return r.Evidence[i].Start < r.Evidence[j].Start })
	finalHash, err := censorcheck.AudioDigest(ctx, target)
	if err != nil {
		return r, err
	}
	if finalHash != audioHash {
		return r, fmt.Errorf("audio changed during audit; retry with the current file")
	}
	if referenceHash != "" {
		finalReferenceHash, err := censorcheck.AudioDigest(ctx, refFile)
		if err != nil {
			return r, err
		}
		if finalReferenceHash != referenceHash {
			return r, fmt.Errorf("reference audio changed during audit; retry with the current file")
		}
	}
	r.AudioHash = audioHash
	r.ReferenceAudioHash = referenceHash
	raw, e := json.Marshal(r)
	if e != nil {
		return r, e
	}
	e = retryCensorshipWrite(ctx, func() error {
		_, err := a.db.ExecContext(ctx, "INSERT INTO censorship_cache(cache_key,result_json,last_used_at) VALUES(?,?,unixepoch()) ON CONFLICT(cache_key) DO UPDATE SET result_json=excluded.result_json,last_used_at=excluded.last_used_at", key, string(raw))
		return err
	})
	return r, e
}

func compareWords(expected, heard string) string {
	norm := func(s string) string { return strings.ToLower(resolver.CleanQueryTextFull(s)) }
	e, h := norm(expected), norm(heard)
	if e == "" || h == "" {
		return "unresolved_transcript"
	}
	if strings.Contains(" "+h+" ", " "+e+" ") {
		return "words_preserved"
	}
	ew, hw := strings.Fields(e), strings.Fields(h)
	set := map[string]bool{}
	for _, w := range hw {
		set[w] = true
	}
	matched := 0
	for _, w := range ew {
		if set[w] {
			matched++
		}
	}
	if len(ew) >= 4 && float64(matched)/float64(len(ew)) >= 0.6 {
		return "possible_word_change"
	}
	return "unresolved_transcript"
}

func (a *CensorshipAudit) transcribe(ctx context.Context, provider string, pcm []byte) (censorcheck.Transcript, error) {
	cloud := a.clouds[provider]
	if cloud == nil {
		return censorcheck.Transcript{}, censorcheck.ErrCloudWaiting
	}
	h := sha256.Sum256(pcm)
	audio := hex.EncodeToString(h[:])
	identity := cloud.Identity()
	key := digest("transcript-v1", audio, identity)
	legacyKey := digest(audio, identity, "censorship-v1")
	var raw sql.NullString
	e := a.db.QueryRowContext(ctx, "SELECT transcript_json FROM censorship_transcripts WHERE cache_key=?", key).Scan(&raw)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return censorcheck.Transcript{}, e
	}
	if t, ok := usableTranscript(raw.String, identity); ok {
		return t, nil
	}
	var legacy string
	e = a.db.QueryRowContext(ctx, "SELECT transcript_json FROM censorship_transcripts WHERE cache_key=?", legacyKey).Scan(&legacy)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return censorcheck.Transcript{}, e
	}
	if t, ok := usableTranscript(legacy, identity); ok {
		return t, a.saveTranscript(ctx, key, legacy, raw)
	}
	var t censorcheck.Transcript
	seconds := int(math.Ceil(float64(len(pcm)) / (censorcheck.SampleRate * 2)))
	// Reserve usage BEFORE sending. Failed requests retain their reservation;
	// restarting cannot repeat potentially billed work without consuming a limit.
	res, e := a.db.ExecContext(ctx, `INSERT INTO censorship_cloud_usage(provider,day,seconds) SELECT ?,date('now'),? WHERE ?<=? ON CONFLICT(provider,day) DO UPDATE SET seconds=seconds+excluded.seconds WHERE seconds+excluded.seconds<=?`, provider, seconds, seconds, a.dailySeconds, a.dailySeconds)
	if e != nil {
		return t, e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return t, e
	}
	if n == 0 {
		return t, fmt.Errorf("%w: local daily audio limit reached", censorcheck.ErrCloudWaiting)
	}
	t, e = cloud.Transcribe(ctx, pcm)
	if e != nil {
		return t, e
	}
	b, e := json.Marshal(t)
	if e != nil {
		return t, e
	}
	// The provider has already charged this attempt. Preserve its response
	// even if this item was paused immediately after the provider returned.
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	defer cancel()
	return t, a.saveTranscript(storeCtx, key, string(b), raw)
}

func usableTranscript(raw, identity string) (censorcheck.Transcript, bool) {
	var t censorcheck.Transcript
	var fields struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal([]byte(raw), &t) != nil || json.Unmarshal([]byte(raw), &fields) != nil ||
		fields.Text == nil || t.Provider == "" || t.Model == "" || t.Settings != identity {
		return t, false
	}
	// Production identities encode provider and model ahead of the settings.
	if provider, model, ok := strings.Cut(identity, ":"); ok {
		if model, _, ok := strings.Cut(model, ":"); ok && (t.Provider != provider || t.Model != model) {
			return t, false
		}
	}
	return t, true
}

func (a *CensorshipAudit) saveTranscript(ctx context.Context, key, raw string, previous sql.NullString) error {
	return retryCensorshipWrite(ctx, func() error {
		_, err := a.db.ExecContext(ctx, `
			INSERT INTO censorship_transcripts(cache_key,transcript_json) VALUES(?,?)
			ON CONFLICT(cache_key) DO UPDATE SET transcript_json=excluded.transcript_json
			WHERE censorship_transcripts.transcript_json=?
		`, key, raw, previous)
		return err
	})
}

// Compare gain-normalized 100ms envelopes with up to one second alignment.
// Only well-aligned context with a localized discrepancy yields a hypothesis.
func alignedDifference(a, b []byte) bool {
	envelope := func(p []byte) []float64 {
		out := []float64{}
		for i := 0; i+1600 <= len(p); i += 1600 {
			sum := 0.0
			for j := i; j < i+1600; j += 2 {
				v := float64(int16(uint16(p[j]) | uint16(p[j+1])<<8))
				sum += v * v
			}
			out = append(out, math.Sqrt(sum/800))
		}
		return out
	}
	x, y := envelope(a), envelope(b)
	if len(x) < 30 || len(y) < 30 {
		return false
	}
	best, bestShift := 0.0, 0
	for shift := -10; shift <= 10; shift++ {
		dot, xx, yy := 0.0, 0.0, 0.0
		for i, v := range x {
			j := i + shift
			if j < 0 || j >= len(y) {
				continue
			}
			dot += v * y[j]
			xx += v * v
			yy += y[j] * y[j]
		}
		if xx > 0 && yy > 0 {
			c := dot / math.Sqrt(xx*yy)
			if c > best {
				best = c
				bestShift = shift
			}
		}
	}
	if best < 0.85 {
		return false
	}
	sumX, sumY := 0.0, 0.0
	for i, v := range x {
		j := i + bestShift
		if j >= 0 && j < len(y) {
			sumX += v
			sumY += y[j]
		}
	}
	if sumX == 0 || sumY == 0 {
		return false
	}
	gain := sumX / sumY
	diff, n := 0, 0
	for i, v := range x {
		j := i + bestShift
		if j < 0 || j >= len(y) {
			continue
		}
		n++
		if math.Abs(v-y[j]*gain) > math.Max(sumX/float64(len(x)), v)*0.65 {
			diff++
		}
	}
	return diff >= 3 && diff*3 < n
}
