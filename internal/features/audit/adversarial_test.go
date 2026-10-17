package audit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"music-manager/internal/features/audit"
	"music-manager/internal/services/scanner"
)

// ============================================================================
// 1. Adversarial Path Traversal Tests
// ============================================================================

func TestAdversarial_PathTraversal_Duplicates(t *testing.T) {
	env, _, _ := setupAuditTest(t)

	// Create sensitive indicator canary file outside sandbox to verify it is NEVER deleted
	tmpDir := t.TempDir()
	canaryFile := filepath.Join(tmpDir, "sensitive_canary.txt")
	if err := os.WriteFile(canaryFile, []byte("TOP_SECRET_DO_NOT_DELETE"), 0644); err != nil {
		t.Fatalf("failed to create canary file: %v", err)
	}

	traversalPayloads := []string{
		"../../../../etc/shadow",
		"../../../etc/passwd",
		"Album/../../../../../../etc/shadow",
		"Album/%2e%2e%2f%2e%2e%2fetc%2fshadow",
		"Album/..%2f..%2fetc%2fshadow",
		"Album/%252e%252e%252f%252e%252e%252fetc/shadow",
		"Album/test%00file.mp3",
		"Album/test\x00file.mp3",
		"..\\..\\..\\etc\\shadow",
		"\uff0e\uff0e\uff0f\uff0e\uff0e\uff0fetc/shadow",
	}

	// 1. Test POST /files/audit/duplicates/fix and aliases
	fixEndpoints := []string{"/files/audit/duplicates/fix", "/duplicates/fix", "/api/audit/duplicates/fix"}
	for _, ep := range fixEndpoints {
		for _, payload := range traversalPayloads {
			t.Run(ep+"/"+payload, func(t *testing.T) {
				fixReq := audit.FixDuplicatesRequest{
					PathsToDelete: []string{payload},
				}
				resp := env.PostJSON(ep, fixReq)
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("expected 200 OK with error payload on %s, got %d", ep, resp.StatusCode)
				}

				var fixRes audit.FixDuplicatesResponse
				env.DecodeJSON(resp, &fixRes)

				if fixRes.Success {
					t.Fatalf("expected success=false for traversal payload %q, got true", payload)
				}
				if fixRes.DeletedCount != 0 {
					t.Fatalf("expected deleted_count=0 for traversal payload %q, got %d", payload, fixRes.DeletedCount)
				}
				if len(fixRes.Errors) == 0 {
					t.Fatalf("expected errors to be reported for traversal payload %q", payload)
				}

				// Verify canary file remains untouched
				if _, err := os.Stat(canaryFile); err != nil {
					t.Fatalf("canary file was modified or deleted! Path traversal succeeded: %v", err)
				}
			})
		}
	}

	// 2. Test POST /duplicates/resolve and /api/duplicates/resolve
	resolveEndpoints := []string{"/duplicates/resolve", "/api/duplicates/resolve"}
	for _, ep := range resolveEndpoints {
		for _, payload := range traversalPayloads {
			t.Run(ep+"/"+payload, func(t *testing.T) {
				resolveReq := audit.ResolveDuplicatesRequest{
					GroupKey:      "some_group",
					KeepPath:      payload,
					PathsToDelete: []string{payload},
				}
				resp := env.PostJSON(ep, resolveReq)
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("expected 200 OK on %s, got %d", ep, resp.StatusCode)
				}

				var fixRes audit.FixDuplicatesResponse
				env.DecodeJSON(resp, &fixRes)

				if fixRes.DeletedCount != 0 {
					t.Fatalf("expected deleted_count=0 for traversal payload %q on %s, got %d", payload, ep, fixRes.DeletedCount)
				}

				if _, err := os.Stat(canaryFile); err != nil {
					t.Fatalf("canary file was modified or deleted by %s! Path traversal succeeded: %v", ep, err)
				}
			})
		}
	}
}

