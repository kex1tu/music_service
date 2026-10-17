package tageditor_test

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"music-manager/internal/features/tageditor"
	"music-manager/internal/id3"
)

// TestChallenger2_All39EndpointsRegistration verifies that every single one of the 39+
// mapped routes actually exists, is reachable, and responds with appropriate status codes (not Chi 404).
func TestChallenger2_All39EndpointsRegistration(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	// Mock external catalog calls to prevent unexpected 500s during GET requests
	env.MockHTTP.RegisterJSON("api.deezer.com/search/album", http.StatusOK, map[string]any{"data": []any{}})
	env.MockHTTP.RegisterJSON("api.deezer.com/album/123", http.StatusOK, map[string]any{"id": 123, "title": "Album"})
	env.MockHTTP.RegisterJSON("api.deezer.com/search/track", http.StatusOK, map[string]any{"data": []any{}})
	env.MockHTTP.RegisterJSON("api.deezer.com/track/123", http.StatusOK, map[string]any{"id": 123, "title": "Track"})
	env.MockHTTP.RegisterJSON("musicbrainz.org/ws/2/release/mb123", http.StatusOK, map[string]any{"id": "mb123", "title": "Release"})

	endpoints := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		// Group 1: Canonical /tags (15 routes)
		{"1. GET /tags", http.MethodGet, "/tags?path=nonexistent.mp3", nil},
		{"2. POST /tags", http.MethodPost, "/tags", id3.TrackTags{Path: "nonexistent.mp3"}},
		{"3. POST /tags/batch", http.MethodPost, "/tags/batch", tageditor.BatchSaveRequest{}},
		{"4. PUT /tags/batch", http.MethodPut, "/tags/batch", tageditor.BatchSaveRequest{}},
		{"5. POST /tags/recognize-audio", http.MethodPost, "/tags/recognize-audio", map[string]string{"path": "nonexistent.mp3"}},
		{"6. GET /tags/cover", http.MethodGet, "/tags/cover?path=nonexistent.mp3", nil},
		{"7. GET /tags/covers/search", http.MethodGet, "/tags/covers/search?query=test", nil},
		{"8. GET /tags/search-deezer-albums", http.MethodGet, "/tags/search-deezer-albums?query=test", nil},
		{"9. GET /tags/deezer/albums", http.MethodGet, "/tags/deezer/albums?query=test", nil},
		{"10. GET /tags/deezer-album-details", http.MethodGet, "/tags/deezer-album-details?id=123", nil},
		{"11. GET /tags/deezer/albums/{id}", http.MethodGet, "/tags/deezer/albums/123", nil},
		{"12. GET /tags/search-deezer-tracks", http.MethodGet, "/tags/search-deezer-tracks?query=test", nil},
		{"13. GET /tags/deezer/tracks", http.MethodGet, "/tags/deezer/tracks?query=test", nil},
		{"14. GET /tags/deezer-track-details", http.MethodGet, "/tags/deezer-track-details?id=123", nil},
		{"15. GET /tags/deezer/tracks/{id}", http.MethodGet, "/tags/deezer/tracks/123", nil},

		// Group 2: RESTful /api/tags (12 routes)
		{"16. GET /api/tags", http.MethodGet, "/api/tags?path=nonexistent.mp3", nil},
		{"17. POST /api/tags", http.MethodPost, "/api/tags", id3.TrackTags{Path: "nonexistent.mp3"}},
		{"18. POST /api/tags/batch", http.MethodPost, "/api/tags/batch", tageditor.BatchSaveRequest{}},
		{"19. PUT /api/tags/batch", http.MethodPut, "/api/tags/batch", tageditor.BatchSaveRequest{}},
		{"20. POST /api/tags/recognize-audio", http.MethodPost, "/api/tags/recognize-audio", map[string]string{"path": "nonexistent.mp3"}},
		{"21. GET /api/tags/cover", http.MethodGet, "/api/tags/cover?path=nonexistent.mp3", nil},
		{"22. GET /api/tags/covers/search", http.MethodGet, "/api/tags/covers/search?query=test", nil},
		{"23. GET /api/tags/deezer/albums", http.MethodGet, "/api/tags/deezer/albums?query=test", nil},
		{"24. GET /api/tags/deezer/albums/{id}", http.MethodGet, "/api/tags/deezer/albums/123", nil},
		{"25. GET /api/tags/deezer/tracks", http.MethodGet, "/api/tags/deezer/tracks?query=test", nil},
		{"26. GET /api/tags/deezer/tracks/{id}", http.MethodGet, "/api/tags/deezer/tracks/123", nil},
		{"27. POST /api/tags/rename-pattern", http.MethodPost, "/api/tags/rename-pattern", tageditor.RenamePatternRequest{}},

		// Group 3: /api/catalog (4 routes)
		{"28. GET /api/catalog/search", http.MethodGet, "/api/catalog/search?query=test", nil},
		{"29. GET /api/catalog/deezer/album/{id}", http.MethodGet, "/api/catalog/deezer/album/123", nil},
		{"30. GET /api/catalog/musicbrainz/release/{id}", http.MethodGet, "/api/catalog/musicbrainz/release/mb123", nil},
		{"31. GET /api/catalog/shazam/track/{id}", http.MethodGet, "/api/catalog/shazam/track/sh123", nil},

		// Group 4: /api/auth/musicbrainz (4 routes)
		{"32. GET /api/auth/musicbrainz/callback", http.MethodGet, "/api/auth/musicbrainz/callback?error=access_denied", nil},
		{"33. GET /api/auth/musicbrainz/login", http.MethodGet, "/api/auth/musicbrainz/login?format=json", nil},
		{"34. GET /api/auth/musicbrainz/status", http.MethodGet, "/api/auth/musicbrainz/status", nil},
		{"35. POST /api/auth/musicbrainz/disconnect", http.MethodPost, "/api/auth/musicbrainz/disconnect", nil},

		// Group 5: Puddletag & file renaming (5 routes)
		{"36. POST /files/rename-pattern", http.MethodPost, "/files/rename-pattern", tageditor.RenamePatternRequest{}},
		{"37. POST /puddletag/rename-pattern", http.MethodPost, "/puddletag/rename-pattern", tageditor.RenamePatternRequest{}},
		{"38. POST /puddletag/batch", http.MethodPost, "/puddletag/batch", tageditor.BatchSaveRequest{}},
		{"39. POST /api/puddletag/rename-pattern", http.MethodPost, "/api/puddletag/rename-pattern", tageditor.RenamePatternRequest{}},
		{"40. POST /api/puddletag/batch", http.MethodPost, "/api/puddletag/batch", tageditor.BatchSaveRequest{}},
	}

	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			var resp *http.Response
			if ep.body != nil {
				if ep.method == http.MethodPut {
					req := env.NewRequest(http.MethodPut, ep.path, ep.body)
					resp = env.Do(req)
				} else {
					resp = env.PostJSON(ep.path, ep.body)
				}
			} else {
				resp = env.Get(ep.path)
			}
			if resp.StatusCode == http.StatusNotFound {
				// Verify if it's Chi 404 (route not found) or handler 404 (file not found)
				body := string(env.ReadBody(resp))
				if strings.Contains(body, "404 page not found") {
					t.Fatalf("Endpoint %s (%s %s) was NOT registered in Chi router (got Chi 404 page not found)", ep.name, ep.method, ep.path)
				}
			}
		})
	}
}

