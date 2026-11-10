package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func mockSoundCloudSearch(t *testing.T, handler http.HandlerFunc) *SoundCloudSetSearcher {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	s := NewSoundCloudSetSearcher("")
	s.SetBaseURLs(server.URL, server.URL)
	s.SetCustomHTTPClient(server.Client())
	s.clientID = "fixture-client"
	s.clientIDExp = time.Now().Add(time.Hour)
	return s
}

func scFixtureItem(kind string, i int) scAPIAlbumItem {
	return scAPIAlbumItem{
		ID: int64(i + 1), Title: fmt.Sprintf("%s %d", kind, i),
		PermalinkURL: fmt.Sprintf("https://soundcloud.com/artist/%s-%d", kind, i),
		TrackCount:   1, User: scAPIUser{Username: "Artist"},
		Tracks: []scAPITrackItem{{ID: int64(i + 1), Title: "Song", PermalinkURL: "https://soundcloud.com/artist/song"}},
	}
}

func TestSoundCloudSearchSetsBalancedExpandedLimits(t *testing.T) {
	for _, requested := range []int{0, -1, 5, 30, 1000} {
		t.Run(strconv.Itoa(requested), func(t *testing.T) {
			limit := SoundCloudCandidateLimit(requested)
			s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("limit") != strconv.Itoa(limit) {
					t.Errorf("upstream limit = %s, want %d", r.URL.Query().Get("limit"), limit)
				}
				kind := "album"
				if strings.Contains(r.URL.Path, "playlists") {
					kind = "playlist"
				}
				page := scAPISearchResponse{}
				for i := 0; i < limit; i++ {
					page.Collection = append(page.Collection, scFixtureItem(kind, i))
				}
				_ = json.NewEncoder(w).Encode(page)
			})
			items, err := s.SearchSetsAPI(context.Background(), "artist release", requested)
			if err != nil || len(items) != limit {
				t.Fatalf("got %d results, error %v; want %d", len(items), err, limit)
			}
			for i, item := range items {
				wantType := "album"
				if i%2 == 1 {
					wantType = "playlist"
				}
				if item.ResultType != wantType {
					t.Errorf("result %d type %s, want %s", i, item.ResultType, wantType)
				}
			}
		})
	}
}

func TestSoundCloudSearchSetsFailures(t *testing.T) {
	for _, failBoth := range []bool{false, true} {
		t.Run(strconv.FormatBool(failBoth), func(t *testing.T) {
			s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) {
				if failBoth || r.URL.Path == "/search/albums" {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				_ = json.NewEncoder(w).Encode(scAPISearchResponse{Collection: []scAPIAlbumItem{scFixtureItem("playlist", 1)}})
			})
			items, err := s.SearchSetsAPI(context.Background(), "artist release", 30)
			if failBoth {
				if err == nil {
					t.Fatal("expected error on total failure")
				}
				return
			}
			if err != nil || len(items) != 1 || items[0].ResultType != "playlist" {
				t.Fatalf("partial success lost: %+v %v", items, err)
			}
		})
	}
}

func TestSoundCloudSearchSetsDeduplicateBeforeHydration(t *testing.T) {
	var hydrationCalls int
	s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tracks" {
			hydrationCalls++
			if r.URL.Query().Get("ids") != "42" {
				t.Errorf("duplicate hydration IDs: %s", r.URL.Query().Get("ids"))
			}
			_ = json.NewEncoder(w).Encode([]scAPITrackItem{{ID: 42, Title: "Song", PermalinkURL: "https://soundcloud.com/artist/song"}})
			return
		}
		item := scFixtureItem("album", 1)
		item.Tracks = []scAPITrackItem{{ID: 42}, {ID: 42}}
		_ = json.NewEncoder(w).Encode(scAPISearchResponse{Collection: []scAPIAlbumItem{item, item}})
	})
	items, err := s.SearchSetsAPI(context.Background(), "artist release", 30)
	if err != nil || len(items) != 1 || hydrationCalls != 1 {
		t.Fatalf("dedup failed: %d items, %d hydration calls, %v", len(items), hydrationCalls, err)
	}
}

func TestSoundCloudCatalogPaginationAndSafety(t *testing.T) {
	s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/tracks" && r.URL.Path != "/search/playlists_without_albums" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		if r.URL.Query().Get("limit") != "10" || r.URL.Query().Get("offset") != "10" {
			t.Errorf("wrong pagination: %s", r.URL.RawQuery)
		}
		page := scAPISearchResponse{NextHref: "https://attacker.invalid/never-follow-this"}
		for i := 10; i < 20; i++ {
			page.Collection = append(page.Collection, scFixtureItem("track", i))
		}
		page.Collection[1].PermalinkURL = "https://soundcloud.com.attacker.invalid/a"
		page.Collection[2].PermalinkURL = page.Collection[0].PermalinkURL
		_ = json.NewEncoder(w).Encode(page)
	})
	groups, err := s.SearchCatalog(context.Background(), "Artist & track", [3]int{10, -1, 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups[0].Items) != 8 || groups[0].NextOffset != 20 || len(groups[1].Items) != 0 || groups[1].NextOffset != -1 {
		t.Fatalf("wrong page: %+v", groups)
	}
}

func TestSoundCloudCatalogPartialFailure(t *testing.T) {
	s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/albums" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(scAPISearchResponse{Collection: []scAPIAlbumItem{scFixtureItem("result", 1)}})
	})
	groups, err := s.SearchCatalog(context.Background(), "Artist", [3]int{})
	if err != nil || len(groups) != 3 || len(groups[0].Items) != 1 || groups[1].Error == "" || groups[1].NextOffset != 0 {
		t.Fatalf("partial failure: %+v, %v", groups, err)
	}
}

func TestSoundCloudCatalogValidation(t *testing.T) {
	s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid input reached network") })
	for _, query := range []string{"", strings.Repeat("я", 201)} {
		if _, err := s.SearchCatalog(context.Background(), query, [3]int{}); err == nil {
			t.Error("expected query error")
		}
	}
	if _, err := s.SearchCatalog(context.Background(), "Artist", [3]int{-2}); err == nil {
		t.Error("expected offset error")
	}
}

func TestSoundCloudCatalogPartialTimeout(t *testing.T) {
	s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/albums" {
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(scAPISearchResponse{Collection: []scAPIAlbumItem{scFixtureItem("result", 1)}})
	})
	s.httpClient.Timeout = 100 * time.Millisecond
	groups, err := s.SearchCatalog(context.Background(), "Artist", [3]int{})
	if err != nil || len(groups) != 3 || len(groups[0].Items) != 1 || groups[1].Error == "" || len(groups[2].Items) != 1 {
		t.Fatalf("partial timeout discarded successful groups: %+v, %v", groups, err)
	}
}
