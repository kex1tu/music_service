package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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

// AlbumTrackCandidate представляет трек официального альбома YouTube Music или SoundCloud сета.
type AlbumTrackCandidate struct {
	VideoID   string `json:"video_id"`
	DirectURL string `json:"direct_url,omitempty"`
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	Duration  int    `json:"duration"` // в секундах
	Index     int    `json:"index"`
}

// AlbumReleaseCandidate представляет метаданные найденного альбома и его треклист.
type AlbumReleaseCandidate struct {
	BrowseID    string                `json:"browse_id"`
	PlaylistID  string                `json:"playlist_id"`
	Title       string                `json:"title"`
	Artist      string                `json:"artist"`
	Year        int                   `json:"year"`
	CoverURL    string                `json:"cover_url"`
	Tracks      []AlbumTrackCandidate `json:"tracks"`
	TotalTracks int                   `json:"total_tracks"`
}

// parseDurationSec парсит строковую длительность ("M:SS", "H:MM:SS", "SS") в секунды.
func parseDurationSec(s string) int {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) == 2 {
		m, _ := strconv.Atoi(parts[0])
		sec, _ := strconv.Atoi(parts[1])
		return m*60 + sec
	} else if len(parts) == 3 {
		h, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		sec, _ := strconv.Atoi(parts[2])
		return h*3600 + m*60 + sec
	} else if len(parts) == 1 {
		sec, _ := strconv.Atoi(parts[0])
		return sec
	}
	return 0
}

// SearchAlbumRelease выполняет поиск официального альбома/плейлиста на YouTube Music
func (c *YouTubeMusicClient) SearchAlbumRelease(ctx context.Context, artist, album string) (*AlbumReleaseCandidate, error) {
	cands, err := c.SearchAlbumReleaseCandidates(ctx, artist, album, 1)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, nil
	}
	return &cands[0], nil
}

