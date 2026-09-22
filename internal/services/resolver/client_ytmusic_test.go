package resolver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"music-manager/internal/id3"
)

func TestYouTubeMusicClient_Search(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/youtubei/v1/search" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		// Мок ответа YouTube Music Innertube: верхняя карточка (CardShelf) + элемент списка (ResponsiveListItem)
		w.Write([]byte(`{
			"contents": {
				"tabbedSearchResultsRenderer": {
					"tabs": [
						{
							"tabRenderer": {
								"content": {
									"sectionListRenderer": {
										"contents": [
											{
												"musicCardShelfRenderer": {
													"title": {
														"runs": [{"text": "Wild Thoughts"}]
													},
													"subtitle": {
														"runs": [
															{
																"text": "DJ Khaled",
																"navigationEndpoint": {
																	"browseEndpoint": {
																		"browseEndpointContextSupportedConfigs": {
																			"browseEndpointContextMusicConfig": {
																				"pageType": "MUSIC_PAGE_TYPE_ARTIST"
																			}
																		}
																	}
																}
															},
															{"text": " • "},
															{
																"text": "Rihanna",
																"navigationEndpoint": {
																	"browseEndpoint": {
																		"browseEndpointContextSupportedConfigs": {
																			"browseEndpointContextMusicConfig": {
																				"pageType": "MUSIC_PAGE_TYPE_ARTIST"
																			}
																		}
																	}
																}
															},
															{"text": " • "},
															{
																"text": "Bryson Tiller",
																"navigationEndpoint": {
																	"browseEndpoint": {
																		"browseEndpointContextSupportedConfigs": {
																			"browseEndpointContextMusicConfig": {
																				"pageType": "MUSIC_PAGE_TYPE_ARTIST"
																			}
																		}
																	}
																}
															}
														]
													},
													"thumbnail": {
														"musicThumbnailRenderer": {
															"thumbnail": {
																"thumbnails": [
																	{"url": "https://lh3.googleusercontent.com/sample_cover=w120-h120", "width": 120, "height": 120},
																	{"url": "https://lh3.googleusercontent.com/sample_cover=w544-h544", "width": 544, "height": 544}
																]
															}
														}
													}
												}
											},
											{
												"itemSectionRenderer": {
													"contents": [
														{
															"musicResponsiveListItemRenderer": {
																"flexColumns": [
																	{
																		"musicResponsiveListItemFlexColumnRenderer": {
																			"text": {
																				"runs": [{"text": "Fata Morgana (feat. Oxxxymiron)"}]
																			}
																		}
																	},
																	{
																		"musicResponsiveListItemFlexColumnRenderer": {
																			"text": {
																				"runs": [
																					{"text": "Песня"},
																					{"text": " • "},
																					{
																						"text": "Markul",
																						"navigationEndpoint": {
																							"browseEndpoint": {
																								"browseEndpointContextSupportedConfigs": {
																									"browseEndpointContextMusicConfig": {
																										"pageType": "MUSIC_PAGE_TYPE_ARTIST"
																									}
																								}
																							}
																						}
																					},
																					{"text": " • "},
																					{
																						"text": "Great Depression",
																						"navigationEndpoint": {
																							"browseEndpoint": {
																								"browseEndpointContextSupportedConfigs": {
																									"browseEndpointContextMusicConfig": {
																										"pageType": "MUSIC_PAGE_TYPE_ALBUM"
																									}
																								}
																							}
																						}
																					},
																					{"text": " • "},
																					{"text": "2017"}
																				]
																			}
																		}
																	}
																],
																"thumbnail": {
																	"musicThumbnailRenderer": {
																		"thumbnail": {
																			"thumbnails": [
																				{"url": "https://lh3.googleusercontent.com/markul_cover=w544-h544"}
																			]
																		}
																	}
																}
															}
														}
													]
												}
											}
										]
									}
								}
							}
						}
					]
				}
			}
		}`))
	}))
	defer mockServer.Close()

	client := NewYouTubeMusicClient("")
	client.SetBaseURL(mockServer.URL)

	ctx := context.Background()
	tracks, err := client.Search(ctx, "Wild Thoughts")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(tracks))
	}

	// 1. Проверяем трек из карточки (Wild Thoughts)
	t1 := tracks[0]
	if t1.Title != "Wild Thoughts" {
		t.Errorf("expected Title 'Wild Thoughts', got %q", t1.Title)
	}
	expectedArtists1 := []string{"DJ Khaled", "Rihanna", "Bryson Tiller"}
	if !reflect.DeepEqual(t1.Artists, expectedArtists1) {
		t.Errorf("expected Artists %v, got %v", expectedArtists1, t1.Artists)
	}
	if t1.Artist != id3.FormatMultiValue(expectedArtists1) {
		t.Errorf("expected Artist string %q, got %q", id3.FormatMultiValue(expectedArtists1), t1.Artist)
	}
	if t1.CoverURL != "https://lh3.googleusercontent.com/sample_cover=w544-h544" {
		t.Errorf("unexpected CoverURL: %q", t1.CoverURL)
	}

	// 2. Проверяем трек из списка (Fata Morgana)
	t2 := tracks[1]
	if t2.Title != "Fata Morgana" {
		t.Errorf("expected Title 'Fata Morgana', got %q", t2.Title)
	}
	expectedArtists2 := []string{"Markul", "Oxxxymiron"}
	if !reflect.DeepEqual(t2.Artists, expectedArtists2) {
		t.Errorf("expected Artists %v, got %v", expectedArtists2, t2.Artists)
	}
	if t2.Artist != id3.FormatMultiValue(expectedArtists2) {
		t.Errorf("expected Artist string %q, got %q", id3.FormatMultiValue(expectedArtists2), t2.Artist)
	}
	if t2.Album != "Great Depression" {
		t.Errorf("expected Album 'Great Depression', got %q", t2.Album)
	}
	if t2.Year != 2017 {
		t.Errorf("expected Year 2017, got %d", t2.Year)
	}
}

func TestYouTubeMusicClient_EmptyAndError(t *testing.T) {
	client := NewYouTubeMusicClient("")
	ctx := context.Background()

	// Пустой запрос
	res, err := client.Search(ctx, "")
	if err != nil || res != nil {
		t.Errorf("expected nil result for empty query, got res=%v, err=%v", res, err)
	}

	// Сервер вернул 500
	mockErrServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer mockErrServer.Close()

	client.SetBaseURL(mockErrServer.URL)
	_, err = client.Search(ctx, "Test")
	if err == nil {
		t.Errorf("expected error on HTTP 500, got nil")
	}
}
