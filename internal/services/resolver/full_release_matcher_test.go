package resolver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFullReleaseMatcher_MatchTracklists_Perfect(t *testing.T) {
	matcher := NewFullReleaseMatcher(nil)

	targetTracks := []ResolvedTrack{
		{TrackID: "dz_1", TrackNumber: 1, Title: "Банкомат", Artist: "DEAD BLONDE", Duration: 200},
		{TrackID: "dz_2", TrackNumber: 2, Title: "Красный свет", Artist: "DEAD BLONDE", Duration: 198},
		{TrackID: "dz_3", TrackNumber: 3, Title: "Самолёт", Artist: "DEAD BLONDE", Duration: 182},
		{TrackID: "dz_4", TrackNumber: 4, Title: "Без шансов", Artist: "DEAD BLONDE", Duration: 214},
		{TrackID: "dz_5", TrackNumber: 5, Title: "Beauty Queen", Artist: "DEAD BLONDE", Duration: 206},
		{TrackID: "dz_6", TrackNumber: 6, Title: "VIP CLUB", Artist: "DEAD BLONDE", Duration: 151},
		{TrackID: "dz_7", TrackNumber: 7, Title: "Всего лишь друг", Artist: "DEAD BLONDE", Duration: 209},
		{TrackID: "dz_8", TrackNumber: 8, Title: "Мираж", Artist: "DEAD BLONDE", Duration: 199},
	}

	candTracks := []AlbumTrackCandidate{
		{VideoID: "v1", Title: "Банкомат", Duration: 200, Index: 1},
		{VideoID: "v2", Title: "Красный свет", Duration: 198, Index: 2},
		{VideoID: "v3", Title: "Самолёт", Duration: 182, Index: 3},
		{VideoID: "v4", Title: "Без шансов", Duration: 214, Index: 4},
		{VideoID: "v5", Title: "Beauty Queen", Duration: 206, Index: 5},
		{VideoID: "v6", Title: "VIP CLUB", Duration: 151, Index: 6},
		{VideoID: "v7", Title: "Всего лишь друг", Duration: 209, Index: 7},
		{VideoID: "v8", Title: "Мираж", Duration: 199, Index: 8},
	}

	urls, matched := matcher.MatchTracklists(targetTracks, candTracks)
	if matched != 8 {
		t.Fatalf("expected 8 matched tracks, got %d", matched)
	}
	if len(urls) != 8 {
		t.Fatalf("expected 8 urls, got %d", len(urls))
	}
	for i := 0; i < 8; i++ {
		expectedURL := fmt.Sprintf("https://music.youtube.com/watch?v=v%d", i+1)
		if urls[i] != expectedURL {
			t.Errorf("track %d URL = %q, want %q", i, urls[i], expectedURL)
		}
	}
}

