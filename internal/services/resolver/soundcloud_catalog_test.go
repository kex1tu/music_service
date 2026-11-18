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
	if len(groups[0].Items) != 8 || groups[0].NextOffset != -1 || len(groups[1].Items) != 0 || groups[1].NextOffset != -1 {
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

func TestSoundCloudCatalog_NonArithmeticContinuationAndPageSizeIndependence(t *testing.T) {
	var requestedOffsets []string
	var requestedCursors []string
	s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) {
		requestedOffsets = append(requestedOffsets, r.URL.Query().Get("offset"))
		requestedCursors = append(requestedCursors, r.URL.Query().Get("cursor"))

		cursor := r.URL.Query().Get("cursor")
		offset := r.URL.Query().Get("offset")

		var page scAPISearchResponse
		if cursor == "cursor_page_3" {
			// Третья страница — финал, NextHref пустой
			page.Collection = append(page.Collection, scFixtureItem("track", 100))
			page.NextHref = ""
		} else if offset == "25" {
			// Вторая страница — вернули 2 элемента (меньше page size!), но с cursor в NextHref
			page.Collection = append(page.Collection, scFixtureItem("track", 20))
			page.Collection = append(page.Collection, scFixtureItem("track", 21))
			page.NextHref = fmt.Sprintf("%s/search/tracks?client_id=fixture-client&cursor=cursor_page_3&linked_partitioning=1&q=Artist", r.Host)
			if !strings.HasPrefix(page.NextHref, "http") {
				page.NextHref = "http://" + page.NextHref
			}
		} else {
			// Первая страница — неарифметический offset=25 в NextHref
			page.Collection = append(page.Collection, scFixtureItem("track", 1))
			page.NextHref = fmt.Sprintf("%s/search/tracks?client_id=fixture-client&offset=25&linked_partitioning=1&q=Artist", r.Host)
			if !strings.HasPrefix(page.NextHref, "http") {
				page.NextHref = "http://" + page.NextHref
			}
		}
		_ = json.NewEncoder(w).Encode(page)
	})

	// Страница 1
	groups1, err := s.SearchCatalog(context.Background(), "Artist", [3]int{0, -1, -1})
	if err != nil {
		t.Fatalf("page 1 failed: %v", err)
	}
	if groups1[0].NextOffset != 25 || groups1[0].NextToken == "" {
		t.Fatalf("expected next_offset=25 and non-empty token, got offset=%d token=%q", groups1[0].NextOffset, groups1[0].NextToken)
	}

	// Страница 2 через token
	groups2, err := s.SearchCatalogWithTokens(context.Background(), "Artist", [3]int{0, -1, -1}, [3]string{groups1[0].NextToken, "", ""})
	if err != nil {
		t.Fatalf("page 2 failed: %v", err)
	}
	if len(groups2[0].Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(groups2[0].Items))
	}
	if groups2[0].NextToken == "" {
		t.Fatalf("expected continuation token with cursor for page 3, got empty")
	}

	// Страница 3 через cursor token
	groups3, err := s.SearchCatalogWithTokens(context.Background(), "Artist", [3]int{0, -1, -1}, [3]string{groups2[0].NextToken, "", ""})
	if err != nil {
		t.Fatalf("page 3 failed: %v", err)
	}
	if len(groups3[0].Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(groups3[0].Items))
	}
	if groups3[0].NextOffset != -1 || groups3[0].NextToken != "" {
		t.Fatalf("expected end of stream (offset -1, empty token), got offset=%d token=%q", groups3[0].NextOffset, groups3[0].NextToken)
	}
}

func TestSoundCloudCatalog_DeduplicationAndZeroFilteredDoesNotLoseContinuation(t *testing.T) {
	s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) {
		page := scAPISearchResponse{
			NextHref: fmt.Sprintf("%s/search/tracks?client_id=fixture-client&offset=50&linked_partitioning=1&q=Artist", r.Host),
		}
		if !strings.HasPrefix(page.NextHref, "http") {
			page.NextHref = "http://" + page.NextHref
		}
		// Все элементы недопустимы (чужой хост / пустой заголовок)
		itemBadHost := scFixtureItem("track", 1)
		itemBadHost.PermalinkURL = "https://malicious.attacker.com/track"
		itemEmptyTitle := scFixtureItem("track", 2)
		itemEmptyTitle.Title = ""
		page.Collection = []scAPIAlbumItem{itemBadHost, itemEmptyTitle}

		_ = json.NewEncoder(w).Encode(page)
	})

	groups, err := s.SearchCatalog(context.Background(), "Artist", [3]int{0, -1, -1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups[0].Items) != 0 {
		t.Fatalf("expected 0 valid items, got %d", len(groups[0].Items))
	}
	if groups[0].NextOffset != 50 || groups[0].NextToken == "" {
		t.Fatalf("continuation lost after filtering: offset=%d, token=%q", groups[0].NextOffset, groups[0].NextToken)
	}
}