func TestAdversarial_PathTraversal_PathFixing(t *testing.T) {
	env, _, _ := setupAuditTest(t)

	traversalPayloads := []string{
		"../../../../etc/shadow",
		"../../../etc/passwd",
		"..%2f..%2fetc%2fpasswd",
		"%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		"%252e%252e%252fetc%252fpasswd",
		"track\x00evil.mp3",
		"track%00evil.mp3",
		"..\\..\\..\\etc\\passwd",
		"\uff0e\uff0e\uff0f\uff0e\uff0e\uff0fetc/passwd",
	}

	endpoints := []string{"/files/audit/fix-path", "/api/audit/fix-path"}
	for _, ep := range endpoints {
		for _, payload := range traversalPayloads {
			t.Run(ep+"/"+payload, func(t *testing.T) {
				fixReq := audit.FixPathRequest{
					Path:      payload,
					Overwrite: true,
				}
				resp := env.PostJSON(ep, fixReq)
				if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("expected rejection (403/404/400) for traversal on %s with payload %q, got %d", ep, payload, resp.StatusCode)
				}
			})
		}
	}
}

func TestAdversarial_PathTraversal_AlbumCoversAndCatalog(t *testing.T) {
	env, _, _ := setupAuditTest(t)

	traversalPayloads := []string{
		"../../../../etc",
		"../../../etc/shadow",
		"..%2f..%2fetc",
		"Album%00evil",
		"..\\..\\..\\etc",
		"\uff0e\uff0e\uff0f\uff0e\uff0e\uff0fetc",
	}

	// 1. POST /files/audit/missing-covers/backfill and aliases
	coverEndpoints := []string{"/files/audit/missing-covers/backfill", "/backfill/covers", "/api/audit/missing-covers/backfill"}
	for _, ep := range coverEndpoints {
		for _, payload := range traversalPayloads {
			t.Run(ep+"/"+payload, func(t *testing.T) {
				req := audit.BackfillCoverRequest{
					FolderPath: payload,
				}
				resp := env.PostJSON(ep, req)
				if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("expected 404/403/400 for cover backfill on %s with payload %q, got %d", ep, payload, resp.StatusCode)
				}
			})
		}
	}

	// 2. GET /files/audit/incomplete-albums/editor-data
	for _, payload := range traversalPayloads {
		t.Run("editor-data/"+payload, func(t *testing.T) {
			escaped := url.QueryEscape(payload)
			resp := env.Get("/files/audit/incomplete-albums/editor-data?path=" + escaped)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 OK for editor-data query, got %d", resp.StatusCode)
			}
			var data audit.BackfillEditorDataResponse
			env.DecodeJSON(resp, &data)
			if len(data.Tracks) != 0 {
				t.Fatalf("expected 0 tracks read outside sandbox for payload %q, got %d", payload, len(data.Tracks))
			}
		})
	}

	// 3. GET /files/audit/tag-issues/catalog-match-album
	for _, payload := range traversalPayloads {
		t.Run("catalog-match-album/"+payload, func(t *testing.T) {
			escaped := url.QueryEscape(payload)
			resp := env.Get("/files/audit/tag-issues/catalog-match-album?folder_path=" + escaped)
			if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 404/403/400 on catalog-match-album for payload %q, got %d", payload, resp.StatusCode)
			}
		})
	}

	// 4. POST /files/audit/tag-issues/apply-catalog-album
	for _, payload := range traversalPayloads {
		t.Run("apply-catalog-album/"+payload, func(t *testing.T) {
			req := audit.ApplyCatalogAlbumRequest{
				FolderPath: payload,
			}
			resp := env.PostJSON("/files/audit/tag-issues/apply-catalog-album", req)
			var res audit.FixBatchResponse
			if resp.StatusCode == http.StatusOK {
				env.DecodeJSON(resp, &res)
				if res.FixedCount != 0 {
					t.Fatalf("expected 0 tracks fixed for traversal payload %q, got %d", payload, res.FixedCount)
				}
			}
		})
	}

	// 5. POST /files/audit/tag-issues/fix with traversal
	for _, payload := range traversalPayloads {
		t.Run("tag-issues-fix/"+payload, func(t *testing.T) {
			req := audit.FixTagIssueRequest{
				Path:  payload,
				Title: "Malicious Title",
			}
			resp := env.PostJSON("/files/audit/tag-issues/fix", req)
			if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 403/404/400 for tag fix on payload %q, got %d", payload, resp.StatusCode)
			}
		})
	}

	// 6. POST /files/audit/tag-issues/apply-catalog with traversal
	for _, payload := range traversalPayloads {
		t.Run("apply-catalog/"+payload, func(t *testing.T) {
			req := audit.ApplyCatalogTagsRequest{
				Path:  payload,
				Title: "Catalog Title",
			}
			resp := env.PostJSON("/files/audit/tag-issues/apply-catalog", req)
			if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 403/404/400 for apply-catalog on payload %q, got %d", payload, resp.StatusCode)
			}
		})
	}
}

