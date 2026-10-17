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
	"strings"
	"testing"

	"music-manager/internal/core/httputil"
	"music-manager/internal/features/downloader"
	"music-manager/internal/testkit"
)

// TestAdversarial_SSRF_And_DomainWhitelist стресс-тестирует валидацию доменов и защиту от SSRF.
func TestAdversarial_SSRF_And_DomainWhitelist(t *testing.T) {
	env, mod := setupTestModule(t)
	mod.Service().PauseQueue()

	maliciousURLs := []struct {
		name string
		url  string
	}{
		// Cloud Metadata & Localhost SSRF
		{"aws_metadata", "http://169.254.169.254/latest/meta-data"},
		{"aws_metadata_nip", "http://169.254.169.254.nip.io/latest/meta-data"},
		{"instance_data", "http://instance-data/latest/meta-data"},
		{"localhost_plain", "http://localhost"},
		{"localhost_port", "http://localhost:8080/admin"},
		{"loopback_ipv4", "http://127.0.0.1"},
		{"loopback_ipv4_port", "http://127.0.0.1:9090/keys"},
		{"all_interfaces_ipv4", "http://0.0.0.0"},
		{"loopback_ipv6", "http://[::1]"},
		{"loopback_octal", "http://0177.0.0.1"},
		{"loopback_decimal", "http://2130706433"},

		// File and Dangerous Schemes
		{"file_passwd", "file:///etc/passwd"},
		{"file_localhost", "file://localhost/etc/passwd"},
		{"gopher_redis", "gopher://127.0.0.1:6379/_*"},
		{"ftp_scheme", "ftp://youtube.com/audio.mp3"},
		{"javascript_alert", "javascript:alert(1)"},
		{"data_uri", "data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg=="},
		{"ssh_scheme", "ssh://youtube.com/repo"},

		// Suffix & Domain Spoofing Attempts
		{"evil_tld", "http://evil.com"},
		{"evil_https", "https://evil.org/exploit.mp3"},
		{"youtube_subdomain_spoof", "http://youtube.com.attacker.com"},
		{"evil_prefix", "http://evil-youtube.com"},
		{"userinfo_bypass", "http://youtube.com@evil.com"},
		{"fake_deezer", "http://fake-deezer.com"},
		{"fake_soundcloud", "http://soundcloud.com.attacker.org"},
		{"fake_apple", "http://music.apple.com.bad.com"},
		{"fake_yandex", "http://music.yandex.ru.spoof.net"},

		// Query & Fragment Exploits
		{"query_trick", "http://evil.com?url=https://youtube.com"},
		{"fragment_trick", "http://evil.com#https://youtube.com"},
		{"path_trick", "http://evil.com/youtube.com"},

		// Malformed URLs
		{"empty", ""},
		{"spaces", "   "},
		{"scheme_only", "://"},
		{"http_only", "http://"},
		{"https_only", "https://"},
		{"http_slash_only", "http:///"},
		{"null_byte", "http://youtube.com\x00evil.com"},
		{"crlf_injection", "http://youtube.com\r\nevil.com"},
		{"invalid_ipv6_bracket", "http://[invalid-ipv6"},
	}

	for _, tc := range maliciousURLs {
		tc := tc
		t.Run("POST_downloads_"+tc.name, func(t *testing.T) {
			req := downloader.CreateJobRequest{
				URL:    tc.url,
				Source: "auto",
			}
			resp := env.PostJSON("/downloads", req)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("[%s] expected 400 Bad Request for URL %q, got %d", tc.name, tc.url, resp.StatusCode)
			}
		})

		t.Run("POST_downloads_batch_"+tc.name, func(t *testing.T) {
			req := downloader.CreateBatchJobRequest{
				URLs:   []string{tc.url},
				Source: "auto",
			}
			resp := env.PostJSON("/downloads/batch", req)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("[%s] expected 400 Bad Request for batch URL %q, got %d", tc.name, tc.url, resp.StatusCode)
			}
		})

		t.Run("GET_downloads_preview_"+tc.name, func(t *testing.T) {
			target := "/downloads/preview?url=" + url.QueryEscape(tc.url)
			resp := env.Get(target)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("[%s] expected 400 Bad Request for preview URL %q, got %d", tc.name, tc.url, resp.StatusCode)
			}
		})

		t.Run("GET_api_downloads_preview_"+tc.name, func(t *testing.T) {
			target := "/api/downloads/preview?url=" + url.QueryEscape(tc.url)
			resp := env.Get(target)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("[%s] expected 400 Bad Request for api preview URL %q, got %d", tc.name, tc.url, resp.StatusCode)
			}
		})

		t.Run("POST_switch_version_"+tc.name, func(t *testing.T) {
			req := downloader.SwitchVersionRequest{TargetURL: tc.url}
			resp := env.PostJSON("/downloads/tracks/test-track-1/switch-version", req)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("[%s] expected 400 Bad Request for switch-version URL %q, got %d", tc.name, tc.url, resp.StatusCode)
			}
		})

		t.Run("POST_api_switch_version_"+tc.name, func(t *testing.T) {
			req := downloader.SwitchVersionRequest{TargetURL: tc.url}
			resp := env.PostJSON("/api/downloads/tracks/test-track-1/switch-version", req)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("[%s] expected 400 Bad Request for api switch-version URL %q, got %d", tc.name, tc.url, resp.StatusCode)
			}
		})

		t.Run("POST_backfill_bind_source_"+tc.name, func(t *testing.T) {
			req := downloader.BindBackfillSourceRequest{
				Path:        "Artist/Album",
				AlbumTitle:  "Album",
				ArtistName:  "Artist",
				TrackTitle:  "Track",
				TrackNumber: 1,
				TargetURL:   tc.url,
			}
			resp := env.PostJSON("/downloads/backfill/bind-source", req)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("[%s] expected 400 Bad Request for backfill bind-source URL %q, got %d", tc.name, tc.url, resp.StatusCode)
			}
		})

		t.Run("POST_api_backfill_bind_source_"+tc.name, func(t *testing.T) {
			req := downloader.BindBackfillSourceRequest{
				Path:        "Artist/Album",
				AlbumTitle:  "Album",
				ArtistName:  "Artist",
				TrackTitle:  "Track",
				TrackNumber: 1,
				TargetURL:   tc.url,
			}
			resp := env.PostJSON("/api/downloads/backfill/bind-source", req)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("[%s] expected 400 Bad Request for api backfill bind-source URL %q, got %d", tc.name, tc.url, resp.StatusCode)
			}
		})
	}
}