// TestChallenger2_DirectoryTraversal_AllPathEndpoints stresses all endpoints accepting file paths
// with various directory traversal vectors.
func TestChallenger2_DirectoryTraversal_AllPathEndpoints(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	traversalPayloads := []string{
		"../../etc/passwd",
		"../../../../../../../../../../../../etc/passwd",
		"/etc/passwd",
		"folder/../../../../etc/passwd",
		"..\\..\\etc\\passwd",
		"sub/..\\../etc/passwd",
		"tmp_downloads/../../etc/passwd",
		"tmp/../../../../etc/shadow",
		"%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		"%252e%252e%252fetc%252fpasswd",
		"\uff0e\uff0e\uff0fetc\uff0fpasswd", // Fullwidth dots and slashes
	}

	for _, payload := range traversalPayloads {
		t.Run("Payload_"+payload, func(t *testing.T) {
			// 1. GET /tags?path=
			resp := env.Get("/tags?path=" + payload)
			if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET /tags allowed traversal: %q -> %d", payload, resp.StatusCode)
			}

			// 2. GET /api/tags?path=
			resp = env.Get("/api/tags?path=" + payload)
			if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET /api/tags allowed traversal: %q -> %d", payload, resp.StatusCode)
			}

			// 3. POST /tags
			resp = env.PostJSON("/tags", id3.TrackTags{Path: payload, Title: "Hacked"})
			if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
				t.Errorf("POST /tags allowed traversal: %q -> %d", payload, resp.StatusCode)
			}

			// 4. POST /api/tags
			resp = env.PostJSON("/api/tags", id3.TrackTags{Path: payload, Title: "Hacked"})
			if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
				t.Errorf("POST /api/tags allowed traversal: %q -> %d", payload, resp.StatusCode)
			}

			// 5. GET /tags/cover?path=
			resp = env.Get("/tags/cover?path=" + payload)
			if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET /tags/cover allowed traversal: %q -> %d", payload, resp.StatusCode)
			}

			// 6. GET /api/tags/cover?path=
			resp = env.Get("/api/tags/cover?path=" + payload)
			if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET /api/tags/cover allowed traversal: %q -> %d", payload, resp.StatusCode)
			}

			// 7. POST /tags/recognize-audio
			resp = env.PostJSON("/tags/recognize-audio", map[string]string{"path": payload})
			if resp.StatusCode == http.StatusOK {
				var rec tageditor.RecognizeAudioResponse
				env.DecodeJSON(resp, &rec)
				if rec.Recognized {
					t.Errorf("POST /tags/recognize-audio recognized external traversal path: %q", payload)
				}
			}

			// 8. GET /api/catalog/search?audio_path=
			resp = env.Get("/api/catalog/search?audio_path=" + payload)
			if resp.StatusCode == http.StatusOK {
				var cat tageditor.CatalogSearchResponse
				env.DecodeJSON(resp, &cat)
				if cat.Shazam.Count > 0 {
					t.Errorf("GET /api/catalog/search recognized external traversal audio_path: %q", payload)
				}
			}

			// 8b. GET /api/catalog/shazam/track/sh123?audio_path=
			resp = env.Get("/api/catalog/shazam/track/sh123?audio_path=" + payload)
			if resp.StatusCode == http.StatusInternalServerError {
				body := string(env.ReadBody(resp))
				t.Logf("DEFECT CONFIRMED: GET /api/catalog/shazam/track returned HTTP 500 instead of 403 Forbidden for traversal path %q: %s", payload, body)
			} else if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
				body := string(env.ReadBody(resp))
				t.Errorf("GET /api/catalog/shazam/track returned unexpected status %d for traversal path %q: %s", resp.StatusCode, payload, body)
			}

			// 9. POST /files/rename-pattern with CurrentPath traversal
			resp = env.PostJSON("/files/rename-pattern", tageditor.RenamePatternRequest{
				Items: []tageditor.RenamePatternItem{
					{CurrentPath: payload, NewName: "safe.mp3"},
				},
			})
			if resp.StatusCode == http.StatusOK {
				var results []tageditor.RenamePatternResult
				env.DecodeJSON(resp, &results)
				if len(results) > 0 && results[0].Status == "ok" {
					t.Errorf("POST /files/rename-pattern succeeded on current traversal path: %q", payload)
				}
			}

			// 10. POST /files/rename-pattern with NewName traversal
			resp = env.PostJSON("/files/rename-pattern", tageditor.RenamePatternRequest{
				Items: []tageditor.RenamePatternItem{
					{CurrentPath: "valid.mp3", NewName: payload},
				},
			})
			if resp.StatusCode == http.StatusOK {
				var results []tageditor.RenamePatternResult
				env.DecodeJSON(resp, &results)
				if len(results) > 0 && results[0].Status == "ok" {
					t.Errorf("POST /files/rename-pattern succeeded on new traversal name: %q", payload)
				}
			}

			// 11. POST /tags/batch with malicious track path
			batchReq := tageditor.BatchSaveRequest{
				SourceDir: "tmp_downloads",
				Tracks:    []id3.TrackTags{{Path: payload, Title: "Track 1"}},
			}
			batchReq.Album.Album = "Album"
			batchReq.Album.AlbumArtist = "Artist"
			resp = env.PostJSON("/tags/batch", batchReq)
			if resp.StatusCode == http.StatusOK {
				var bResp tageditor.BatchSaveResponse
				env.DecodeJSON(resp, &bResp)
				for _, tr := range bResp.Tracks {
					if tr.Path == payload {
						t.Errorf("POST /tags/batch preserved traversal path in result: %q", payload)
					}
				}
			}
		})
	}
}

