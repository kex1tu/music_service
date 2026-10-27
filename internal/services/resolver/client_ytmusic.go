package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"music-manager/internal/id3"
)

// YtMusicTrack представляет дорожку, извлеченную из YouTube Music.
type YtMusicTrack struct {
	Title    string   `json:"title"`
	Artist   string   `json:"artist"`  // Сформированная строка для UI ("Artist 1\\Artist 2")
	Artists  []string `json:"artists"` // Полный список всех соавторов
	Album    string   `json:"album"`
	Year     int      `json:"year"`
	CoverURL string   `json:"cover_url"`
	Duration int      `json:"duration"`
	VideoID  string   `json:"video_id"`
}

// YouTubeMusicClient выполняет запросы к открытому веб-API YouTube Music (Innertube WEB_REMIX).
type YouTubeMusicClient struct {
	httpClient *http.Client
	baseURL    string
}

// NewYouTubeMusicClient создает новый экземпляр клиента YouTube Music.
func NewYouTubeMusicClient(baseURL string) *YouTubeMusicClient {
	if baseURL == "" {
		baseURL = "https://music.youtube.com"
	}
	return &YouTubeMusicClient{
		httpClient: &http.Client{Timeout: 6 * time.Second},
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// SetBaseURL позволяет переопределить базовый URL для мок-тестов.
func (c *YouTubeMusicClient) SetBaseURL(u string) {
	c.baseURL = strings.TrimRight(u, "/")
}

// Search выполняет поиск композиций в YouTube Music и возвращает структурированный список с соавторами.
func (c *YouTubeMusicClient) Search(ctx context.Context, query string) ([]YtMusicTrack, error) {
	cleanQ := strings.TrimSpace(query)
	if cleanQ == "" {
		return nil, nil
	}

	payload := map[string]any{
		"context": map[string]any{
			"client": map[string]any{
				"clientName":    "WEB_REMIX",
				"clientVersion": "1.20240101.01.00",
				"hl":            "ru",
				"gl":            "RU",
			},
		},
		"query": cleanQ,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	targetURL := fmt.Sprintf("%s/youtubei/v1/search", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")
	req.Header.Set("Origin", "https://music.youtube.com")
	req.Header.Set("Referer", "https://music.youtube.com/")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("запрос к YouTube Music Innertube завершился ошибкой: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("YouTube Music Innertube вернул статус HTTP %d", resp.StatusCode)
	}

	var root struct {
		Contents struct {
			TabbedSearchResultsRenderer struct {
				Tabs []struct {
					TabRenderer struct {
						Content struct {
							SectionListRenderer struct {
								Contents []json.RawMessage `json:"contents"`
							} `json:"sectionListRenderer"`
						} `json:"content"`
					} `json:"tabRenderer"`
				} `json:"tabs"`
			} `json:"tabbedSearchResultsRenderer"`
		} `json:"contents"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&root); err != nil {
		return nil, fmt.Errorf("ошибка декодирования JSON YouTube Music: %w", err)
	}

	var tracks []YtMusicTrack
	seenKeys := make(map[string]bool)

	addTrack := func(t YtMusicTrack) {
		if t.Title == "" || len(t.Artists) == 0 {
			return
		}
		key := strings.ToLower(fmt.Sprintf("%s|%s", t.Artists[0], t.Title))
		if !seenKeys[key] {
			seenKeys[key] = true
			tracks = append(tracks, t)
		}
	}

	tabs := root.Contents.TabbedSearchResultsRenderer.Tabs
	if len(tabs) == 0 {
		return nil, nil
	}
	sections := tabs[0].TabRenderer.Content.SectionListRenderer.Contents

	for _, secRaw := range sections {
		// 1. Проверяем наличие карточки верхнего совпадения (musicCardShelfRenderer)
		var cardContainer struct {
			MusicCardShelfRenderer struct {
				Title struct {
					Runs []struct {
						Text               string `json:"text"`
						NavigationEndpoint *struct {
							WatchEndpoint *struct {
								VideoID string `json:"videoId"`
							} `json:"watchEndpoint"`
						} `json:"navigationEndpoint"`
					} `json:"runs"`
				} `json:"title"`
				Subtitle struct {
					Runs []struct {
						Text               string `json:"text"`
						NavigationEndpoint *struct {
							BrowseEndpoint *struct {
								BrowseEndpointContextSupportedConfigs *struct {
									BrowseEndpointContextMusicConfig *struct {
										PageType string `json:"pageType"`
									} `json:"browseEndpointContextMusicConfig"`
								} `json:"browseEndpointContextSupportedConfigs"`
							} `json:"browseEndpoint"`
						} `json:"navigationEndpoint"`
					} `json:"runs"`
				} `json:"subtitle"`
				Thumbnail struct {
					MusicThumbnailRenderer struct {
						Thumbnail struct {
							Thumbnails []struct {
								URL    string `json:"url"`
								Width  int    `json:"width"`
								Height int    `json:"height"`
							} `json:"thumbnails"`
						} `json:"thumbnail"`
					} `json:"musicThumbnailRenderer"`
				} `json:"thumbnail"`
			} `json:"musicCardShelfRenderer"`
		}

		if err := json.Unmarshal(secRaw, &cardContainer); err == nil && len(cardContainer.MusicCardShelfRenderer.Title.Runs) > 0 {
			r0 := cardContainer.MusicCardShelfRenderer.Title.Runs[0]
			rawTitle := r0.Text
			cleanTit, titleFeats := ExtractFeatFromText(rawTitle)
			cleanTit = CleanYouTubeVideoTitle(cleanTit)

			videoID := ""
			if r0.NavigationEndpoint != nil && r0.NavigationEndpoint.WatchEndpoint != nil {
				videoID = r0.NavigationEndpoint.WatchEndpoint.VideoID
			}

			var artists []string
			for _, r := range cardContainer.MusicCardShelfRenderer.Subtitle.Runs {
				txt := strings.TrimSpace(r.Text)
				if txt == "" || txt == "•" || txt == "Композиция" || txt == "Песня" || txt == "Видео" {
					continue
				}
				// Если это артист
				if r.NavigationEndpoint != nil && r.NavigationEndpoint.BrowseEndpoint != nil {
					artists = append(artists, txt)
				}
			}
			// Добавляем фиты из названия
			artists = append(artists, titleFeats...)

			bestThumb := ""
			for _, th := range cardContainer.MusicCardShelfRenderer.Thumbnail.MusicThumbnailRenderer.Thumbnail.Thumbnails {
				bestThumb = th.URL
			}

			if len(artists) > 0 {
				addTrack(YtMusicTrack{
					Title:    cleanTit,
					Artists:  artists,
					Artist:   id3.FormatMultiValue(artists),
					CoverURL: bestThumb,
					VideoID:  videoID,
				})
			}
		}

		// 2. Проверяем элементы списка itemSectionRenderer -> contents -> musicResponsiveListItemRenderer
		var itemSection struct {
			ItemSectionRenderer struct {
				Contents []struct {
					MusicResponsiveListItemRenderer ytResponsiveListItem `json:"musicResponsiveListItemRenderer"`
				} `json:"contents"`
			} `json:"itemSectionRenderer"`
		}
		if err := json.Unmarshal(secRaw, &itemSection); err == nil {
			for _, c := range itemSection.ItemSectionRenderer.Contents {
				parseYtResponsiveListItem(c.MusicResponsiveListItemRenderer, addTrack)
			}
		}

		// 3. Проверяем элементы списка musicShelfRenderer -> contents -> musicResponsiveListItemRenderer (актуальный формат Innertube)
		var shelfContainer struct {
			MusicShelfRenderer struct {
				Contents []struct {
					MusicResponsiveListItemRenderer ytResponsiveListItem `json:"musicResponsiveListItemRenderer"`
				} `json:"contents"`
			} `json:"musicShelfRenderer"`
		}
		if err := json.Unmarshal(secRaw, &shelfContainer); err == nil {
			for _, c := range shelfContainer.MusicShelfRenderer.Contents {
				parseYtResponsiveListItem(c.MusicResponsiveListItemRenderer, addTrack)
			}
		}
	}

	return tracks, nil
}

type ytResponsiveListItem struct {
	FlexColumns []struct {
		MusicResponsiveListItemFlexColumnRenderer struct {
			Text struct {
				Runs []struct {
					Text               string `json:"text"`
					NavigationEndpoint *struct {
						WatchEndpoint *struct {
							VideoID string `json:"videoId"`
						} `json:"watchEndpoint"`
						BrowseEndpoint *struct {
							BrowseEndpointContextSupportedConfigs *struct {
								BrowseEndpointContextMusicConfig *struct {
									PageType string `json:"pageType"`
								} `json:"browseEndpointContextMusicConfig"`
							} `json:"browseEndpointContextSupportedConfigs"`
						} `json:"browseEndpoint"`
					} `json:"navigationEndpoint"`
				} `json:"runs"`
			} `json:"text"`
		} `json:"musicResponsiveListItemFlexColumnRenderer"`
	} `json:"flexColumns"`
	PlaylistItemData *struct {
		VideoID string `json:"videoId"`
	} `json:"playlistItemData"`
	Overlay *struct {
		MusicItemThumbnailOverlayRenderer *struct {
			Content *struct {
				MusicPlayButtonRenderer *struct {
					PlayNavigationEndpoint *struct {
						WatchEndpoint *struct {
							VideoID string `json:"videoId"`
						} `json:"watchEndpoint"`
					} `json:"playNavigationEndpoint"`
				} `json:"musicPlayButtonRenderer"`
			} `json:"content"`
		} `json:"musicItemThumbnailOverlayRenderer"`
	} `json:"overlay"`
	Thumbnail struct {
		MusicThumbnailRenderer struct {
			Thumbnail struct {
				Thumbnails []struct {
					URL string `json:"url"`
				} `json:"thumbnails"`
			} `json:"thumbnail"`
		} `json:"musicThumbnailRenderer"`
	} `json:"thumbnail"`
}

func parseYtResponsiveListItem(r ytResponsiveListItem, addTrack func(YtMusicTrack)) {
	if len(r.FlexColumns) == 0 {
		return
	}

	// Первый столбец - название
	runs0 := r.FlexColumns[0].MusicResponsiveListItemFlexColumnRenderer.Text.Runs
	if len(runs0) == 0 {
		return
	}
	rawTitle := runs0[0].Text

	// Извлечение VideoID
	videoID := ""
	if r.PlaylistItemData != nil && r.PlaylistItemData.VideoID != "" {
		videoID = r.PlaylistItemData.VideoID
	}
	if videoID == "" && len(runs0) > 0 && runs0[0].NavigationEndpoint != nil && runs0[0].NavigationEndpoint.WatchEndpoint != nil {
		videoID = runs0[0].NavigationEndpoint.WatchEndpoint.VideoID
	}
	if videoID == "" && r.Overlay != nil && r.Overlay.MusicItemThumbnailOverlayRenderer != nil {
		if content := r.Overlay.MusicItemThumbnailOverlayRenderer.Content; content != nil {
			if btn := content.MusicPlayButtonRenderer; btn != nil {
				if playEp := btn.PlayNavigationEndpoint; playEp != nil && playEp.WatchEndpoint != nil {
					videoID = playEp.WatchEndpoint.VideoID
				}
			}
		}
	}

	// Второй столбец - метаданные (тип, артисты, альбом, время)
	var artists []string
	album := ""
	year := 0

	if len(r.FlexColumns) > 1 {
		runs1 := r.FlexColumns[1].MusicResponsiveListItemFlexColumnRenderer.Text.Runs
		isSongOrTrack := false

		for _, run := range runs1 {
			t := strings.TrimSpace(run.Text)
			if t == "" || t == "•" {
				continue
			}
			if t == "Композиция" || t == "Песня" || t == "Сингл" {
				isSongOrTrack = true
				continue
			}
			// Проверяем страницу артиста
			if run.NavigationEndpoint != nil && run.NavigationEndpoint.BrowseEndpoint != nil {
				cfg := run.NavigationEndpoint.BrowseEndpoint.BrowseEndpointContextSupportedConfigs
				if cfg != nil && cfg.BrowseEndpointContextMusicConfig != nil {
					pt := cfg.BrowseEndpointContextMusicConfig.PageType
					if pt == "MUSIC_PAGE_TYPE_ARTIST" || pt == "MUSIC_PAGE_TYPE_USER_CHANNEL" {
						artists = append(artists, t)
						continue
					}
					if pt == "MUSIC_PAGE_TYPE_ALBUM" {
						album = t
						continue
					}
				} else {
					artists = append(artists, t)
					continue
				}
			}

			// Проверка на год выпуска
			if len(t) == 4 {
				if y, err := strconv.Atoi(t); err == nil && y > 1950 && y < 2030 {
					year = y
					continue
				}
			}
		}
		_ = isSongOrTrack
	}

	cleanTit, titleFeats := ExtractFeatFromText(rawTitle)
	cleanTit = CleanYouTubeVideoTitle(cleanTit)
	artists = append(artists, titleFeats...)

	bestThumb := ""
	for _, th := range r.Thumbnail.MusicThumbnailRenderer.Thumbnail.Thumbnails {
		bestThumb = th.URL
	}

	if len(artists) > 0 {
		addTrack(YtMusicTrack{
			Title:    cleanTit,
			Artists:  artists,
			Artist:   id3.FormatMultiValue(artists),
			Album:    album,
			Year:     year,
			CoverURL: bestThumb,
			VideoID:  videoID,
		})
	}
}
