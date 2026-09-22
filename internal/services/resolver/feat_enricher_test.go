package resolver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestFeatEnricher_LocalExtraction(t *testing.T) {
	enricher := NewFeatEnricher(nil, nil)
	ctx := context.Background()

	// 1. Feat in title
	artists1 := enricher.EnrichTrackArtists(ctx, "I Got Love (feat. Рем Дигга)", []string{"MiyaGi & Эндшпиль"})
	want1 := []string{"MiyaGi & Эндшпиль", "Рем Дигга"}
	if !reflect.DeepEqual(artists1, want1) {
		t.Errorf("artists1 = %v, want %v", artists1, want1)
	}

	// 2. Feat in artist name
	artists2 := enricher.EnrichTrackArtists(ctx, "Сансара", []string{"Баста feat. Полина Гагарина"})
	want2 := []string{"Баста", "Полина Гагарина"}
	if !reflect.DeepEqual(artists2, want2) {
		t.Errorf("artists2 = %v, want %v", artists2, want2)
	}

	// 3. Already multi-valued with \\
	artists3 := enricher.EnrichTrackArtists(ctx, "Fata Morgana", []string{"Markul\\\\Oxxxymiron"})
	want3 := []string{"Markul", "Oxxxymiron"}
	if !reflect.DeepEqual(artists3, want3) {
		t.Errorf("artists3 = %v, want %v", artists3, want3)
	}
}

func TestFeatEnricher_YandexMusicOnline(t *testing.T) {
	mockYa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/search" {
			w.Write([]byte(`{
				"result": {
					"tracks": {
						"results": [
							{
								"id": 12345,
								"title": "I Got Love",
								"artists": [
									{"id": 1, "name": "MiyaGi & Эндшпиль"},
									{"id": 2, "name": "Рем Дигга"}
								]
							}
						]
					}
				}
			}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer mockYa.Close()

	yaClient := NewYandexMusicClient("")
	yaClient.SetBaseURL(mockYa.URL)

	enricher := NewFeatEnricher(yaClient, nil)
	ctx := context.Background()

	artists := enricher.EnrichTrackArtists(ctx, "I Got Love", []string{"MiyaGi & Эндшпиль"})
	want := []string{"MiyaGi & Эндшпиль", "Рем Дигга"}
	if !reflect.DeepEqual(artists, want) {
		t.Errorf("artists = %v, want %v", artists, want)
	}
}

func TestFeatEnricher_MaxArtistsWins_YouTubeMusicWins(t *testing.T) {
	// Yandex возвращает только 1 исполнителя
	mockYa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"result": {
				"tracks": {
					"results": [
						{
							"id": 1,
							"title": "Wild Thoughts",
							"artists": [{"id": 10, "name": "DJ Khaled"}]
						}
					]
				}
			}
		}`))
	}))
	defer mockYa.Close()

	// YouTube Music возвращает 3 соавторов
	mockYtm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
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
													"title": {"runs": [{"text": "Wild Thoughts"}]},
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
													}
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
	defer mockYtm.Close()

	yaClient := NewYandexMusicClient("")
	yaClient.SetBaseURL(mockYa.URL)

	ytmClient := NewYouTubeMusicClient("")
	ytmClient.SetBaseURL(mockYtm.URL)

	enricher := NewFeatEnricher(yaClient, ytmClient)
	ctx := context.Background()

	// На входе 1 исполнитель, YTM вернул 3, Yandex 1 -> побеждает YTM (3 исполнителя)
	artists := enricher.EnrichTrackArtists(ctx, "Wild Thoughts", []string{"DJ Khaled"})
	want := []string{"DJ Khaled", "Rihanna", "Bryson Tiller"}
	if !reflect.DeepEqual(artists, want) {
		t.Errorf("artists = %v, want %v", artists, want)
	}
}

func TestFeatEnricher_MaxArtistsWins_YandexWins(t *testing.T) {
	// Yandex возвращает 6 соавторов для «Сансара»
	mockYa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"result": {
				"tracks": {
					"results": [
						{
							"id": 99,
							"title": "Сансара",
							"artists": [
								{"id": 1, "name": "Баста"},
								{"id": 2, "name": "Диана Арбенина"},
								{"id": 3, "name": "Александр Ф. Скляр"},
								{"id": 4, "name": "Сергей Бобунец"},
								{"id": 5, "name": "SunSay"},
								{"id": 6, "name": "Скриптонит"}
							]
						}
					]
				}
			}
		}`))
	}))
	defer mockYa.Close()

	// YouTube Music возвращает 1 соавтора
	mockYtm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
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
													"title": {"runs": [{"text": "Сансара"}]},
													"subtitle": {
														"runs": [
															{
																"text": "Баста - Topic",
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
								}
							}
						}
					]
				}
			}
		}`))
	}))
	defer mockYtm.Close()

	yaClient := NewYandexMusicClient("")
	yaClient.SetBaseURL(mockYa.URL)

	ytmClient := NewYouTubeMusicClient("")
	ytmClient.SetBaseURL(mockYtm.URL)

	enricher := NewFeatEnricher(yaClient, ytmClient)
	ctx := context.Background()

	// На входе 1 исполнитель, Yandex вернул 6, YTM 1 -> побеждает Yandex (6 соавторов)
	artists := enricher.EnrichTrackArtists(ctx, "Сансара", []string{"Баста"})
	want := []string{"Баста", "Диана Арбенина", "Александр Ф. Скляр", "Сергей Бобунец", "SunSay", "Скриптонит"}
	if !reflect.DeepEqual(artists, want) {
		t.Errorf("artists = %v, want %v", artists, want)
	}
}
