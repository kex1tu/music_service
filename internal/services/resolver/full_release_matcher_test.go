package resolver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