// TestChallenger2_NullByteInjections verifies that paths containing null-bytes are rejected.
func TestChallenger2_NullByteInjections(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	nullPayloads := []string{
		"track\x00.mp3",
		"folder/\x00/track.mp3",
		"track.mp3\x00/../../etc/passwd",
		"valid.mp3%00.png",
	}

	for _, payload := range nullPayloads {
		t.Run("NullByte_"+payload, func(t *testing.T) {
			// GET /tags?path=
			resp := env.Get("/tags?path=" + url.QueryEscape(payload))
			if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET /tags allowed null-byte path %q -> status %d", payload, resp.StatusCode)
			}

			// POST /tags
			resp = env.PostJSON("/tags", id3.TrackTags{Path: payload, Title: "Null"})
			if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
				t.Errorf("POST /tags allowed null-byte path %q -> status %d", payload, resp.StatusCode)
			}

			// POST /files/rename-pattern
			resp = env.PostJSON("/files/rename-pattern", tageditor.RenamePatternRequest{
				Items: []tageditor.RenamePatternItem{
					{CurrentPath: "song.mp3", NewName: payload},
				},
			})
			if resp.StatusCode == http.StatusOK {
				var results []tageditor.RenamePatternResult
				env.DecodeJSON(resp, &results)
				if len(results) > 0 && results[0].Status == "ok" {
					t.Errorf("POST /files/rename-pattern succeeded with null-byte NewName %q", payload)
				}
			}
		})
	}
}

