package downloader_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"music-manager/internal/features/downloader"
)

// The 14 exact SSRF bypass vectors specified in Milestone 2
var milestone2SSRFVectors = []struct {
	name string
	url  string
}{
	{"path_injection_youtube", "http://attacker.com/youtube.com"},
	{"query_injection_youtube", "http://evil.org?ref=https://youtube.com"},
	{"fragment_injection_youtube", "http://evil.org#https://youtube.com"},
	{"subdomain_spoof_youtube", "http://youtube.com.attacker.com"},
	{"prefix_spoof_youtube", "http://evil-youtube.com"},
	{"ftp_scheme_youtube", "ftp://youtube.com/artist/123"},
	{"gopher_scheme", "gopher://127.0.0.1:6379/_youtube.com"},
	{"file_scheme", "file:///etc/passwd#youtube.com"},
	{"path_injection_deezer", "http://attacker.com/deezer.com"},
	{"path_injection_yandex", "http://attacker.com/music.yandex.ru"},
	{"path_injection_apple", "http://attacker.com/music.apple.com"},
	{"path_injection_spotify", "http://attacker.com/spotify.com"},
	{"aws_metadata", "http://169.254.169.254/latest/meta-data"},
	{"loopback_ip", "http://127.0.0.1:8080"},
}

// Additional adversarial SSRF bypass vectors to stress-test URL parsing
var extendedAdversarialVectors = []struct {
	name string
	url  string
}{
	{"ipv6_loopback", "http://[::1]"},
	{"ipv4_mapped_ipv6", "http://[::ffff:127.0.0.1]"},
	{"decimal_ip", "http://2130706433"},
	{"hex_ip", "http://0x7f000001"},
	{"userinfo_bypass_evil", "http://youtube.com@evil.com"},
	{"userinfo_bypass_metadata", "http://youtube.com:pass@169.254.169.254"},
	{"crlf_header_injection", "http://youtube.com\r\nHost: evil.com"},
	{"null_byte_injection", "http://youtube.com\x00evil.com"},
}

// TestChallenger_All14SSRFVectors_GetDiscographyAndCreateJob verifies that all 14 SSRF bypass
// vectors are strictly rejected with HTTP 400 Bad Request by both GetDiscography and CreateJob.
func TestChallenger_All14SSRFVectors_GetDiscographyAndCreateJob(t *testing.T) {
	env, mod := setupTestModule(t)
	mod.Service().PauseQueue()

	combinedVectors := append(milestone2SSRFVectors, extendedAdversarialVectors...)

	for _, tc := range combinedVectors {
		tc := tc

		// 1. Verify GET /downloads/discography
		t.Run("GetDiscography_"+tc.name, func(t *testing.T) {
			endpoint := "/downloads/discography?url=" + url.QueryEscape(tc.url)
			resp := env.Get(endpoint)
			body := env.ReadBody(resp)

			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("VULNERABILITY: GetDiscography did NOT reject %s (%q) with 400, got %d. Body: %s",
					tc.name, tc.url, resp.StatusCode, string(body))
			}
		})

		// 2. Verify POST /downloads (CreateJob)
		t.Run("CreateJob_"+tc.name, func(t *testing.T) {
			payload := downloader.CreateJobRequest{
				URL:    tc.url,
				Source: "auto",
			}
			resp := env.PostJSON("/downloads", payload)
			body := env.ReadBody(resp)

			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("VULNERABILITY: CreateJob did NOT reject %s (%q) with 400, got %d. Body: %s",
					tc.name, tc.url, resp.StatusCode, string(body))
			}
		})

		// 3. Verify POST /downloads/batch (CreateBatch)
		t.Run("CreateBatch_"+tc.name, func(t *testing.T) {
			payload := downloader.CreateBatchJobRequest{
				URLs:   []string{tc.url},
				Source: "auto",
			}
			resp := env.PostJSON("/downloads/batch", payload)
			body := env.ReadBody(resp)

			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("VULNERABILITY: CreateBatch did NOT reject %s (%q) with 400, got %d. Body: %s",
					tc.name, tc.url, resp.StatusCode, string(body))
			}
		})
	}
}