func TestFullReleaseMatcher_IntroOrBonusMissing_Threshold85(t *testing.T) {
	matcher := NewFullReleaseMatcher(nil)

	// 8 target tracks
	targetTracks := []ResolvedTrack{
		{TrackID: "dz_1", TrackNumber: 1, Title: "Интро", Artist: "DEAD BLONDE", Duration: 45},
		{TrackID: "dz_2", TrackNumber: 2, Title: "Банкомат", Artist: "DEAD BLONDE", Duration: 200},
		{TrackID: "dz_3", TrackNumber: 3, Title: "Красный свет", Artist: "DEAD BLONDE", Duration: 198},
		{TrackID: "dz_4", TrackNumber: 4, Title: "Самолёт", Artist: "DEAD BLONDE", Duration: 182},
		{TrackID: "dz_5", TrackNumber: 5, Title: "Без шансов", Artist: "DEAD BLONDE", Duration: 214},
		{TrackID: "dz_6", TrackNumber: 6, Title: "Beauty Queen", Artist: "DEAD BLONDE", Duration: 206},
		{TrackID: "dz_7", TrackNumber: 7, Title: "VIP CLUB", Artist: "DEAD BLONDE", Duration: 151},
		{TrackID: "dz_8", TrackNumber: 8, Title: "Всего лишь друг", Artist: "DEAD BLONDE", Duration: 209},
	}

	// Candidate missing Intro, but has 7 remaining tracks (7/8 = 87.5% >= 85%)
	candTracks := []AlbumTrackCandidate{
		{VideoID: "v2", Title: "Банкомат", Duration: 201, Index: 1}, // 1 sec delta
		{VideoID: "v3", Title: "Красный свет", Duration: 198, Index: 2},
		{VideoID: "v4", Title: "Самолёт", Duration: 183, Index: 3}, // 1 sec delta
		{VideoID: "v5", Title: "Без шансов", Duration: 214, Index: 4},
		{VideoID: "v6", Title: "Beauty Queen (Explicit)", Duration: 206, Index: 5},
		{VideoID: "v7", Title: "VIP CLUB", Duration: 151, Index: 6},
		{VideoID: "v8", Title: "Всего лишь друг", Duration: 208, Index: 7},
	}

	urls, matched := matcher.MatchTracklists(targetTracks, candTracks)
	if matched != 7 {
		t.Fatalf("expected 7 matched tracks, got %d", matched)
	}
	ratio := float64(matched) / float64(len(targetTracks))
	if ratio < 0.85 {
		t.Fatalf("expected ratio >= 0.85, got %f", ratio)
	}
	if _, ok := urls[0]; ok {
		t.Errorf("track 0 (Intro) should not be matched")
	}
	if urls[1] != "https://music.youtube.com/watch?v=v2" {
		t.Errorf("track 1 (Банкомат) URL = %q, want https://music.youtube.com/watch?v=v2", urls[1])
	}
}

func TestFullReleaseMatcher_RejectWrongAlbum(t *testing.T) {
	matcher := NewFullReleaseMatcher(nil)

	// Target album: 8 tracks
	targetTracks := []ResolvedTrack{
		{TrackID: "dz_1", TrackNumber: 1, Title: "Банкомат", Artist: "DEAD BLONDE", Duration: 200},
		{TrackID: "dz_2", TrackNumber: 2, Title: "Красный свет", Artist: "DEAD BLONDE", Duration: 198},
		{TrackID: "dz_3", TrackNumber: 3, Title: "Самолёт", Artist: "DEAD BLONDE", Duration: 182},
		{TrackID: "dz_4", TrackNumber: 4, Title: "Без шансов", Artist: "DEAD BLONDE", Duration: 214},
		{TrackID: "dz_5", TrackNumber: 5, Title: "Beauty Queen", Artist: "DEAD BLONDE", Duration: 206},
		{TrackID: "dz_6", TrackNumber: 6, Title: "VIP CLUB", Artist: "DEAD BLONDE", Duration: 151},
		{TrackID: "dz_7", TrackNumber: 7, Title: "Всего лишь друг", Artist: "DEAD BLONDE", Duration: 209},
		{TrackID: "dz_8", TrackNumber: 8, Title: "Мираж", Artist: "DEAD BLONDE", Duration: 199},
	}

	// Candidate compilation with completely different songs except 1
	candTracks := []AlbumTrackCandidate{
		{VideoID: "v_other_1", Title: "Другая песня 1", Duration: 200, Index: 1},
		{VideoID: "v_other_2", Title: "Другая песня 2", Duration: 198, Index: 2},
		{VideoID: "v_other_3", Title: "Другая песня 3", Duration: 182, Index: 3},
		{VideoID: "v1", Title: "Банкомат", Duration: 200, Index: 4},
		{VideoID: "v_other_5", Title: "Другая песня 5", Duration: 206, Index: 5},
	}

	_, matched := matcher.MatchTracklists(targetTracks, candTracks)
	if matched != 1 {
		t.Fatalf("expected only 1 matched track, got %d", matched)
	}
	ratio := float64(matched) / float64(len(targetTracks))
	if ratio >= 0.85 {
		t.Fatalf("expected ratio < 0.85, got %f", ratio)
	}
}