// ============================================================================
// 2. Adversarial Malformed JSON Tests
// ============================================================================

func TestAdversarial_MalformedJSON_SyntaxAndTypeErrors(t *testing.T) {
	env, _, _ := setupAuditTest(t)

	targetEndpoints := []string{
		// Batch retag & tag issues
		"/files/audit/tag-issues/fix",
		"/tags/audit/fix",
		"/files/audit/tag-issues/fix-selected",
		"/tags/audit/fix-selected",
		"/files/audit/tag-issues/apply-catalog",
		"/tags/audit/apply-catalog",
		"/files/audit/tag-issues/apply-catalog-album",
		"/tags/audit/apply-catalog-album",
		// Backfill
		"/files/audit/missing-covers/backfill",
		"/backfill/covers",
		"/files/audit/incomplete-albums/download-missing",
		"/backfill/download-missing",
		// Duplicates
		"/files/audit/duplicates/fix",
		"/duplicates/fix",
		"/duplicates/resolve",
		"/api/duplicates/resolve",
		// Path fixing
		"/files/audit/fix-path",
		"/api/audit/fix-path",
	}

	malformedSyntaxPayloads := []struct {
		name string
		raw  string
	}{
		{"truncated_json", `{"path": "artist/album", "title":`},
		{"syntax_error_unquoted", `{path: "artist/album"}`},
		{"unclosed_brace", `{"path": "artist/album"`},
		{"invalid_commas", `{"path": "artist/album",,,}`},
		{"plain_text", `This is raw unformatted text`},
		{"empty_body", ``},
		{"boolean_scalar", `true`},
		{"number_scalar", `12345`},
		{"string_scalar", `"just a string"`},
	}

	for _, ep := range targetEndpoints {
		for _, tc := range malformedSyntaxPayloads {
			testName := fmt.Sprintf("%s/%s", ep, tc.name)
			t.Run(testName, func(t *testing.T) {
				req := env.NewRequest(http.MethodPost, ep, bytes.NewBufferString(tc.raw))
				req.Header.Set("Content-Type", "application/json")
				resp := env.Do(req)

				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("expected 400 Bad Request on %s with payload %s, got %d", ep, tc.name, resp.StatusCode)
				}

				body := string(env.ReadBody(resp))
				if !strings.Contains(strings.ToLower(body), "invalid") &&
					!strings.Contains(strings.ToLower(body), "error") &&
					!strings.Contains(strings.ToLower(body), "body") &&
					!strings.Contains(strings.ToLower(body), "bad request") {
					t.Fatalf("expected error message in response body for %s with payload %s, got: %s", ep, tc.name, body)
				}
			})
		}
	}
}

func TestAdversarial_MalformedJSON_TargetedFieldTypeMismatches(t *testing.T) {
	env, _, _ := setupAuditTest(t)

	targetedCases := []struct {
		endpoint string
		payload  string
		desc     string
	}{
		{"/files/audit/fix-path", `{"path": 12345}`, "int instead of string for path"},
		{"/files/audit/tag-issues/fix", `{"path": ["not", "string"]}`, "array instead of string for path"},
		{"/files/audit/tag-issues/fix-selected", `{"items": "not_an_array"}`, "string instead of array for items"},
		{"/tags/audit/fix-selected", `{"items": 9999}`, "int instead of array for items"},
		{"/files/audit/tag-issues/apply-catalog", `{"path": true}`, "bool instead of string for path"},
		{"/files/audit/missing-covers/backfill", `{"album_id": "not_an_int"}`, "string instead of int for album_id"},
		{"/files/audit/incomplete-albums/download-missing", `{"track_numbers": "not_an_array"}`, "string instead of []int for track_numbers"},
		{"/backfill/download-missing", `{"track_numbers": {"num": 1}}`, "object instead of []int for track_numbers"},
		{"/files/audit/duplicates/fix", `{"paths_to_delete": "not_an_array"}`, "string instead of []string for paths_to_delete"},
		{"/duplicates/fix", `{"paths_to_delete": 12345}`, "int instead of []string for paths_to_delete"},
		{"/duplicates/resolve", `{"paths_to_delete": false}`, "bool instead of []string for paths_to_delete"},
		{"/api/duplicates/resolve", `{"paths_to_delete": {"nested": "obj"}}`, "object instead of []string for paths_to_delete"},
	}

	for _, tc := range targetedCases {
		t.Run(tc.endpoint+"/"+tc.desc, func(t *testing.T) {
			req := env.NewRequest(http.MethodPost, tc.endpoint, bytes.NewBufferString(tc.payload))
			req.Header.Set("Content-Type", "application/json")
			resp := env.Do(req)

			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 Bad Request on %s for %s, got %d", tc.endpoint, tc.desc, resp.StatusCode)
			}
		})
	}
}

