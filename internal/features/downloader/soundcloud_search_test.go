package downloader_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestSoundCloudSearch_TokenContinuationAndTypeFilter(t *testing.T) {
	env, mod := setupTestModule(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte("<html></html>"))
			return
		}
		endpoint := r.URL.Path
		nextURL := fmt.Sprintf("%s%s?client_id=fixture-client&offset=20&linked_partitioning=1&q=Artist", r.Host, endpoint)
		if !strings.HasPrefix(nextURL, "http") {
			nextURL = "http://" + nextURL
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"collection": []map[string]any{
				{"id": 42, "title": "Track 42", "permalink_url": "https://soundcloud.com/artist/track-42", "track_count": 1},
			},
			"next_href": nextURL,
		})
	}))
	defer upstream.Close()

	sc := resolver.NewSoundCloudSetSearcher("")
	sc.SetBaseURLs(upstream.URL, upstream.URL)
	sc.SetCustomHTTPClient(upstream.Client())
	mod.Service().Resolver().SetFullReleaseMatcher(resolver.NewFullReleaseMatcher(nil, sc))

	// Запрос только треков: type=track
	respTrackOnly := env.Get("/api/downloads/soundcloud/search?q=Artist&type=track")
	if respTrackOnly.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", respTrackOnly.StatusCode)
	}
	var trackBody struct {
		Groups []resolver.SoundCloudSearchGroup `json:"groups"`
	}
	env.DecodeJSON(respTrackOnly, &trackBody)
	if len(trackBody.Groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(trackBody.Groups))
	}
	if len(trackBody.Groups[0].Items) != 1 || trackBody.Groups[0].NextToken == "" {
		t.Fatalf("expected 1 track and continuation token, got %+v", trackBody.Groups[0])
	}
	if len(trackBody.Groups[1].Items) != 0 || len(trackBody.Groups[2].Items) != 0 {
		t.Fatalf("expected album and playlist to be skipped when type=track")
	}

	trackToken := trackBody.Groups[0].NextToken

	// Продолжение через track_token
	respNext := env.Get("/api/downloads/soundcloud/search?q=Artist&type=track&track_token=" + trackToken)
	if respNext.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on valid token, got %d", respNext.StatusCode)
	}

	// Подмена категории в токене: передаем trackToken как album_token для type=album
	respMismatch := env.Get("/api/downloads/soundcloud/search?q=Artist&type=album&album_token=" + trackToken)
	if respMismatch.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on category mismatch, got %d", respMismatch.StatusCode)
	}
	_ = respMismatch.Body.Close()

	// Подмена запроса: токен получен для Artist, а запрашивается Other
	respQueryMismatch := env.Get("/api/downloads/soundcloud/search?q=Other&type=track&track_token=" + trackToken)
	if respQueryMismatch.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on query mismatch, got %d", respQueryMismatch.StatusCode)
	}
	_ = respQueryMismatch.Body.Close()
}

func TestSoundCloudReleaseCandidates_PagedContinuation(t *testing.T) {
	env, mod := setupTestModule(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte("<html></html>"))
			return
		}
		endpoint := r.URL.Path
		offset := r.URL.Query().Get("offset")
		nextOff := 10
		if offset == "10" {
			nextOff = 20
		}
		nextURL := fmt.Sprintf("%s%s?client_id=fixture-client&offset=%d&linked_partitioning=1&q=Artist+Album", r.Host, endpoint, nextOff)
		if !strings.HasPrefix(nextURL, "http") {
			nextURL = "http://" + nextURL
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"collection": []map[string]any{
				{
					"id": 101, "title": "Album " + offset,
					"permalink_url": fmt.Sprintf("https://soundcloud.com/artist/album-%s", offset),
					"track_count":   1,
					"tracks": []map[string]any{
						{"id": 201, "title": "Track 1", "permalink_url": "https://soundcloud.com/artist/t1"},
					},
				},
			},
			"next_href": nextURL,
		})
	}))
	defer upstream.Close()

	sc := resolver.NewSoundCloudSetSearcher("")
	sc.SetBaseURLs(upstream.URL, upstream.URL)
	sc.SetCustomHTTPClient(upstream.Client())
	mod.Service().Resolver().SetFullReleaseMatcher(resolver.NewFullReleaseMatcher(nil, sc))

	// Страница 1
	resp1 := env.Get("/api/downloads/release-candidates?artist=Artist&album=Album")
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("page 1 failed: %d", resp1.StatusCode)
	}
	var body1 struct {
		HasMoreSoundCloud bool                            `json:"has_more_soundcloud"`
		AlbumOffset       int                             `json:"album_offset"`
		PlaylistOffset    int                             `json:"playlist_offset"`
		AlbumToken        string                          `json:"album_token"`
		PlaylistToken     string                          `json:"playlist_token"`
		Candidates        []resolver.ReleaseCandidateItem `json:"candidates"`
	}
	env.DecodeJSON(resp1, &body1)
	if !body1.HasMoreSoundCloud {
		t.Fatalf("expected HasMoreSoundCloud to be true")
	}
	if body1.AlbumOffset != 10 || body1.AlbumToken == "" {
		t.Fatalf("expected album continuation params, got off=%d tok=%s", body1.AlbumOffset, body1.AlbumToken)
	}

	// Страница 2: передаем album_offset и album_token
	resp2 := env.Get(fmt.Sprintf("/api/downloads/release-candidates?artist=Artist&album=Album&album_offset=%d&album_token=%s&playlist_offset=%d&playlist_token=%s",
		body1.AlbumOffset, body1.AlbumToken, body1.PlaylistOffset, body1.PlaylistToken))
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("page 2 failed: %d", resp2.StatusCode)
	}
	var body2 struct {
		HasMoreSoundCloud bool                            `json:"has_more_soundcloud"`
		AlbumOffset       int                             `json:"album_offset"`
		Candidates        []resolver.ReleaseCandidateItem `json:"candidates"`
	}
	env.DecodeJSON(resp2, &body2)
	if !body2.HasMoreSoundCloud {
		t.Fatalf("expected HasMoreSoundCloud on page 2")
	}
	if body2.AlbumOffset != 20 {
		t.Fatalf("expected album offset 20 on page 2, got %d", body2.AlbumOffset)
	}
}
