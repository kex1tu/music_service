package downloader_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"music-manager/internal/services/resolver"
)

func TestSoundCloudSearchRoutesAndPagination(t *testing.T) {
	env, mod := setupTestModule(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte("<html></html>"))
			return
		}
		if r.URL.Query().Get("limit") != "10" {
			t.Errorf("wrong upstream limit: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("q") != "Artist" {
			t.Errorf("wrong query: %s", r.URL.RawQuery)
		}
		if r.URL.Path == "/search/albums" && r.URL.Query().Get("offset") != "10" {
			t.Errorf("wrong album offset: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"collection": []map[string]any{{
			"id": 1, "title": "Result", "permalink_url": "https://soundcloud.com/artist/result", "track_count": 3,
		}}})
	}))
	defer upstream.Close()
	sc := resolver.NewSoundCloudSetSearcher("")
	sc.SetBaseURLs(upstream.URL, upstream.URL)
	sc.SetCustomHTTPClient(upstream.Client())
	mod.Service().Resolver().SetFullReleaseMatcher(resolver.NewFullReleaseMatcher(nil, sc))
	for _, prefix := range []string{"/downloads", "/api/downloads"} {
		response := env.Get(prefix + "/soundcloud/search?q=Artist&album_offset=10")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s returned %d: %s", prefix, response.StatusCode, env.ReadBody(response))
		}
		var body struct {
			Groups []resolver.SoundCloudSearchGroup `json:"groups"`
		}
		env.DecodeJSON(response, &body)
		if len(body.Groups) != 3 {
			t.Fatalf("expected 3 groups, got %+v", body)
		}
		for _, group := range body.Groups {
			if len(group.Items) != 1 || group.NextOffset != -1 {
				t.Errorf("unexpected group %+v", group)
			}
		}
	}
	for _, query := range []string{"", "?q=Artist&track_offset=bad", "?q=Artist&album_offset=-2", "?q=Artist&playlist_offset=10001"} {
		response := env.Get("/api/downloads/soundcloud/search" + query)
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s returned %d", query, response.StatusCode)
		}
		_ = response.Body.Close()
	}
	env.ClearAuth()
	response := env.Get("/api/downloads/soundcloud/search?q=Artist")
	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", response.StatusCode)
	}
	_ = response.Body.Close()
}