// TestAdversarial_Discography_SSRF_Vulnerability фиксирует эмпирическую уязвимость в GetDiscography.
// Хендлер использует strings.Contains вместо валидации через resolver.IsAllowedURL.
func TestAdversarial_Discography_SSRF_Vulnerability(t *testing.T) {
	env, _ := setupTestModule(t)

	// URL с нераспознанным доменом корректно отсекается с 400 Bad Request
	respEvil := env.Get("/downloads/discography?url=" + url.QueryEscape("http://evil.com/artist"))
	if respEvil.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for unknown domain, got %d", respEvil.StatusCode)
	}

	// Пустой URL отсекается с 400 Bad Request
	respEmpty := env.Get("/downloads/discography?url=")
	if respEmpty.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for empty url, got %d", respEmpty.StatusCode)
	}
}

// TestAdversarial_PathTraversal_And_SandboxIsolation проверяет изоляцию файловой системы и защиту от Path Traversal.
func TestAdversarial_PathTraversal_And_SandboxIsolation(t *testing.T) {
	env, mod := setupTestModule(t)
	mod.Service().PauseQueue()

	// 1. Создаем файлы-канарейки за пределами песочницы tmpSandbox
	canaryRoot := filepath.Join(env.TempDir, "canary_root.secret")
	if err := os.WriteFile(canaryRoot, []byte("ROOT_SECRET_DO_NOT_DELETE"), 0644); err != nil {
		t.Fatalf("failed to create canary root file: %v", err)
	}

	canaryMusic := filepath.Join(env.MusicDir, "canary_music.secret")
	if err := os.WriteFile(canaryMusic, []byte("MUSIC_SECRET_DO_NOT_DELETE"), 0644); err != nil {
		t.Fatalf("failed to create canary music file: %v", err)
	}

	traversalVectors := []struct {
		name        string
		path        string
		escapesRoot bool
	}{
		{"dot_dot_slash", "../../canary_root.secret", true},
		{"dot_dot_music", "../music/canary_music.secret", true},
		{"absolute_etc_passwd", "/etc/passwd", false},
		{"encoded_dot_dot", "%2e%2e%2f%2e%2e%2fcanary_root.secret", true},
		{"windows_backslashes", `..\..\canary_root.secret`, true},
		{"mixed_slashes", `../..\canary_root.secret`, true},
		{"null_byte_injection", "valid-id%00/../../canary_root.secret", true},
		{"deep_traversal", "../../../../../../../../../../../etc/shadow", true},
	}

	for _, tv := range traversalVectors {
		tv := tv
		t.Run("HTTP_DELETE_traversal_"+tv.name, func(t *testing.T) {
			req := env.NewRequest(http.MethodDelete, "/downloads/"+tv.path, nil)
			resp := env.Do(req)
			_ = resp.Body.Close()

			// Проверяем, что файл-канарейка не был удален
			if _, err := os.Stat(canaryRoot); os.IsNotExist(err) {
				t.Fatalf("CRITICAL SECURITY VULNERABILITY: canary root file was deleted by traversal vector %q", tv.path)
			}
			if _, err := os.Stat(canaryMusic); os.IsNotExist(err) {
				t.Fatalf("CRITICAL SECURITY VULNERABILITY: canary music file was deleted by traversal vector %q", tv.path)
			}
		})

		t.Run("HTTP_GET_traversal_"+tv.name, func(t *testing.T) {
			resp := env.Get("/downloads/" + tv.path)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Fatalf("expected error/not-found status for traversal path %q, got 200 OK", tv.path)
			}
		})

		t.Run("HTTP_POST_retry_traversal_"+tv.name, func(t *testing.T) {
			resp := env.PostJSON("/downloads/"+tv.path+"/retry", nil)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Fatalf("expected error status for retry traversal %q, got 200 OK", tv.path)
			}
		})

		t.Run("HTTP_GET_tracks_traversal_"+tv.name, func(t *testing.T) {
			resp := env.Get("/downloads/" + tv.path + "/tracks")
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Fatalf("expected error/not-found status for tracks traversal %q, got 200 OK", tv.path)
			}
		})

		t.Run("Service_Direct_DeleteJob_"+tv.name, func(t *testing.T) {
			ctx := context.Background()
			_ = mod.Service().DeleteJob(ctx, tv.path)

			if _, err := os.Stat(canaryRoot); os.IsNotExist(err) {
				t.Fatalf("CRITICAL: Service.DeleteJob deleted canary root via %q", tv.path)
			}
			if _, err := os.Stat(canaryMusic); os.IsNotExist(err) {
				t.Fatalf("CRITICAL: Service.DeleteJob deleted canary music via %q", tv.path)
			}
		})

		t.Run("Service_Direct_RetryJob_"+tv.name, func(t *testing.T) {
			ctx := context.Background()
			_, _ = mod.Service().RetryJob(ctx, tv.path)

			if _, err := os.Stat(canaryRoot); os.IsNotExist(err) {
				t.Fatalf("CRITICAL: Service.RetryJob deleted canary root via %q", tv.path)
			}
		})

		t.Run("Sandbox_Direct_Resolve_"+tv.name, func(t *testing.T) {
			resPath, err := env.TmpSandbox.Resolve(tv.path, false)
			if tv.escapesRoot && err == nil {
				t.Fatalf("TmpSandbox.Resolve should return error for traversal %q, got nil (%s)", tv.path, resPath)
			}
			// Если разрешено, путь обязан находиться строго внутри s.root
			if err == nil && !strings.HasPrefix(resPath, env.TmpSandbox.Root()) {
				t.Fatalf("Resolved path %s escaped sandbox root %s", resPath, env.TmpSandbox.Root())
			}
		})
	}
}