func TestSoundCloudCatalog_MaliciousNextHref_Cycle_And_TokenTampering(t *testing.T) {
	t.Run("malicious next_href rejection", func(t *testing.T) {
		for _, badHref := range []string{
			"javascript:alert(1)",
			"file:///etc/passwd",
			"https://attacker.invalid/search/tracks",
			"http://169.254.169.254/latest/meta-data",
			"https://user:pass@soundcloud.com/search/tracks",
		} {
			s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(scAPISearchResponse{
					Collection: []scAPIAlbumItem{scFixtureItem("track", 1)},
					NextHref:   badHref,
				})
			})
			groups, err := s.SearchCatalog(context.Background(), "Artist", [3]int{0, -1, -1})
			if err != nil {
				t.Fatalf("unexpected error on bad next_href: %v", err)
			}
			if groups[0].NextToken != "" {
				t.Errorf("token generated for malicious next_href %q: %s", badHref, groups[0].NextToken)
			}
		}
	})

	t.Run("cyclic continuation detection", func(t *testing.T) {
		s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) {
			page := scAPISearchResponse{
				Collection: []scAPIAlbumItem{scFixtureItem("track", 1)},
				NextHref:   fmt.Sprintf("%s/search/tracks?client_id=fixture-client&offset=10&linked_partitioning=1&q=Artist", r.Host),
			}
			if !strings.HasPrefix(page.NextHref, "http") {
				page.NextHref = "http://" + page.NextHref
			}
			_ = json.NewEncoder(w).Encode(page)
		})
		// Запрос уже на offset=10, и NextHref вернул снова offset=10
		groups, err := s.SearchCatalog(context.Background(), "Artist", [3]int{10, -1, -1})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if groups[0].NextToken != "" {
			t.Errorf("cyclic next_href was not terminated: token=%s", groups[0].NextToken)
		}
	})

	t.Run("token tampering and category/query mismatch", func(t *testing.T) {
		s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) {})
		token := s.createContinuationToken("album", "Queen", 20, "")

		// Подмена категории: токен альбома передан как токен трека
		_, err := s.SearchCatalogWithTokens(context.Background(), "Queen", [3]int{0, -1, -1}, [3]string{token, "", ""})
		if err == nil || !strings.Contains(err.Error(), "category mismatch") {
			t.Errorf("expected category mismatch error, got %v", err)
		}

		// Подмена запроса: токен для Queen передан с запросом Beatles
		_, err = s.SearchCatalogWithTokens(context.Background(), "Beatles", [3]int{-1, 0, -1}, [3]string{"", token, ""})
		if err == nil || !strings.Contains(err.Error(), "query mismatch") {
			t.Errorf("expected query mismatch error, got %v", err)
		}

		// Поддельная подпись
		tamperedToken := token[:len(token)-5] + "aaaaa"
		_, err = s.SearchCatalogWithTokens(context.Background(), "Queen", [3]int{-1, 0, -1}, [3]string{"", tamperedToken, ""})
		if err == nil || !strings.Contains(err.Error(), "signature") {
			t.Errorf("expected invalid signature error, got %v", err)
		}
	})
}

func TestSoundCloudCatalog_RateLimit429_And_SingleRefresh401(t *testing.T) {
	t.Run("429 rate limit error preserves offset for retry", func(t *testing.T) {
		s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
		groups, err := s.SearchCatalog(context.Background(), "Artist", [3]int{20, -1, -1})
		if err == nil {
			t.Fatal("expected error on total 429")
		}
		if groups != nil && groups[0].NextOffset != 20 {
			t.Errorf("offset not preserved on 429: %d", groups[0].NextOffset)
		}
	})

	t.Run("401 single retry with client_id refresh", func(t *testing.T) {
		var calls int
		s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.URL.Path == "/" {
				_, _ = w.Write([]byte(`<html><script src="/app.js"></script></html>`))
				return
			}
			if r.URL.Path == "/app.js" {
				_, _ = w.Write([]byte(`client_id:"0123456789abcdef0123456789abcdef"`))
				return
			}
			if calls == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(scAPISearchResponse{
				Collection: []scAPIAlbumItem{scFixtureItem("track", 1)},
			})
		})
		groups, err := s.SearchCatalog(context.Background(), "Artist", [3]int{0, -1, -1})
		if err != nil {
			t.Fatalf("expected successful recovery after 401: %v", err)
		}
		if len(groups[0].Items) != 1 {
			t.Errorf("expected 1 item after refresh, got %d", len(groups[0].Items))
		}
	})
}