func TestFullReleaseMatcher_DurationMismatchReject(t *testing.T) {
	matcher := NewFullReleaseMatcher(nil)

	// Target track duration 200s
	targetTracks := []ResolvedTrack{
		{TrackID: "dz_1", TrackNumber: 1, Title: "Банкомат", Artist: "DEAD BLONDE", Duration: 200},
	}

	// Candidate has same title but slowed version (250s, delta 50s > 12s)
	candTracks := []AlbumTrackCandidate{
		{VideoID: "v1_slow", Title: "Банкомат", Duration: 250, Index: 1},
	}

	_, matched := matcher.MatchTracklists(targetTracks, candTracks)
	if matched != 0 {
		t.Fatalf("expected 0 matches due to duration delta > 12s, got %d", matched)
	}
}

func TestFullReleaseMatcher_MatchFullRelease_Success(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/youtubei/v1/search" {
			w.Write([]byte(`{
				"contents": {
					"tabbedSearchResultsRenderer": {
						"tabs": [{
							"tabRenderer": {
								"content": {
									"sectionListRenderer": {
										"contents": [{
											"musicShelfRenderer": {
												"contents": [{
													"musicResponsiveListItemRenderer": {
														"navigationEndpoint": {
															"browseEndpoint": {
																"browseId": "MPREb_AbAianWC2U6"
															}
														},
														"overlay": {
															"musicItemThumbnailOverlayRenderer": {
																"content": {
																	"musicPlayButtonRenderer": {
																		"playNavigationEndpoint": {
																			"watchPlaylistEndpoint": {
																				"playlistId": "OLAK5uy_kY28dYd4bU5mCxgpXlRZPgpWnTXC2QB7A"
																			}
																		}
																	}
																}
															}
														},
														"flexColumns": [
															{
																"musicResponsiveListItemFlexColumnRenderer": {
																	"text": {
																		"runs": [{"text": "СПЛЕТНИЦА"}]
																	}
																}
															},
															{
																"musicResponsiveListItemFlexColumnRenderer": {
																	"text": {
																		"runs": [
																			{"text": "Альбом"},
																			{"text": " • "},
																			{"text": "DEAD BLONDE"},
																			{"text": " • "},
																			{"text": "2022"}
																		]
																	}
																}
															}
														]
													}
												}]
											}
										}]
									}
								}
							}
						}]
					}
				}
			}`))
			return
		}

		if r.URL.Path == "/youtubei/v1/browse" {
			w.Write([]byte(`{
				"contents": {
					"twoColumnBrowseResultsRenderer": {
						"secondaryContents": {
							"sectionListRenderer": {
								"contents": [{
									"musicShelfRenderer": {
										"contents": [
											{
												"musicResponsiveListItemRenderer": {
													"playlistItemData": {"videoId": "v1"},
													"flexColumns": [
														{"musicResponsiveListItemFlexColumnRenderer": {"text": {"runs": [{"text": "Банкомат"}]}}},
														{"musicResponsiveListItemFlexColumnRenderer": {"text": {"runs": [{"text": "DEAD BLONDE"}]}}}
													],
													"fixedColumns": [{"musicResponsiveListItemFixedColumnRenderer": {"text": {"runs": [{"text": "3:20"}]}}}]
												}
											},
											{
												"musicResponsiveListItemRenderer": {
													"playlistItemData": {"videoId": "v2"},
													"flexColumns": [
														{"musicResponsiveListItemFlexColumnRenderer": {"text": {"runs": [{"text": "Красный свет"}]}}},
														{"musicResponsiveListItemFlexColumnRenderer": {"text": {"runs": [{"text": "DEAD BLONDE"}]}}}
													],
													"fixedColumns": [{"musicResponsiveListItemFixedColumnRenderer": {"text": {"runs": [{"text": "3:18"}]}}}]
												}
											}
										]
									}
								}]
							}
						}
					}
				}
			}`))
			return
		}

		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	ytClient := NewYouTubeMusicClient(mockServer.URL)
	matcher := NewFullReleaseMatcher(ytClient)

	targetTracks := []ResolvedTrack{
		{TrackID: "dz_1", TrackNumber: 1, Title: "Банкомат", Artist: "DEAD BLONDE", Duration: 200},
		{TrackID: "dz_2", TrackNumber: 2, Title: "Красный свет", Artist: "DEAD BLONDE", Duration: 198},
	}

	res, err := matcher.MatchFullRelease(context.Background(), "DEAD BLONDE", "СПЛЕТНИЦА", targetTracks)
	if err != nil {
		t.Fatalf("MatchFullRelease error: %v", err)
	}
	if res == nil {
		t.Fatal("expected full release match, got nil")
	}
	if res.MatchedCount != 2 || res.TotalCount != 2 {
		t.Errorf("matched %d/%d, want 2/2", res.MatchedCount, res.TotalCount)
	}
	if res.TrackURLs[0] != "https://music.youtube.com/watch?v=v1" {
		t.Errorf("track 0 url = %q, want v1", res.TrackURLs[0])
	}
}