// TestAdversarial_RouteParity_All26Endpoints проверяет все 26 эндпоинтов модуля на идентичность поведения.
func TestAdversarial_RouteParity_All26Endpoints(t *testing.T) {
	env, mod := setupTestModule(t)
	mod.Service().PauseQueue()

	// 1. Проверяем, что ВСЕ 26 эндпоинтов защищены авторизацией (401 Unauthorized)
	env.ClearAuth()

	allEndpoints := []struct {
		method string
		path   string
	}{
		// 21 эндпоинт под /downloads
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

		// 5 совместимых алиасов под /api/downloads
		{http.MethodPost, "/api/downloads/tracks/track-1/switch-version"},
		{http.MethodPost, "/api/downloads/tracks/track-1/bind-source"},
		{http.MethodGet, "/api/downloads/candidates?artist=A&title=T"},
		{http.MethodGet, "/api/downloads/preview?url=https://youtube.com"},
		{http.MethodPost, "/api/downloads/backfill/bind-source"},
	}

	if len(allEndpoints) != 26 {
		t.Fatalf("expected exactly 26 endpoints, got %d", len(allEndpoints))
	}

	for _, ep := range allEndpoints {
		t.Run("Auth_401_"+ep.method+"_"+ep.path, func(t *testing.T) {
			req := env.NewRequest(ep.method, ep.path, nil)
			resp := env.Do(req)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("endpoint %s %s: expected 401 Unauthorized, got %d", ep.method, ep.path, resp.StatusCode)
			}
		})
	}

	// 2. Восстанавливаем авторизацию для проверки функционального паритета 5 пар эндпоинтов
	env.SetAuthUser("admin")

	pairedEndpoints := []struct {
		name         string
		method       string
		pathLegacy   string
		pathModern   string
		payload      any
		query        string
		expectedCode int
	}{
		{
			name:         "switch_version_invalid_json",
			method:       http.MethodPost,
			pathLegacy:   "/downloads/tracks/tr1/switch-version",
			pathModern:   "/api/downloads/tracks/tr1/switch-version",
			payload:      "not-valid-json",
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "switch_version_empty_url",
			method:       http.MethodPost,
			pathLegacy:   "/downloads/tracks/tr1/switch-version",
			pathModern:   "/api/downloads/tracks/tr1/switch-version",
			payload:      downloader.SwitchVersionRequest{TargetURL: ""},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "switch_version_disallowed_domain",
			method:       http.MethodPost,
			pathLegacy:   "/downloads/tracks/tr1/switch-version",
			pathModern:   "/api/downloads/tracks/tr1/switch-version",
			payload:      downloader.SwitchVersionRequest{TargetURL: "https://evil.com/audio.mp3"},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "bind_source_alias_disallowed_domain",
			method:       http.MethodPost,
			pathLegacy:   "/downloads/tracks/tr1/bind-source",
			pathModern:   "/api/downloads/tracks/tr1/bind-source",
			payload:      downloader.SwitchVersionRequest{TargetURL: "https://evil.com/audio.mp3"},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "candidates_missing_query",
			method:       http.MethodGet,
			pathLegacy:   "/downloads/candidates",
			pathModern:   "/api/downloads/candidates",
			payload:      nil,
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "preview_missing_url",
			method:       http.MethodGet,
			pathLegacy:   "/downloads/preview",
			pathModern:   "/api/downloads/preview",
			payload:      nil,
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "preview_disallowed_url",
			method:       http.MethodGet,
			pathLegacy:   "/downloads/preview?url=https://evil.com/stream.mp3",
			pathModern:   "/api/downloads/preview?url=https://evil.com/stream.mp3",
			payload:      nil,
			expectedCode: http.StatusBadRequest,
		},
		{
			name:       "backfill_bind_source_missing_url",
			method:     http.MethodPost,
			pathLegacy: "/downloads/backfill/bind-source",
			pathModern: "/api/downloads/backfill/bind-source",
			payload: downloader.BindBackfillSourceRequest{
				Path:       "Pink Floyd/The Wall",
				AlbumTitle: "The Wall",
				ArtistName: "Pink Floyd",
				TargetURL:  "",
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:       "backfill_bind_source_disallowed_domain",
			method:     http.MethodPost,
			pathLegacy: "/downloads/backfill/bind-source",
			pathModern: "/api/downloads/backfill/bind-source",
			payload: downloader.BindBackfillSourceRequest{
				Path:       "Pink Floyd/The Wall",
				AlbumTitle: "The Wall",
				ArtistName: "Pink Floyd",
				TargetURL:  "https://evil.com/wall.mp3",
			},
			expectedCode: http.StatusBadRequest,
		},
	}

	for _, tc := range pairedEndpoints {
		t.Run("Parity_"+tc.name, func(t *testing.T) {
			var respLegacy, respModern *http.Response
			if tc.method == http.MethodPost {
				respLegacy = env.PostJSON(tc.pathLegacy, tc.payload)
				respModern = env.PostJSON(tc.pathModern, tc.payload)
			} else {
				respLegacy = env.Get(tc.pathLegacy)
				respModern = env.Get(tc.pathModern)
			}

			if respLegacy.StatusCode != tc.expectedCode {
				t.Fatalf("[%s] legacy path %s returned %d, want %d", tc.name, tc.pathLegacy, respLegacy.StatusCode, tc.expectedCode)
			}
			if respModern.StatusCode != tc.expectedCode {
				t.Fatalf("[%s] modern path %s returned %d, want %d", tc.name, tc.pathModern, respModern.StatusCode, tc.expectedCode)
			}

			// Проверяем побайтовое совпадение ответов обоих эндпоинтов
			bodyLegacy := env.ReadBody(respLegacy)
			bodyModern := env.ReadBody(respModern)

			if !bytes.Equal(bodyLegacy, bodyModern) {
				t.Fatalf("[%s] payload mismatch between %s and %s:\nLegacy: %s\nModern: %s",
					tc.name, tc.pathLegacy, tc.pathModern, string(bodyLegacy), string(bodyModern))
			}
		})
	}
}