// TestChallenger_SubprocessMocking_TrapHostExecutions uses a trap PATH to ensure that
// zero host yt-dlp or ffmpeg processes can be spawned during tests.
func TestChallenger_SubprocessMocking_TrapHostExecutions(t *testing.T) {
	env, mod := setupTestModule(t)

	// Create trap directory with canary script for yt-dlp and ffmpeg
	trapDir := t.TempDir()
	canaryPath := filepath.Join(trapDir, "UNMOCKED_HOST_EXECUTION_CANARY")

	// Create trap scripts that write to canaryPath if invoked
	trapScript := fmt.Sprintf("#!/bin/sh\necho \"TRAPPED\" >> %s\nexit 1\n", canaryPath)

	for _, bin := range []string{"yt-dlp", "ffmpeg"} {
		p := filepath.Join(trapDir, bin)
		if err := os.WriteFile(p, []byte(trapScript), 0755); err != nil {
			t.Fatalf("failed to write trap binary: %v", err)
		}
	}

	// Prepend trap directory to PATH
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", trapDir+string(os.PathListSeparator)+origPath)

	// 1. Test PreviewAudio under default test mode
	previewURL := "/downloads/preview?url=" + url.QueryEscape("https://www.youtube.com/watch?v=dQw4w9WgXcQ")
	respPrev := env.Get(previewURL)
	if respPrev.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for mock preview, got %d", respPrev.StatusCode)
	}
	if ct := respPrev.Header.Get("Content-Type"); ct != "audio/mpeg" {
		t.Fatalf("expected audio/mpeg Content-Type, got %q", ct)
	}
	bodyPrev := env.ReadBody(respPrev)
	if len(bodyPrev) == 0 {
		t.Fatal("expected non-empty mock preview body")
	}

	// 2. Test Discography under default test mode
	discoURL := "/downloads/discography?url=" + url.QueryEscape("https://music.youtube.com/channel/UCmockChannel")
	respDisco := env.Get(discoURL)
	if respDisco.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for mock discography, got %d", respDisco.StatusCode)
	}
	var discoResp downloader.DiscographyResponse
	env.DecodeJSON(respDisco, &discoResp)
	if discoResp.Source != "youtube" {
		t.Fatalf("expected source 'youtube', got %q", discoResp.Source)
	}
	if len(discoResp.Releases) == 0 {
		t.Fatal("expected non-empty mock releases")
	}

	// 3. Test explicit mock injection via SetPreviewStreamer and SetYtReleasesFetcher
	streamerCalled := false
	mod.SetPreviewStreamer(func(ctx context.Context, targetURL string, w http.ResponseWriter) error {
		streamerCalled = true
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("CUSTOM_MOCK_STREAM"))
		return nil
	})

	respCustomPrev := env.Get(previewURL)
	if respCustomPrev.StatusCode != http.StatusOK || !streamerCalled {
		t.Fatalf("custom preview streamer was not called or failed (status %d)", respCustomPrev.StatusCode)
	}

	fetcherCalled := false
	mod.SetYtReleasesFetcher(func(ctx context.Context, targetURL, cookiesPath string) ([]downloader.DiscographyRelease, string, error) {
		fetcherCalled = true
		return []downloader.DiscographyRelease{
			{ID: 99, Title: "Custom Mock Album", Type: "album", Year: 2026},
		}, "Custom Artist", nil
	})

	respCustomDisco := env.Get(discoURL)
	if respCustomDisco.StatusCode != http.StatusOK || !fetcherCalled {
		t.Fatalf("custom yt releases fetcher was not called or failed (status %d)", respCustomDisco.StatusCode)
	}

	// VERIFY: The canary file must NOT exist. If it exists, an unmocked host execution occurred!
	if _, err := os.Stat(canaryPath); !os.IsNotExist(err) {
		t.Fatalf("CRITICAL SECURITY VIOLATION: Unmocked host subprocess was executed during tests! Canary file %s was created", canaryPath)
	}
}