// ============================================================================
// 3. Golden Snapshot Verification Tests
// ============================================================================

func TestAdversarial_GoldenSnapshots_Duplicates(t *testing.T) {
	// 1. Verify Clean Duplicates Listing Golden Snapshot
	t.Run("duplicates_clean_golden", func(t *testing.T) {
		env, _, _ := setupAuditTest(t)

		resp := env.Get("/files/audit/duplicates")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		body := env.ReadBody(resp)

		var dupResp audit.DuplicatesResponse
		if err := json.Unmarshal(body, &dupResp); err != nil {
			t.Fatalf("failed to decode duplicates json: %v", err)
		}
		if dupResp.TotalDuplicateFiles != 0 {
			t.Fatalf("expected 0 duplicate files, got %d", dupResp.TotalDuplicateFiles)
		}

		env.AssertGoldenJSON(body, "testdata/duplicates_clean.golden.json")

		// Verify aliases match canonical response
		aliasResp := env.Get("/duplicates")
		if aliasResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK on alias, got %d", aliasResp.StatusCode)
		}
		aliasBody := env.ReadBody(aliasResp)
		env.AssertGoldenJSON(aliasBody, "testdata/duplicates_clean.golden.json")
	})

	// 2. Verify Populated Duplicates Listing Golden Snapshot with Timestamp Normalization
	t.Run("duplicates_populated_golden", func(t *testing.T) {
		env, _, _ := setupAuditTest(t)

		env.CreateDummyMP3("Artist Dup/Album Dup/01. Track One.mp3", "Track One", "Artist Dup", "Album Dup")
		env.CreateDummyMP3("Artist Dup/Album Dup/01. Track One (1).mp3", "Track One", "Artist Dup", "Album Dup")

		scannerSvc := scanner.NewScanner(env.MusicSandbox, env.DB, nil)
		if _, err := scannerSvc.ScanAll(context.Background()); err != nil {
			t.Fatalf("failed to scan music files: %v", err)
		}

		resp := env.Get("/files/audit/duplicates")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		rawBody := env.ReadBody(resp)

		var dupResp audit.DuplicatesResponse
		if err := json.Unmarshal(rawBody, &dupResp); err != nil {
			t.Fatalf("failed to decode populated duplicates json: %v", err)
		}
		if dupResp.TotalDuplicateFiles != 1 {
			t.Fatalf("expected 1 duplicate file, got %d", dupResp.TotalDuplicateFiles)
		}
		if len(dupResp.Groups) != 1 {
			t.Fatalf("expected 1 duplicate group, got %d", len(dupResp.Groups))
		}

		// Normalize dynamic modified_at timestamp for deterministic golden testing
		re := regexp.MustCompile(`"modified_at":\s*"[^"]+"`)
		normalized := re.ReplaceAll(rawBody, []byte(`"modified_at": "2026-10-15T12:00:00Z"`))

		env.AssertGoldenJSON(normalized, "testdata/duplicates_populated.golden.json")
	})
}

// ============================================================================
// 4. Comprehensive Unauthorized Access Tests (All 35 canonical routes + aliases)
// ============================================================================