// SearchAlbumReleaseCandidates выполняет поиск альбомов в каталоге YouTube Music и возвращает список подходящих релизов с их треклистами.
func (c *YouTubeMusicClient) SearchAlbumReleaseCandidates(ctx context.Context, artist, album string, limit int) ([]AlbumReleaseCandidate, error) {
	if limit <= 0 {
		limit = 5
	}
	cleanArt := strings.TrimSpace(artist)
	cleanAlb := strings.TrimSpace(album)
	if cleanAlb == "" {
		return nil, nil
	}

	query := strings.TrimSpace(fmt.Sprintf("%s %s", cleanArt, cleanAlb))
	payload := map[string]any{
		"context": map[string]any{
			"client": map[string]any{
				"clientName":    "WEB_REMIX",
				"clientVersion": "1.20240101.01.00",
				"hl":            "ru",
				"gl":            "RU",
			},
		},
		"query":  query,
		"params": "EgWKAQIYAWoKEAkQBRAKEAMQBA==", // Filter: Albums only
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
		return nil, fmt.Errorf("YouTube Music Innertube search error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("YouTube Music Innertube returned HTTP %d", resp.StatusCode)
	}

	var root struct {
		Contents struct {
			TabbedSearchResultsRenderer struct {
				Tabs []struct {
					TabRenderer struct {
						Content struct {
							SectionListRenderer struct {
								Contents []struct {
									MusicShelfRenderer struct {
										Contents []json.RawMessage `json:"contents"`
									} `json:"musicShelfRenderer"`
									ItemSectionRenderer struct {
										Contents []json.RawMessage `json:"contents"`
									} `json:"itemSectionRenderer"`
								} `json:"contents"`
							} `json:"sectionListRenderer"`
						} `json:"content"`
					} `json:"tabRenderer"`
				} `json:"tabs"`
			} `json:"tabbedSearchResultsRenderer"`
		} `json:"contents"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&root); err != nil {
		return nil, fmt.Errorf("decode error: %w", err)
	}

	tabs := root.Contents.TabbedSearchResultsRenderer.Tabs
	if len(tabs) == 0 {
		return nil, nil
	}
	sections := tabs[0].TabRenderer.Content.SectionListRenderer.Contents

	type rawCandidate struct {
		BrowseID   string
		PlaylistID string
		Title      string
		Artist     string
		Year       int
		CoverURL   string
		Score      float64
	}

	var candidates []rawCandidate

	for _, sec := range sections {
		var rawItems []json.RawMessage
		if len(sec.MusicShelfRenderer.Contents) > 0 {
			rawItems = sec.MusicShelfRenderer.Contents
		} else if len(sec.ItemSectionRenderer.Contents) > 0 {
			rawItems = sec.ItemSectionRenderer.Contents
		}

		for _, item := range rawItems {
			var wrapper struct {
				MusicResponsiveListItemRenderer struct {
					NavigationEndpoint *struct {
						BrowseEndpoint *struct {
							BrowseId string `json:"browseId"`
						} `json:"browseEndpoint"`
					} `json:"navigationEndpoint"`
					Overlay *struct {
						MusicItemThumbnailOverlayRenderer *struct {
							Content *struct {
								MusicPlayButtonRenderer *struct {
									PlayNavigationEndpoint *struct {
										WatchPlaylistEndpoint *struct {
											PlaylistId string `json:"playlistId"`
										} `json:"watchPlaylistEndpoint"`
									} `json:"playNavigationEndpoint"`
								} `json:"musicPlayButtonRenderer"`
							} `json:"content"`
						} `json:"musicItemThumbnailOverlayRenderer"`
					} `json:"overlay"`
					FlexColumns []struct {
						MusicResponsiveListItemFlexColumnRenderer struct {
							Text struct {
								Runs []struct {
									Text               string `json:"text"`
									NavigationEndpoint *struct {
										BrowseEndpoint *struct {
											BrowseId string `json:"browseId"`
										} `json:"browseEndpoint"`
									} `json:"navigationEndpoint"`
								} `json:"runs"`
							} `json:"text"`
						} `json:"musicResponsiveListItemFlexColumnRenderer"`
					} `json:"flexColumns"`
					Thumbnail *struct {
						MusicThumbnailRenderer *struct {
							Thumbnail *struct {
								Thumbnails []struct {
									URL string `json:"url"`
								} `json:"thumbnails"`
							} `json:"thumbnail"`
						} `json:"musicThumbnailRenderer"`
					} `json:"thumbnail"`
				} `json:"musicResponsiveListItemRenderer"`
			}

			if err := json.Unmarshal(item, &wrapper); err != nil {
				continue
			}
			rend := wrapper.MusicResponsiveListItemRenderer
			if len(rend.FlexColumns) == 0 {
				continue
			}

			browseId := ""
			if rend.NavigationEndpoint != nil && rend.NavigationEndpoint.BrowseEndpoint != nil {
				browseId = rend.NavigationEndpoint.BrowseEndpoint.BrowseId
			}
			if browseId == "" && len(rend.FlexColumns[0].MusicResponsiveListItemFlexColumnRenderer.Text.Runs) > 0 {
				if ep := rend.FlexColumns[0].MusicResponsiveListItemFlexColumnRenderer.Text.Runs[0].NavigationEndpoint; ep != nil && ep.BrowseEndpoint != nil {
					browseId = ep.BrowseEndpoint.BrowseId
				}
			}

			playlistId := ""
			if rend.Overlay != nil && rend.Overlay.MusicItemThumbnailOverlayRenderer != nil {
				if cnt := rend.Overlay.MusicItemThumbnailOverlayRenderer.Content; cnt != nil && cnt.MusicPlayButtonRenderer != nil {
					if pne := cnt.MusicPlayButtonRenderer.PlayNavigationEndpoint; pne != nil && pne.WatchPlaylistEndpoint != nil {
						playlistId = pne.WatchPlaylistEndpoint.PlaylistId
					}
				}
			}

			titleRuns := rend.FlexColumns[0].MusicResponsiveListItemFlexColumnRenderer.Text.Runs
			candTitle := ""
			if len(titleRuns) > 0 {
				candTitle = strings.TrimSpace(titleRuns[0].Text)
			}

			candArtist := ""
			candYear := 0
			if len(rend.FlexColumns) > 1 {
				for _, r := range rend.FlexColumns[1].MusicResponsiveListItemFlexColumnRenderer.Text.Runs {
					txt := strings.TrimSpace(r.Text)
					if txt == "" || txt == "•" || txt == "Альбом" || txt == "Сингл" || txt == "EP" || txt == "Album" || txt == "Single" {
						continue
					}
					if len(txt) == 4 {
						if y, yErr := strconv.Atoi(txt); yErr == nil && y > 1950 && y < 2030 {
							candYear = y
							continue
						}
					}
					if candArtist == "" {
						candArtist = txt
					}
				}
			}

			coverURL := ""
			if rend.Thumbnail != nil && rend.Thumbnail.MusicThumbnailRenderer != nil && len(rend.Thumbnail.MusicThumbnailRenderer.Thumbnail.Thumbnails) > 0 {
				thumbs := rend.Thumbnail.MusicThumbnailRenderer.Thumbnail.Thumbnails
				coverURL = thumbs[len(thumbs)-1].URL
			}

			if browseId == "" && playlistId == "" {
				continue
			}

			// Вычисляем релевантность
			titleScore := LevenshteinRatio(strings.ToLower(cleanAlb), strings.ToLower(candTitle))
			if strings.Contains(strings.ToLower(candTitle), strings.ToLower(cleanAlb)) || strings.Contains(strings.ToLower(cleanAlb), strings.ToLower(candTitle)) {
				if titleScore < 0.8 {
					titleScore = 0.8
				}
			}

			artScore := 0.5
			if cleanArt != "" && candArtist != "" {
				artScore = LevenshteinRatio(strings.ToLower(cleanArt), strings.ToLower(candArtist))
				if strings.Contains(strings.ToLower(candArtist), strings.ToLower(cleanArt)) || strings.Contains(strings.ToLower(cleanArt), strings.ToLower(candArtist)) {
					if artScore < 0.8 {
						artScore = 0.8
					}
				}
			}

			totalScore := titleScore*0.6 + artScore*0.4
			candidates = append(candidates, rawCandidate{
				BrowseID:   browseId,
				PlaylistID: playlistId,
				Title:      candTitle,
				Artist:     candArtist,
				Year:       candYear,
				CoverURL:   coverURL,
				Score:      totalScore,
			})
		}
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	var results []AlbumReleaseCandidate
	for _, cand := range candidates {
		if cand.Score < 0.35 {
			continue
		}
		tracks, err := c.GetAlbumReleaseTracks(ctx, cand.BrowseID)
		if err != nil || len(tracks) == 0 {
			continue
		}
		results = append(results, AlbumReleaseCandidate{
			BrowseID:    cand.BrowseID,
			PlaylistID:  cand.PlaylistID,
			Title:       cand.Title,
			Artist:      cand.Artist,
			Year:        cand.Year,
			CoverURL:    cand.CoverURL,
			Tracks:      tracks,
			TotalTracks: len(tracks),
		})
		if len(results) >= limit {
			break
		}
	}

	return results, nil
}

// GetAlbumReleaseTracks загружает треклист альбома по browseId.
func (c *YouTubeMusicClient) GetAlbumReleaseTracks(ctx context.Context, browseID string) ([]AlbumTrackCandidate, error) {
	if browseID == "" {
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
		"browseId": browseID,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	targetURL := fmt.Sprintf("%s/youtubei/v1/browse", c.baseURL)
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
		return nil, fmt.Errorf("browse error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("browse returned HTTP %d", resp.StatusCode)
	}

	var root struct {
		Contents struct {
			TwoColumnBrowseResultsRenderer struct {
				SecondaryContents struct {
					SectionListRenderer struct {
						Contents []struct {
							MusicShelfRenderer struct {
								Contents []json.RawMessage `json:"contents"`
							} `json:"musicShelfRenderer"`
						} `json:"contents"`
					} `json:"sectionListRenderer"`
				} `json:"secondaryContents"`
				Tabs []struct {
					TabRenderer struct {
						Content struct {
							SectionListRenderer struct {
								Contents []struct {
									MusicShelfRenderer struct {
										Contents []json.RawMessage `json:"contents"`
									} `json:"musicShelfRenderer"`
								} `json:"contents"`
							} `json:"sectionListRenderer"`
						} `json:"content"`
					} `json:"tabRenderer"`
				} `json:"tabs"`
			} `json:"twoColumnBrowseResultsRenderer"`
		} `json:"contents"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&root); err != nil {
		return nil, fmt.Errorf("browse decode error: %w", err)
	}

	var rawItems []json.RawMessage
	twoCol := root.Contents.TwoColumnBrowseResultsRenderer

	// 1. Проверяем secondaryContents
	for _, sec := range twoCol.SecondaryContents.SectionListRenderer.Contents {
		if len(sec.MusicShelfRenderer.Contents) > 0 {
			rawItems = append(rawItems, sec.MusicShelfRenderer.Contents...)
		}
	}

	// 2. Проверяем tabs (если secondaryContents пуст)
	if len(rawItems) == 0 {
		for _, tab := range twoCol.Tabs {
			for _, sec := range tab.TabRenderer.Content.SectionListRenderer.Contents {
				if len(sec.MusicShelfRenderer.Contents) > 0 {
					rawItems = append(rawItems, sec.MusicShelfRenderer.Contents...)
				}
			}
		}
	}

	var tracks []AlbumTrackCandidate
	for _, item := range rawItems {
		var trItem struct {
			MusicResponsiveListItemRenderer struct {
				PlaylistItemData *struct {
					VideoId string `json:"videoId"`
				} `json:"playlistItemData"`
				FlexColumns []struct {
					MusicResponsiveListItemFlexColumnRenderer struct {
						Text struct {
							Runs []struct {
								Text               string `json:"text"`
								NavigationEndpoint *struct {
									WatchEndpoint *struct {
										VideoId string `json:"videoId"`
									} `json:"watchEndpoint"`
								} `json:"navigationEndpoint"`
							} `json:"runs"`
						} `json:"text"`
					} `json:"musicResponsiveListItemFlexColumnRenderer"`
				} `json:"flexColumns"`
				FixedColumns []struct {
					MusicResponsiveListItemFixedColumnRenderer struct {
						Text struct {
							Runs []struct {
								Text string `json:"text"`
							} `json:"runs"`
						} `json:"text"`
					} `json:"musicResponsiveListItemFixedColumnRenderer"`
				} `json:"fixedColumns"`
			} `json:"musicResponsiveListItemRenderer"`
		}

		if err := json.Unmarshal(item, &trItem); err != nil {
			continue
		}

		rend := trItem.MusicResponsiveListItemRenderer
		if len(rend.FlexColumns) == 0 {
			continue
		}

		vid := ""
		if rend.PlaylistItemData != nil && rend.PlaylistItemData.VideoId != "" {
			vid = rend.PlaylistItemData.VideoId
		}
		if vid == "" && len(rend.FlexColumns[0].MusicResponsiveListItemFlexColumnRenderer.Text.Runs) > 0 {
			if ep := rend.FlexColumns[0].MusicResponsiveListItemFlexColumnRenderer.Text.Runs[0].NavigationEndpoint; ep != nil && ep.WatchEndpoint != nil {
				vid = ep.WatchEndpoint.VideoId
			}
		}

		tit := ""
		if len(rend.FlexColumns[0].MusicResponsiveListItemFlexColumnRenderer.Text.Runs) > 0 {
			tit = strings.TrimSpace(rend.FlexColumns[0].MusicResponsiveListItemFlexColumnRenderer.Text.Runs[0].Text)
		}

		art := ""
		if len(rend.FlexColumns) > 1 && len(rend.FlexColumns[1].MusicResponsiveListItemFlexColumnRenderer.Text.Runs) > 0 {
			art = strings.TrimSpace(rend.FlexColumns[1].MusicResponsiveListItemFlexColumnRenderer.Text.Runs[0].Text)
		}

		durSec := 0
		if len(rend.FixedColumns) > 0 && len(rend.FixedColumns[0].MusicResponsiveListItemFixedColumnRenderer.Text.Runs) > 0 {
			durStr := rend.FixedColumns[0].MusicResponsiveListItemFixedColumnRenderer.Text.Runs[0].Text
			durSec = parseDurationSec(durStr)
		}

		if tit == "" {
			continue
		}

		cleanTit, _ := ExtractFeatFromText(tit)
		cleanTit = CleanYouTubeVideoTitle(cleanTit)

		tracks = append(tracks, AlbumTrackCandidate{
			VideoID:  vid,
			Title:    cleanTit,
			Artist:   art,
			Duration: durSec,
			Index:    len(tracks) + 1,
		})
	}

	return tracks, nil
}

