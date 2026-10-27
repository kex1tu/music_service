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

func TestYouTubeMusicClient_MusicShelfRenderer(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Мок ответа YouTube Music с актуальным musicShelfRenderer
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
												"musicShelfRenderer": {
													"contents": [
														{
															"musicResponsiveListItemRenderer": {
																"flexColumns": [
																	{
																		"musicResponsiveListItemFlexColumnRenderer": {
																			"text": {
																				"runs": [{"text": "Haunted House"}]
																			}
																		}
																	},
																	{
																		"musicResponsiveListItemFlexColumnRenderer": {
																			"text": {
																				"runs": [
																					{
																						"text": "Aarne",
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
																					{"text": ", "},
																					{
																						"text": "Big Baby Tape",
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
																					{"text": " & "},
																					{
																						"text": "kizaru",
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
																						"text": "AA LANGUAGE 2",
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
																					{"text": "2:34"}
																				]
																			}
																		}
																	}
																]
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

	client := NewYouTubeMusicClient(mockServer.URL)
	tracks, err := client.Search(context.Background(), "Aarne Haunted House")
	if err != nil {
		t.Fatalf("unexpected search error: %v", err)
	}

	if len(tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(tracks))
	}

	tr := tracks[0]
	if tr.Title != "Haunted House" {
		t.Errorf("expected title 'Haunted House', got %q", tr.Title)
	}

	expectedArtists := []string{"Aarne", "Big Baby Tape", "kizaru"}
	if !reflect.DeepEqual(tr.Artists, expectedArtists) {
		t.Errorf("expected artists %v, got %v", expectedArtists, tr.Artists)
	}
	if tr.Album != "AA LANGUAGE 2" {
		t.Errorf("expected album 'AA LANGUAGE 2', got %q", tr.Album)
	}
}

func TestYouTubeMusicClient_ExtractVideoID(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
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
													"flexColumns": [
														{
															"musicResponsiveListItemFlexColumnRenderer": {
																"text": {
																	"runs": [{
																		"text": "Дым",
																		"navigationEndpoint": {
																			"watchEndpoint": {
																				"videoId": "vid_h8hood_123"
																			}
																		}
																	}]
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
																			"text": "h8.hood",
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
	}))
	defer mockServer.Close()

	client := NewYouTubeMusicClient(mockServer.URL)
	tracks, err := client.Search(context.Background(), "h8.hood Дым")
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(tracks))
	}
	if tracks[0].VideoID != "vid_h8hood_123" {
		t.Errorf("expected VideoID 'vid_h8hood_123', got %q", tracks[0].VideoID)
	}
}

func TestYouTubeMusicClient_SearchAlbumRelease(t *testing.T) {
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
																			{
																				"text": "DEAD BLONDE",
																				"navigationEndpoint": {
																					"browseEndpoint": {
																						"browseId": "UCneVw2164rmpaVJKFiWwlRg"
																					}
																				}
																			},
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
													"playlistItemData": {
														"videoId": "m80jEPGrr7o"
													},
													"flexColumns": [
														{
															"musicResponsiveListItemFlexColumnRenderer": {
																"text": {
																	"runs": [{"text": "Банкомат"}]
																}
															}
														},
														{
															"musicResponsiveListItemFlexColumnRenderer": {
																"text": {
																	"runs": [{"text": "DEAD BLONDE"}]
																}
															}
														}
													],
													"fixedColumns": [{
														"musicResponsiveListItemFixedColumnRenderer": {
															"text": {
																"runs": [{"text": "3:20"}]
															}
														}
													}]
												}
											},
											{
												"musicResponsiveListItemRenderer": {
													"playlistItemData": {
														"videoId": "0pd_z_x144M"
													},
													"flexColumns": [
														{
															"musicResponsiveListItemFlexColumnRenderer": {
																"text": {
																	"runs": [{"text": "Красный свет"}]
																}
															}
														},
														{
															"musicResponsiveListItemFlexColumnRenderer": {
																"text": {
																	"runs": [{"text": "DEAD BLONDE"}]
																}
															}
														}
													],
													"fixedColumns": [{
														"musicResponsiveListItemFixedColumnRenderer": {
															"text": {
																"runs": [{"text": "3:18"}]
															}
														}
													}]
												}
											},
											{
												"musicResponsiveListItemRenderer": {
													"playlistItemData": {
														"videoId": "B2dcmYaRzaY"
													},
													"flexColumns": [
														{
															"musicResponsiveListItemFlexColumnRenderer": {
																"text": {
																	"runs": [{"text": "Самолёт"}]
																}
															}
														},
														{
															"musicResponsiveListItemFlexColumnRenderer": {
																"text": {
																	"runs": [{"text": "DEAD BLONDE"}]
																}
															}
														}
													],
													"fixedColumns": [{
														"musicResponsiveListItemFixedColumnRenderer": {
															"text": {
																"runs": [{"text": "3:02"}]
															}
														}
													}]
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

	client := NewYouTubeMusicClient(mockServer.URL)
	album, err := client.SearchAlbumRelease(context.Background(), "DEAD BLONDE", "СПЛЕТНИЦА")
	if err != nil {
		t.Fatalf("SearchAlbumRelease error: %v", err)
	}
	if album == nil {
		t.Fatal("expected album candidate, got nil")
	}
	if album.Title != "СПЛЕТНИЦА" {
		t.Errorf("expected title 'СПЛЕТНИЦА', got %q", album.Title)
	}
	if album.Artist != "DEAD BLONDE" {
		t.Errorf("expected artist 'DEAD BLONDE', got %q", album.Artist)
	}
	if album.Year != 2022 {
		t.Errorf("expected year 2022, got %d", album.Year)
	}
	if len(album.Tracks) != 3 {
		t.Fatalf("expected 3 tracks, got %d", len(album.Tracks))
	}
	if album.Tracks[0].Title != "Банкомат" || album.Tracks[0].Duration != 200 || album.Tracks[0].VideoID != "m80jEPGrr7o" {
		t.Errorf("unexpected track 0: %+v", album.Tracks[0])
	}
}