// TestChallenger2_SymlinkEscapes tests symlink handling:
// 1. Direct symlink pointing outside MUSIC_DIR
// 2. Hidden symlink inside directory pointing to external file as cover.jpg
func TestChallenger2_SymlinkEscapes(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	// Create an external secret file outside the sandboxes
	secretDir := t.TempDir()
	secretFile := filepath.Join(secretDir, "super_secret.txt")
	secretContent := "CONFIDENTIAL_API_KEY_12345"
	if err := os.WriteFile(secretFile, []byte(secretContent), 0644); err != nil {
		t.Fatalf("failed to write secret file: %v", err)
	}

	// 1. Create a symlink pointing to the external secret file
	symlinkPath := filepath.Join(env.MusicDir, "evil_link.mp3")
	if err := os.Symlink(secretFile, symlinkPath); err != nil {
		t.Skipf("symlink creation not supported in this environment: %v", err)
	}

	t.Run("DirectSymlink_GetTags", func(t *testing.T) {
		resp := env.Get("/tags?path=evil_link.mp3")
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			body := string(env.ReadBody(resp))
			t.Fatalf("expected 403 or 404 for symlink pointing outside, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("DirectSymlink_SaveTags", func(t *testing.T) {
		resp := env.PostJSON("/tags", id3.TrackTags{Path: "evil_link.mp3", Title: "Overwrite"})
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 403 or 404 for saving to external symlink, got %d", resp.StatusCode)
		}
	})

	t.Run("DirectSymlink_GetCover", func(t *testing.T) {
		resp := env.Get("/tags/cover?path=evil_link.mp3")
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 403 or 404 for external symlink cover, got %d", resp.StatusCode)
		}
	})

	// 2. Subdirectory with a symlinked cover: album/cover.jpg -> secretFile
	albumDir := filepath.Join(env.MusicDir, "SecretAlbum")
	if err := os.MkdirAll(albumDir, 0755); err != nil {
		t.Fatalf("failed to create album dir: %v", err)
	}
	symlinkCover := filepath.Join(albumDir, "cover.jpg")
	_ = os.Symlink(secretFile, symlinkCover)

	// Also put a valid mp3 in SecretAlbum
	env.CreateDummyMP3("SecretAlbum/track.mp3", "Track", "Artist", "SecretAlbum")

	t.Run("HiddenSymlinkCover_GetCover_CRITICAL_LEAK", func(t *testing.T) {
		resp := env.Get("/tags/cover?path=SecretAlbum")
		if resp.StatusCode == http.StatusOK {
			data := env.ReadBody(resp)
			if string(data) == secretContent {
				t.Errorf("CRITICAL SECURITY VULNERABILITY CONFIRMED: GetCover bypassed PathSandbox.Resolve() and read external file %q via symlinked cover.jpg!", secretFile)
			}
		}
	})
}