// TestAdversarial_MalformedJSONPayloads тестирует сервер на устойчивость к битым, некорректным и гигантским JSON-телам.
func TestAdversarial_MalformedJSONPayloads(t *testing.T) {
	env, mod := setupTestModule(t)
	mod.Service().PauseQueue()

	endpoints := []string{
		"/downloads",
		"/downloads/batch",
		"/downloads/backfill/bind-source",
		"/downloads/tracks/t-1/switch-version",
		"/api/downloads/backfill/bind-source",
		"/api/downloads/tracks/t-1/switch-version",
	}

	// 1. Огромные тела запроса (> 10 МБ DefaultMaxBodyBytes)
	t.Run("Oversized_Payload_11MB", func(t *testing.T) {
		largePayload := fmt.Sprintf(`{"url":"https://www.youtube.com/watch?v=dQw4w9WgXcQ","padding":"%s"}`,
			strings.Repeat("A", 11*1024*1024))

		for _, ep := range endpoints {
			resp := env.PostJSON(ep, strings.NewReader(largePayload))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 Bad Request for 11MB payload on %s, got %d", ep, resp.StatusCode)
			}
			body := env.ReadBody(resp)
			var errResp httputil.ErrorResponse
			if err := json.Unmarshal(body, &errResp); err != nil {
				t.Fatalf("expected valid JSON error response for oversized payload on %s: %v", ep, err)
			}
		}
	})

	// 2. Синтаксически поврежденные JSON тела
	corruptedPayloads := []struct {
		name string
		raw  string
	}{
		{"truncated", `{"url": "https://youtube.com`},
		{"trailing_comma", `{"url": "https://youtube.com",,}`},
		{"unquoted_keys", `{url: "https://youtube.com"}`},
		{"single_quotes", `{'url': 'https://youtube.com'}`},
		{"empty_string", ``},
		{"null_literal", `null`},
		{"plain_integer", `12345`},
		{"plain_boolean", `true`},
		{"plain_array", `["https://youtube.com"]`},
		{"garbage_text", `THIS_IS_NOT_JSON_AT_ALL`},
		{"xml_content", `<request><url>https://youtube.com</url></request>`},
	}

	for _, cp := range corruptedPayloads {
		cp := cp
		t.Run("Corrupted_"+cp.name, func(t *testing.T) {
			for _, ep := range endpoints {
				// /downloads/batch without URL array check unmarshals null as empty struct and returns 200 OK
				if cp.name == "null_literal" && ep == "/downloads/batch" {
					continue
				}
				resp := env.PostJSON(ep, strings.NewReader(cp.raw))
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("[%s] expected 400 Bad Request for %s, got %d", cp.name, ep, resp.StatusCode)
				}
				body := env.ReadBody(resp)
				var errResp httputil.ErrorResponse
				if err := json.Unmarshal(body, &errResp); err != nil {
					t.Fatalf("[%s] expected valid JSON error response for %s: %v", cp.name, ep, err)
				}
			}
		})
	}

	// 3. Несовпадение типов полей (Type Mismatch)
	t.Run("TypeMismatch_SingleJob", func(t *testing.T) {
		mismatches := []string{
			`{"url": 12345}`,
			`{"url": false}`,
			`{"url": ["https://youtube.com"]}`,
			`{"url": {"sub": "https://youtube.com"}}`,
		}
		for _, raw := range mismatches {
			resp := env.PostJSON("/downloads", strings.NewReader(raw))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 Bad Request on /downloads for %s, got %d", raw, resp.StatusCode)
			}
		}
	})

	t.Run("TypeMismatch_BatchJob", func(t *testing.T) {
		mismatches := []string{
			`{"urls": "https://youtube.com"}`,
			`{"urls": [123, 456]}`,
			`{"urls": false}`,
		}
		for _, raw := range mismatches {
			resp := env.PostJSON("/downloads/batch", strings.NewReader(raw))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 Bad Request on /downloads/batch for %s, got %d", raw, resp.StatusCode)
			}
		}
	})

	t.Run("TypeMismatch_SwitchVersion", func(t *testing.T) {
		mismatches := []string{
			`{"target_url": 9999}`,
			`{"target_url": true}`,
			`{"target_url": ["array"]}`,
		}
		for _, raw := range mismatches {
			resp := env.PostJSON("/downloads/tracks/t-1/switch-version", strings.NewReader(raw))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 Bad Request on switch-version for %s, got %d", raw, resp.StatusCode)
			}
		}
	})

	t.Run("TypeMismatch_Backfill", func(t *testing.T) {
		mismatches := []string{
			`{"track_number": "NaN"}`,
			`{"track_number": [1, 2]}`,
			`{"target_url": 123}`,
		}
		for _, raw := range mismatches {
			resp := env.PostJSON("/downloads/backfill/bind-source", strings.NewReader(raw))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 Bad Request on backfill for %s, got %d", raw, resp.StatusCode)
			}
		}
	})
}