func TestAdversarial_UnauthorizedAccess_AllEndpoints(t *testing.T) {
	env, _, _ := setupAuditTest(t)
	env.ClearAuth()

	type routeDef struct {
		method string
		path   string
	}

	// 1. All 35 Canonical /files/audit endpoints
	canonicalRoutes := []routeDef{
		{http.MethodGet, "/files/audit/summary"},
		{http.MethodPost, "/files/audit/refresh"},
		{http.MethodGet, "/files/audit/search"},
		{http.MethodGet, "/files/audit/invalid-paths"},
		{http.MethodPost, "/files/audit/fix-path"},
		{http.MethodPost, "/files/audit/fix-all-paths"},
		{http.MethodPost, "/files/audit/reorganize"},
		{http.MethodPost, "/files/audit/normalize-navidrome"},
		{http.MethodGet, "/files/audit/duplicates"},
		{http.MethodPost, "/files/audit/duplicates/fix"},
		{http.MethodPost, "/files/audit/duplicates/fix-all"},
		{http.MethodGet, "/files/audit/missing-lyrics"},
		{http.MethodPost, "/files/audit/missing-lyrics/fetch-all"},
		{http.MethodGet, "/files/audit/missing-lyrics/progress"},
		{http.MethodGet, "/files/audit/missing-covers"},
		{http.MethodPost, "/files/audit/missing-covers/backfill"},
		{http.MethodPost, "/files/audit/missing-covers/backfill-all"},
		{http.MethodGet, "/files/audit/missing-covers/progress"},
		{http.MethodPost, "/files/audit/missing-covers/cancel"},
		{http.MethodGet, "/files/audit/non-en-genres"},
		{http.MethodPost, "/files/audit/retag-genres"},
		{http.MethodGet, "/files/audit/incomplete-albums"},
		{http.MethodGet, "/files/audit/incomplete-albums/catalog-match"},
		{http.MethodGet, "/files/audit/incomplete-albums/editor-data"},
		{http.MethodGet, "/files/audit/incomplete-albums/backfill-editor-data"},
		{http.MethodPost, "/files/audit/incomplete-albums/download-missing"},
		{http.MethodGet, "/files/audit/tag-issues"},
		{http.MethodPost, "/files/audit/tag-issues/fix"},
		{http.MethodPost, "/files/audit/tag-issues/fix-safe"},
		{http.MethodPost, "/files/audit/tag-issues/fix-selected"},
		{http.MethodPost, "/files/audit/tag-issues/fix-all"},
		{http.MethodGet, "/files/audit/tag-issues/catalog-match"},
		{http.MethodGet, "/files/audit/tag-issues/catalog-match-album"},
		{http.MethodPost, "/files/audit/tag-issues/apply-catalog"},
		{http.MethodPost, "/files/audit/tag-issues/apply-catalog-album"},
	}

	// 2. Key aliases
	aliasRoutes := []routeDef{
		{http.MethodGet, "/api/audit/summary"},
		{http.MethodPost, "/api/audit/refresh"},
		{http.MethodGet, "/api/audit/search"},
		{http.MethodGet, "/api/audit/invalid-paths"},
		{http.MethodPost, "/api/audit/fix-path"},
		{http.MethodPost, "/api/audit/fix-all-paths"},
		{http.MethodPost, "/api/audit/reorganize"},
		{http.MethodPost, "/api/audit/normalize-navidrome"},
		{http.MethodGet, "/api/audit/duplicates"},
		{http.MethodPost, "/api/audit/duplicates/fix"},
		{http.MethodPost, "/api/audit/duplicates/fix-all"},
		{http.MethodPost, "/api/audit/duplicates/resolve"},
		{http.MethodGet, "/duplicates"},
		{http.MethodPost, "/duplicates/fix"},
		{http.MethodPost, "/duplicates/fix-all"},
		{http.MethodPost, "/duplicates/resolve"},
		{http.MethodGet, "/api/duplicates"},
		{http.MethodPost, "/api/duplicates/fix"},
		{http.MethodPost, "/api/duplicates/fix-all"},
		{http.MethodPost, "/api/duplicates/resolve"},
		{http.MethodPost, "/backfill/download-missing"},
		{http.MethodPost, "/backfill/covers"},
		{http.MethodPost, "/backfill/covers-all"},
		{http.MethodGet, "/backfill/editor-data"},
		{http.MethodGet, "/tags/audit"},
		{http.MethodPost, "/tags/audit/fix"},
		{http.MethodPost, "/tags/audit/fix-safe"},
		{http.MethodPost, "/tags/audit/fix-selected"},
		{http.MethodPost, "/tags/audit/fix-all"},
		{http.MethodGet, "/tags/audit/catalog-match"},
		{http.MethodGet, "/tags/audit/catalog-match-album"},
		{http.MethodPost, "/tags/audit/apply-catalog"},
		{http.MethodPost, "/tags/audit/apply-catalog-album"},
	}

	allRoutes := append(canonicalRoutes, aliasRoutes...)

	for _, r := range allRoutes {
		testName := fmt.Sprintf("%s %s", r.method, r.path)
		t.Run(testName, func(t *testing.T) {
			req := env.NewRequest(r.method, r.path, nil)
			resp := env.Do(req)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("security violation: expected 401 Unauthorized on %s %s, got %d", r.method, r.path, resp.StatusCode)
			}
		})
	}
}
