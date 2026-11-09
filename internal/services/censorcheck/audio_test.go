package censorcheck

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuditSpotsOffsetAndPriority(t *testing.T) {
	lrc := "[offset:1000]\n[00:05]суицид\n[00:30]суицид\n[01:00]суицид\n[01:30]суицид\n[02:00]суицид"
	if got := ExtractSpots(lrc, 3); len(got) != 3 || got[0].TimeSec != 4 || got[0].Category != CategorySuicide {
		t.Fatalf("quick spots: %+v", got)
	}
	all := ExtractAuditSpots(lrc)
	if len(all) != 5 {
		t.Fatalf("audit inherited UI limit: %d", len(all))
	}
	windows := AuditWindows(all, 150)
	if len(windows) != 5 {
		t.Fatalf("windows: %+v", windows)
	}
	merged := AuditWindows([]QuickListenSpot{{TimeSec: 10}, {TimeSec: 12}}, 30)
	if len(merged) != 1 || len(merged[0].Spots) != 2 {
		t.Fatalf("merge: %+v", merged)
	}
}
func TestSignalHypotheses(t *testing.T) {
	silence := make([]byte, SampleRate*2)
	got := InspectPCM(silence, 5)
	if len(got) != 1 || got[0].Kind != "possible_mute" || got[0].Start != 5 {
		t.Fatalf("silence: %+v", got)
	}
	tone := make([]byte, SampleRate*2)
	for i := 0; i < SampleRate; i++ {
		v := int16(10000 * math.Sin(2*math.Pi*1000*float64(i)/SampleRate))
		binary.LittleEndian.PutUint16(tone[i*2:], uint16(v))
	}
	got = InspectPCM(tone, 0)
	if len(got) != 1 || got[0].Kind != "possible_beep" {
		t.Fatalf("beep: %+v", got)
	}
	// A synthesizer produces the same hypothesis; the detector must never call it confirmed.
	if strings.Contains(got[0].Kind, "confirmed") {
		t.Fatal("intent inferred from tone")
	}
}
func TestCloudAdaptersNoPromptLeakOrProfanityFilter(t *testing.T) {
	for _, provider := range []string{"gemini", "deepgram"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if provider == "deepgram" {
					if r.URL.Query().Get("profanity_filter") != "false" {
						t.Error("profanity filtering enabled")
					}
					w.Write([]byte(`{"metadata":{"request_id":"test"},"results":{"channels":[{"alternatives":[{"transcript":"слово"}]}]}}`))
				} else {
					var v map[string]any
					if json.NewDecoder(r.Body).Decode(&v) != nil {
						t.Error("invalid Gemini request")
					}
					raw, _ := json.Marshal(v)
					if strings.Contains(string(raw), "expected_lyrics") {
						t.Error("expected text leaked into prompt")
					}
					w.Write([]byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"слово"}]}}]}`))
				}
			}))
			defer server.Close()
			c, e := NewCloudClient(provider, "test-key", "test-model", server.Client())
			if e != nil {
				t.Fatal(e)
			}
			c.endpoint = server.URL
			tr, e := c.Transcribe(context.Background(), make([]byte, 1600))
			if e != nil || tr.Text != "слово" || tr.Model != "test-model" {
				t.Fatalf("transcript=%+v err=%v", tr, e)
			}
		})
	}
}
func TestCloudQuotaAndRedaction(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429); w.Write([]byte("private-secret")) }))
	defer s.Close()
	c, _ := NewCloudClient("gemini", "private-secret", "test", s.Client())
	c.endpoint = s.URL
	_, e := c.Transcribe(context.Background(), make([]byte, 100))
	if e == nil || strings.Contains(e.Error(), "private-secret") {
		t.Fatalf("unsafe quota error %v", e)
	}
}

func TestAuditWindowsPrioritizeThemesBeforeCloudBudget(t *testing.T) {
	windows := AuditWindows([]QuickListenSpot{{TimeSec: 5, Category: CategoryProfane}, {TimeSec: 60, Category: CategorySuicide}, {TimeSec: 120, Category: CategoryDrugCoAP}}, 160)
	if len(windows) != 3 || windows[0].Spots[0].Category != CategorySuicide || windows[1].Spots[0].Category != CategoryDrugCoAP || windows[2].Spots[0].Category != CategoryProfane {
		t.Fatalf("wrong budget priority: %+v", windows)
	}
}