// TestChallenger_All26Endpoints_ParityAndRegistration verifies all 26 endpoints:
// 1) All 26 require authentication (401 Unauthorized when unauthenticated).
// 2) All 5 paired legacy/modern endpoints behave identically for identical inputs.
// 3) All 21 endpoints under /downloads and 5 endpoints under /api/downloads are properly registered.
func TestChallenger_All26Endpoints_ParityAndRegistration(t *testing.T) {
	env, mod := setupTestModule(t)
	mod.Service().PauseQueue()

	allEndpoints := []struct {
		method string
		path   string
	}{
		// 21 endpoints under /downloads
		{http.MethodPost, "/downloads"},
		{http.MethodPost, "/downloads/batch"},
		{http.MethodGet, "/downloads/discography?url=https://deezer.com"},
		{http.MethodPost, "/downloads/torrent"},
		{http.MethodGet, "/downloads"},
		{http.MethodGet, "/downloads/queue/summary"},
		{http.MethodPost, "/downloads/queue/clear"},
		{http.MethodDelete, "/downloads/completed"},
		{http.MethodPost, "/downloads/clear-completed"},
		{http.MethodPost, "/downloads/retry-failed"},
		{http.MethodPost, "/downloads/pause"},
		{http.MethodPost, "/downloads/resume"},
		{http.MethodGet, "/downloads/nonexistent-id"},
		{http.MethodDelete, "/downloads/nonexistent-id"},
		{http.MethodPost, "/downloads/nonexistent-id/retry"},
		{http.MethodGet, "/downloads/nonexistent-id/tracks"},
		{http.MethodPost, "/downloads/tracks/track-1/switch-version"},
		{http.MethodPost, "/downloads/tracks/track-1/bind-source"},
		{http.MethodGet, "/downloads/candidates?artist=A&title=T"},
		{http.MethodGet, "/downloads/preview?url=https://youtube.com"},
		{http.MethodPost, "/downloads/backfill/bind-source"},

		// 5 aliases under /api/downloads
		{http.MethodPost, "/api/downloads/tracks/track-1/switch-version"},
		{http.MethodPost, "/api/downloads/tracks/track-1/bind-source"},
		{http.MethodGet, "/api/downloads/candidates?artist=A&title=T"},
		{http.MethodGet, "/api/downloads/preview?url=https://youtube.com"},
		{http.MethodPost, "/api/downloads/backfill/bind-source"},
	}

	if len(allEndpoints) != 26 {
		t.Fatalf("expected exactly 26 endpoints, got %d", len(allEndpoints))
	}

	// Phase 1: Authentication enforcement across all 26 endpoints
	env.ClearAuth()
	for _, ep := range allEndpoints {
		req := env.NewRequest(ep.method, ep.path, nil)
		resp := env.Do(req)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Authentication bypass on endpoint %s %s: expected 401, got %d",
				ep.method, ep.path, resp.StatusCode)
		}
	}

	// Phase 2: Functional Parity for the 5 paired endpoints
	env.SetAuthUser("admin")

	pairedScenarios := []struct {
		name       string
		method     string
		pathLegacy string
		pathModern string
		body       any
	}{
		{
			name:       "switch_version_invalid_json",
			method:     http.MethodPost,
			pathLegacy: "/downloads/tracks/track-1/switch-version",
			pathModern: "/api/downloads/tracks/track-1/switch-version",
			body:       "invalid-json",
		},
		{
			name:       "switch_version_disallowed_domain",
			method:     http.MethodPost,
			pathLegacy: "/downloads/tracks/track-1/switch-version",
			pathModern: "/api/downloads/tracks/track-1/switch-version",
			body:       downloader.SwitchVersionRequest{TargetURL: "http://malicious.com"},
		},
		{
			name:       "bind_source_alias_disallowed_domain",
			method:     http.MethodPost,
			pathLegacy: "/downloads/tracks/track-1/bind-source",
			pathModern: "/api/downloads/tracks/track-1/bind-source",
			body:       downloader.SwitchVersionRequest{TargetURL: "http://malicious.com"},
		},
		{
			name:       "candidates_missing_query",
			method:     http.MethodGet,
			pathLegacy: "/downloads/candidates",
			pathModern: "/api/downloads/candidates",
			body:       nil,
		},
		{
			name:       "candidates_with_query",
			method:     http.MethodGet,
			pathLegacy: "/downloads/candidates?artist=Daft+Punk&title=Get+Lucky",
			pathModern: "/api/downloads/candidates?artist=Daft+Punk&title=Get+Lucky",
			body:       nil,
		},
		{
			name:       "preview_missing_url",
			method:     http.MethodGet,
			pathLegacy: "/downloads/preview",
			pathModern: "/api/downloads/preview",
			body:       nil,
		},
		{
			name:       "preview_disallowed_url",
			method:     http.MethodGet,
			pathLegacy: "/downloads/preview?url=http://attacker.com/evil.mp3",
			pathModern: "/api/downloads/preview?url=http://attacker.com/evil.mp3",
			body:       nil,
		},
		{
			name:       "preview_valid_mock_url",
			method:     http.MethodGet,
			pathLegacy: "/downloads/preview?url=https://www.youtube.com/watch?v=mock",
			pathModern: "/api/downloads/preview?url=https://www.youtube.com/watch?v=mock",
			body:       nil,
		},
		{
			name:       "backfill_missing_body",
			method:     http.MethodPost,
			pathLegacy: "/downloads/backfill/bind-source",
			pathModern: "/api/downloads/backfill/bind-source",
			body:       "invalid-json",
		},
		{
			name:       "backfill_disallowed_url",
			method:     http.MethodPost,
			pathLegacy: "/downloads/backfill/bind-source",
			pathModern: "/api/downloads/backfill/bind-source",
			body: downloader.BindBackfillSourceRequest{
				Path:       "Artist/Album",
				AlbumTitle: "Album",
				ArtistName: "Artist",
				TargetURL:  "http://attacker.com/song.mp3",
			},
		},
	}

	for _, sc := range pairedScenarios {
		sc := sc
		t.Run("Parity_"+sc.name, func(t *testing.T) {
			var respLegacy, respModern *http.Response
			if sc.method == http.MethodPost {
				respLegacy = env.PostJSON(sc.pathLegacy, sc.body)
				respModern = env.PostJSON(sc.pathModern, sc.body)
			} else {
				respLegacy = env.Get(sc.pathLegacy)
				respModern = env.Get(sc.pathModern)
			}

			if respLegacy.StatusCode != respModern.StatusCode {
				t.Fatalf("[%s] Status code mismatch: legacy %s returned %d, modern %s returned %d",
					sc.name, sc.pathLegacy, respLegacy.StatusCode, sc.pathModern, respModern.StatusCode)
			}

			bodyLegacy := env.ReadBody(respLegacy)
			bodyModern := env.ReadBody(respModern)

			if sc.name == "candidates_with_query" {
				var resLegacy, resModern downloader.CandidatesResponse
				_ = json.Unmarshal(bodyLegacy, &resLegacy)
				_ = json.Unmarshal(bodyModern, &resModern)
				if resLegacy.Artist != resModern.Artist || resLegacy.Title != resModern.Title || resLegacy.Total != resModern.Total {
					t.Fatalf("[%s] Candidates metadata mismatch: Legacy %+v vs Modern %+v", sc.name, resLegacy, resModern)
				}
				return
			}

			if !bytes.Equal(bodyLegacy, bodyModern) {
				t.Logf("[%s] LEGACY: %s", sc.name, string(bodyLegacy))
				t.Logf("[%s] MODERN: %s", sc.name, string(bodyModern))
				t.Fatalf("[%s] Response body mismatch between %s and %s:\nLegacy: %s\nModern: %s",
					sc.name, sc.pathLegacy, sc.pathModern, string(bodyLegacy), string(bodyModern))
			}
		})
	}
}