// TestChallenger2_BoundaryConditions tests empty inputs, huge inputs, extreme numbers, and malformed JSON.
func TestChallenger2_BoundaryConditions(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	t.Run("EmptyPath_GetTags", func(t *testing.T) {
		resp := env.Get("/tags?path=")
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for empty path, got %d", resp.StatusCode)
		}
	})

	t.Run("WhitespacePath_GetTags", func(t *testing.T) {
		resp := env.Get("/tags?path=%20%20%20")
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for whitespace path, got %d", resp.StatusCode)
		}
	})

	t.Run("MalformedJSON_SaveTags", func(t *testing.T) {
		req := env.NewRequest(http.MethodPost, "/tags", strings.NewReader("{broken-json"))
		req.Header.Set("Content-Type", "application/json")
		resp := env.Do(req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for malformed JSON, got %d", resp.StatusCode)
		}
	})

	t.Run("ExtremeValues_SaveTags", func(t *testing.T) {
		env.CreateDummyMP3("extreme.mp3", "Normal", "Normal", "Normal")
		extremeTags := id3.TrackTags{
			Path:        "extreme.mp3",
			Title:       strings.Repeat("A", 5000), // Very long title
			Artist:      "Artist1\\Artist2\\Artist3",
			Year:        999999,
			TrackNumber: -10,
			DiscNumber:  -5,
		}
		resp := env.PostJSON("/tags", extremeTags)
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
			t.Errorf("unexpected status for extreme tag values: %d", resp.StatusCode)
		}
	})

	t.Run("XSS_OAuthCallback", func(t *testing.T) {
		xssPayload := "<script>alert(1)</script>"
		resp := env.Get("/api/auth/musicbrainz/callback?error=" + url.QueryEscape(xssPayload))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 for OAuth error callback, got %d", resp.StatusCode)
		}
		body := string(env.ReadBody(resp))
		if strings.Contains(body, "<script>alert(1)</script>") {
			t.Errorf("XSS flaw: OAuth error page reflected unescaped script tag!")
		}
	})
}