func TestSoundCloudSetsAPIPaged_HasMoreSoundCloudIndependentOfTracklistHydration(t *testing.T) {
	s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) {
		nextURL := fmt.Sprintf("%s%s?client_id=fixture-client&offset=10&linked_partitioning=1&q=Artist+Album", r.Host, r.URL.Path)
		if !strings.HasPrefix(nextURL, "http") {
			nextURL = "http://" + nextURL
		}
		// Альбомы без треков (отфильтруются convertAlbumToCandidate)
		emptyAlbum := scFixtureItem("album", 1)
		emptyAlbum.Tracks = []scAPITrackItem{}
		page := scAPISearchResponse{
			Collection: []scAPIAlbumItem{emptyAlbum},
			NextHref:   nextURL,
		}
		_ = json.NewEncoder(w).Encode(page)
	})

	res, err := s.SearchSetsAPIPaged(context.Background(), "Artist Album", 0, 0, "", "", 10)
	if err != nil {
		t.Fatalf("SearchSetsAPIPaged error: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Fatalf("expected 0 candidates after track filtering, got %d", len(res.Candidates))
	}
	if !res.HasMoreSoundCloud {
		t.Fatalf("HasMoreSoundCloud must be true based on upstream NextHref, got false")
	}
	if res.AlbumOffset != 10 || res.PlaylistOffset != 10 {
		t.Errorf("expected offsets 10, got album=%d playlist=%d", res.AlbumOffset, res.PlaylistOffset)
	}
	if res.AlbumToken == "" || res.PlaylistToken == "" {
		t.Errorf("expected non-empty continuation tokens")
	}
}

func TestSoundCloudSetsAPIPaged_SequentialPaginationBeyond100(t *testing.T) {
	var requestedOffsets []string
	s := mockSoundCloudSearch(t, func(w http.ResponseWriter, r *http.Request) {
		offStr := r.URL.Query().Get("offset")
		requestedOffsets = append(requestedOffsets, offStr)
		off, _ := strconv.Atoi(offStr)

		nextOff := off + 10
		nextURL := fmt.Sprintf("%s%s?client_id=fixture-client&offset=%d&linked_partitioning=1&q=Artist", r.Host, r.URL.Path, nextOff)
		if !strings.HasPrefix(nextURL, "http") {
			nextURL = "http://" + nextURL
		}
		item := scFixtureItem("album", off)
		item.Tracks = []scAPITrackItem{{ID: int64(off + 1), Title: fmt.Sprintf("Track %d", off), PermalinkURL: fmt.Sprintf("https://soundcloud.com/artist/t-%d", off)}}
		page := scAPISearchResponse{
			Collection: []scAPIAlbumItem{item},
			NextHref:   nextURL,
		}
		_ = json.NewEncoder(w).Encode(page)
	})

	albumOff, playlistOff := 0, 0
	albumTok, playlistTok := "", ""
	totalCandidates := 0

	for page := 0; page < 12; page++ {
		res, err := s.SearchSetsAPIPaged(context.Background(), "Artist", albumOff, playlistOff, albumTok, playlistTok, 10)
		if err != nil {
			t.Fatalf("page %d error: %v", page, err)
		}
		totalCandidates += len(res.Candidates)
		if !res.HasMoreSoundCloud {
			t.Fatalf("page %d lost HasMoreSoundCloud", page)
		}
		albumOff = res.AlbumOffset
		playlistOff = res.PlaylistOffset
		albumTok = res.AlbumToken
		playlistTok = res.PlaylistToken
	}

	if totalCandidates < 12 {
		t.Errorf("expected sequential candidate accumulation, got %d", totalCandidates)
	}
	// Убеждаемся, что смещения продвигались вперед (0, 10, 20, ...)
	if albumOff < 120 {
		t.Errorf("album offset did not advance past 100: %d", albumOff)
	}
}