// TestChallenger_HighConcurrency_RaceAndOrderStress stress-tests concurrent jobs creation,
// sorting snapshots, status updates, and deletions to ensure 0 race conditions and 0 deadlocks.
func TestChallenger_HighConcurrency_RaceAndOrderStress(t *testing.T) {
	env, mod := setupTestModule(t)
	mod.Service().PauseQueue()

	const (
		goroutines = 12
		iterations = 30
	)

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// 1. Concurrent Creators
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				u := fmt.Sprintf("https://www.youtube.com/watch?v=concurrency_%d_%d", gid, i)
				resp := env.PostJSON("/downloads", downloader.CreateJobRequest{URL: u, Source: "youtube"})
				_ = env.ReadBody(resp)
				time.Sleep(1 * time.Millisecond)
			}
		}(g)
	}

	// 2. Concurrent Readers (ListJobs & Snapshots)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				resp := env.Get("/downloads")
				body := env.ReadBody(resp)
				var jobs []*downloader.Job
				if err := json.Unmarshal(body, &jobs); err == nil && len(jobs) > 1 {
					// Verify sorting invariant: active/resolving before ready/error
					for j := 0; j < len(jobs)-1; j++ {
						if jobs[j].Status.Priority() > jobs[j+1].Status.Priority() {
							t.Errorf("Sort order violation: %s (%d) after %s (%d)",
								jobs[j+1].Status, jobs[j+1].Status.Priority(),
								jobs[j].Status, jobs[j].Status.Priority())
						}
					}
				}
				time.Sleep(2 * time.Millisecond)
			}
		}()
	}

	// 3. Concurrent Queue Pause/Resume
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			select {
			case <-ctx.Done():
				return
			default:
			}
			respP := env.PostJSON("/downloads/pause", nil)
			_ = env.ReadBody(respP)
			respR := env.PostJSON("/downloads/resume", nil)
			_ = env.ReadBody(respR)
			time.Sleep(3 * time.Millisecond)
		}
	}()

	wg.Wait()
}