func TestCensorshipScoring_RussianArtist(t *testing.T) {
	// Для русскоязычных артистов:
	// YouTube Music официальный релиз -> red_flag, штраф
	// SoundCloud сет -> recommended, бонус

	isCyrillic := true
	matchedRatio := 1.0

	scoreYTM, statusYTM, badgeYTM := ScoreReleaseCandidate("ytmusic_album", isCyrillic, matchedRatio)
	if statusYTM != "red_flag" {
		t.Errorf("YTM status = %q, want 'red_flag'", statusYTM)
	}
	if !strings.Contains(badgeYTM, "цензура") {
		t.Errorf("YTM badge = %q, want warning about censorship", badgeYTM)
	}
	// 60 - 30 = 30
	if scoreYTM != 30 {
		t.Errorf("YTM score = %d, want 30", scoreYTM)
	}

	scoreSC, statusSC, badgeSC := ScoreReleaseCandidate("soundcloud_set", isCyrillic, matchedRatio)
	if statusSC != "recommended" {
		t.Errorf("SC status = %q, want 'recommended'", statusSC)
	}
	if !strings.Contains(badgeSC, "оригинал") {
		t.Errorf("SC badge = %q, want badge with original/uncensored", badgeSC)
	}
	// 60 + 35 = 95
	if scoreSC != 95 {
		t.Errorf("SC score = %d, want 95", scoreSC)
	}

	if scoreSC <= scoreYTM {
		t.Errorf("SoundCloud score (%d) should be strictly greater than YTM score (%d) for Russian artist", scoreSC, scoreYTM)
	}
}

func TestCensorshipScoring_ForeignArtist(t *testing.T) {
	// Для зарубежных артистов:
	// YouTube Music официальный релиз -> recommended, высший балл
	// SoundCloud сет -> neutral / штраф

	isCyrillic := false
	matchedRatio := 1.0

	scoreYTM, statusYTM, badgeYTM := ScoreReleaseCandidate("ytmusic_album", isCyrillic, matchedRatio)
	if statusYTM != "recommended" {
		t.Errorf("YTM status = %q, want 'recommended'", statusYTM)
	}
	if !strings.Contains(badgeYTM, "Официальный") {
		t.Errorf("YTM badge = %q, want official badge", badgeYTM)
	}
	// 60 + 40 = 100
	if scoreYTM != 100 {
		t.Errorf("YTM score = %d, want 100", scoreYTM)
	}

	scoreSC, statusSC, badgeSC := ScoreReleaseCandidate("soundcloud_set", isCyrillic, matchedRatio)
	if statusSC != "neutral" {
		t.Errorf("SC status = %q, want 'neutral'", statusSC)
	}
	if !strings.Contains(badgeSC, "Неофициальный") {
		t.Errorf("SC badge = %q, want 'Неофициальный источник'", badgeSC)
	}
	// 60 - 15 = 45
	if scoreSC != 45 {
		t.Errorf("SC score = %d, want 45", scoreSC)
	}

	if scoreYTM <= scoreSC {
		t.Errorf("YTM score (%d) should be strictly greater than SoundCloud score (%d) for foreign artist", scoreYTM, scoreSC)
	}
}