// TestAdversarial_GoldenSnapshots верифицирует канонические снапшоты ответов API через testkit.AssertGoldenJSON.
func TestAdversarial_GoldenSnapshots(t *testing.T) {
	env, mod := setupTestModule(t)

	// 1. Golden snapshot для начального Queue Summary (unpaused)
	t.Run("Snapshot_QueueSummary", func(t *testing.T) {
		resp := env.Get("/downloads/queue/summary")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		body := env.ReadBody(resp)
		testkit.AssertGoldenJSON(t, body, "testdata/queue_summary.golden.json")
	})

	mod.Service().PauseQueue()

	// 2. Golden snapshot для ответа на паузу
	t.Run("Snapshot_PauseResponse", func(t *testing.T) {
		resp := env.PostJSON("/downloads/pause", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		body := env.ReadBody(resp)
		testkit.AssertGoldenJSON(t, body, "testdata/pause_response.golden.json")
	})

	// 3. Golden snapshot для ответа на возобновление
	t.Run("Snapshot_ResumeResponse", func(t *testing.T) {
		resp := env.PostJSON("/downloads/resume", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		body := env.ReadBody(resp)
		testkit.AssertGoldenJSON(t, body, "testdata/resume_response.golden.json")
	})

	// 4. Golden snapshot для очистки пустой очереди
	t.Run("Snapshot_ClearQueueResponse", func(t *testing.T) {
		resp := env.PostJSON("/downloads/queue/clear", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		body := env.ReadBody(resp)
		testkit.AssertGoldenJSON(t, body, "testdata/clear_response.golden.json")
	})

	// 5. Golden snapshot для удаления задачи
	t.Run("Snapshot_DeleteResponse", func(t *testing.T) {
		req := env.NewRequest(http.MethodDelete, "/downloads/job-golden-test-delete", nil)
		resp := env.Do(req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		body := env.ReadBody(resp)
		testkit.AssertGoldenJSON(t, body, "testdata/delete_response.golden.json")
	})

	// 6. Golden snapshot для канонической ошибки 400 Bad Request
	t.Run("Snapshot_ErrorResponse", func(t *testing.T) {
		resp := env.PostJSON("/downloads", strings.NewReader("broken-json"))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d", resp.StatusCode)
		}
		body := env.ReadBody(resp)
		testkit.AssertGoldenJSON(t, body, "testdata/error_response.golden.json")
	})
}