func TestContainsCyrillic(t *testing.T) {
	cases := []struct {
		input    string
		expected bool
	}{
		{"DEAD BLONDE", false},
		{"СПЛЕТНИЦА", true},
		{"Мальчик на девятке", true},
		{"21 Savage", false},
		{"Savage Mode II", false},
		{"Макс Корж", true},
		{"Clean Bandit", false},
	}

	for _, tc := range cases {
		got := ContainsCyrillic(tc.input)
		if got != tc.expected {
			t.Errorf("ContainsCyrillic(%q) = %v, want %v", tc.input, got, tc.expected)
		}
	}
}

func TestFullReleaseMatcher_SearchReleaseCandidates(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/youtubei/v1/search" {
			w.Write([]byte(`{
				"contents": {
					"tabbedSearchResultsRenderer": {
						"tabs": [{
							"tabRenderer": {
								"content": {
									"sectionListRenderer": {
										"contents": [{
											"musicShelfRenderer": {
												"contents": [{
													"musicResponsiveListItemRenderer": {
														"navigationEndpoint": {"browseEndpoint": {"browseId": "MPREb_AbAianWC2U6"}},
														"flexColumns": [
															{"musicResponsiveListItemFlexColumnRenderer": {"text": {"runs": [{"text": "СПЛЕТНИЦА"}]}}},
															{"musicResponsiveListItemFlexColumnRenderer": {"text": {"runs": [{"text": "DEAD BLONDE"}]}}}
														]
													}
												}]
											}
										}]
									}
								}
							}
						}]
					}
				}
			}`))
			return
		}
		if r.URL.Path == "/youtubei/v1/browse" {
			w.Write([]byte(`{
				"contents": {
					"twoColumnBrowseResultsRenderer": {
						"secondaryContents": {
							"sectionListRenderer": {
								"contents": [{
									"musicShelfRenderer": {
										"contents": [
											{
												"musicResponsiveListItemRenderer": {
													"playlistItemData": {"videoId": "v1"},
													"flexColumns": [{"musicResponsiveListItemFlexColumnRenderer": {"text": {"runs": [{"text": "Банкомат"}]}}}],
													"fixedColumns": [{"musicResponsiveListItemFixedColumnRenderer": {"text": {"runs": [{"text": "3:20"}]}}}]
												}
											},
											{
												"musicResponsiveListItemRenderer": {
													"playlistItemData": {"videoId": "v2"},
													"flexColumns": [{"musicResponsiveListItemFlexColumnRenderer": {"text": {"runs": [{"text": "Красный свет"}]}}}],
													"fixedColumns": [{"musicResponsiveListItemFixedColumnRenderer": {"text": {"runs": [{"text": "3:18"}]}}}]
												}
											}
										]
									}
								}]
							}
						}
					}
				}
			}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	ytClient := NewYouTubeMusicClient(mockServer.URL)
	matcher := NewFullReleaseMatcher(ytClient, nil)

	targetTracks := []ResolvedTrack{
		{TrackID: "dz_1", TrackNumber: 1, Title: "Банкомат", Artist: "DEAD BLONDE", Duration: 200},
		{TrackID: "dz_2", TrackNumber: 2, Title: "Красный свет", Artist: "DEAD BLONDE", Duration: 198},
	}

	cands, isCyrillic, err := matcher.SearchReleaseCandidates(context.Background(), "DEAD BLONDE", "СПЛЕТНИЦА", targetTracks)
	if err != nil {
		t.Fatalf("SearchReleaseCandidates error: %v", err)
	}
	if !isCyrillic {
		t.Errorf("expected isCyrillic=true for DEAD BLONDE - СПЛЕТНИЦА")
	}
	if len(cands) == 0 {
		t.Fatal("expected at least 1 candidate, got 0")
	}
	first := cands[0]
	if first.Title != "СПЛЕТНИЦА" {
		t.Errorf("candidate title = %q, want 'СПЛЕТНИЦА'", first.Title)
	}
	if first.MatchedCount != 2 {
		t.Errorf("candidate matched count = %d, want 2", first.MatchedCount)
	}
	if first.CensorStatus != "red_flag" {
		t.Errorf("candidate censor status = %q, want 'red_flag' for Cyrillic official release", first.CensorStatus)
	}
	if first.TrackMapping[0] != "https://music.youtube.com/watch?v=v1" {
		t.Errorf("track mapping 0 = %q, want v1", first.TrackMapping[0])
	}
}

func TestFullReleaseMatcher_CensorshipRanking_SoundCloudVsYTM(t *testing.T) {
	// 1. Тест для русского артиста: SoundCloud должен быть #1, YTM #2 с бейджем цензуры
	mockYTM := &YouTubeMusicClient{} // пустой, заменим mock-сервером
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/youtubei/v1/search" {
			w.Write([]byte(`{"contents":{"tabbedSearchResultsRenderer":{"tabs":[{"tabRenderer":{"content":{"sectionListRenderer":{"contents":[{"musicShelfRenderer":{"contents":[{"musicResponsiveListItemRenderer":{"navigationEndpoint":{"browseEndpoint":{"browseId":"MPREb_YTM"}},"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"СПЛЕТНИЦА"}]}}},{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"DEAD BLONDE"}]}}}]}}]}}]}}}}]}}}`))
			return
		}
		if r.URL.Path == "/youtubei/v1/browse" {
			w.Write([]byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"secondaryContents":{"sectionListRenderer":{"contents":[{"musicShelfRenderer":{"contents":[{"musicResponsiveListItemRenderer":{"playlistItemData":{"videoId":"ytm_1"},"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Банкомат"}]}}}],"fixedColumns":[{"musicResponsiveListItemFixedColumnRenderer":{"text":{"runs":[{"text":"3:20"}]}}}]}}]}}]}}}}}`))
			return
		}
	}))
	defer mockServer.Close()
	mockYTM = NewYouTubeMusicClient(mockServer.URL)

	scSearcher := NewSoundCloudSetSearcher("")
	scSearcher.SetCustomSearchFunc(func(ctx context.Context, artist, album string, limit int) ([]AlbumReleaseCandidate, error) {
		return []AlbumReleaseCandidate{
			{
				BrowseID:   "sc_spletnitsa",
				PlaylistID: "https://soundcloud.com/rusyarapscene/sets/spletnitsa",
				Title:      "СПЛЕТНИЦА",
				Artist:     "DEAD BLONDE",
				Tracks: []AlbumTrackCandidate{
					{
						Title:     "Банкомат",
						Artist:    "DEAD BLONDE",
						Duration:  200,
						DirectURL: "https://soundcloud.com/rusyarapscene/dead-blonde-bankomat",
					},
				},
				TotalTracks: 1,
			},
		}, nil
	})

	matcher := NewFullReleaseMatcher(mockYTM, scSearcher)
	targetTracks := []ResolvedTrack{
		{TrackID: "t1", TrackNumber: 1, Title: "Банкомат", Artist: "DEAD BLONDE", Duration: 200},
	}

	candsRU, isCyrillic, err := matcher.SearchReleaseCandidates(context.Background(), "DEAD BLONDE", "СПЛЕТНИЦА", targetTracks)
	if err != nil {
		t.Fatalf("SearchReleaseCandidates RU error: %v", err)
	}
	if !isCyrillic {
		t.Fatalf("expected isCyrillic = true")
	}
	if len(candsRU) < 2 {
		t.Fatalf("expected at least 2 candidates, got %d", len(candsRU))
	}
	// Первым должен быть SoundCloud с бейджем рекомендовано
	if candsRU[0].Source != "soundcloud_set" {
		t.Errorf("candidate 0 source = %q, want 'soundcloud_set'", candsRU[0].Source)
	}
	if candsRU[0].CensorStatus != "recommended" {
		t.Errorf("candidate 0 status = %q, want 'recommended'", candsRU[0].CensorStatus)
	}
	// Вторым должен быть YouTube Music с красным флагом возможной цензуры
	if candsRU[1].Source != "ytmusic_album" {
		t.Errorf("candidate 1 source = %q, want 'ytmusic_album'", candsRU[1].Source)
	}
	if candsRU[1].CensorStatus != "red_flag" {
		t.Errorf("candidate 1 status = %q, want 'red_flag'", candsRU[1].CensorStatus)
	}

	// 2. Тест для зарубежного артиста (21 Savage): YouTube Music должен быть #1
	scSearcherForeign := NewSoundCloudSetSearcher("")
	scSearcherForeign.SetCustomSearchFunc(func(ctx context.Context, artist, album string, limit int) ([]AlbumReleaseCandidate, error) {
		return []AlbumReleaseCandidate{
			{
				BrowseID:   "sc_savage",
				PlaylistID: "https://soundcloud.com/user/sets/savage-mode",
				Title:      "Savage Mode II",
				Artist:     "21 Savage",
				Tracks: []AlbumTrackCandidate{
					{Title: "Runnin", Artist: "21 Savage", Duration: 195, DirectURL: "https://soundcloud.com/track"},
				},
			},
		}, nil
	})

	serverForeign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/youtubei/v1/search" {
			w.Write([]byte(`{"contents":{"tabbedSearchResultsRenderer":{"tabs":[{"tabRenderer":{"content":{"sectionListRenderer":{"contents":[{"musicShelfRenderer":{"contents":[{"musicResponsiveListItemRenderer":{"navigationEndpoint":{"browseEndpoint":{"browseId":"MPREb_Foreign"}},"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Savage Mode II"}]}}},{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"21 Savage"}]}}}]}}]}}]}}}}]}}}`))
			return
		}
		if r.URL.Path == "/youtubei/v1/browse" {
			w.Write([]byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"secondaryContents":{"sectionListRenderer":{"contents":[{"musicShelfRenderer":{"contents":[{"musicResponsiveListItemRenderer":{"playlistItemData":{"videoId":"yt_runnin"},"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Runnin"}]}}}],"fixedColumns":[{"musicResponsiveListItemFixedColumnRenderer":{"text":{"runs":[{"text":"3:15"}]}}}]}}]}}]}}}}}`))
			return
		}
	}))
	defer serverForeign.Close()

	matcherForeign := NewFullReleaseMatcher(NewYouTubeMusicClient(serverForeign.URL), scSearcherForeign)
	targetForeign := []ResolvedTrack{
		{TrackID: "tf1", TrackNumber: 1, Title: "Runnin", Artist: "21 Savage", Duration: 195},
	}

	candsEN, isCyrillicEN, err := matcherForeign.SearchReleaseCandidates(context.Background(), "21 Savage", "Savage Mode II", targetForeign)
	if err != nil {
		t.Fatalf("SearchReleaseCandidates EN error: %v", err)
	}
	if isCyrillicEN {
		t.Fatalf("expected isCyrillicEN = false")
	}
	if len(candsEN) < 2 {
		t.Fatalf("expected at least 2 candidates for EN artist, got %d", len(candsEN))
	}
	// Первым должен быть YouTube Music официальный релиз
	if candsEN[0].Source != "ytmusic_album" {
		t.Errorf("EN candidate 0 source = %q, want 'ytmusic_album'", candsEN[0].Source)
	}
	if candsEN[0].CensorStatus != "recommended" {
		t.Errorf("EN candidate 0 status = %q, want 'recommended'", candsEN[0].CensorStatus)
	}
	// Вторым должен быть SoundCloud
	if candsEN[1].Source != "soundcloud_set" {
		t.Errorf("EN candidate 1 source = %q, want 'soundcloud_set'", candsEN[1].Source)
	}
	if candsEN[1].CensorStatus != "neutral" {
		t.Errorf("EN candidate 1 status = %q, want 'neutral'", candsEN[1].CensorStatus)
	}
}

