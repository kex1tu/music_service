package resolver

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// ContainsCyrillic проверяет наличие кириллических символов в строке.
func ContainsCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// ReleaseCandidateItem представляет отдельного кандидата цельного релиза (альбом/сет/плейлист)
// со статусом цензуры, соответствием треклиста и детализированным маппингом.
type ReleaseCandidateItem struct {
	ResultType        string                  `json:"result_type,omitempty"`
	MappingLimited    bool                    `json:"mapping_limited"`
	Policy            VersionPolicy           `json:"version_policy"`
	Evaluation        MatchEvaluation         `json:"evaluation"`
	TrackEvaluations  map[int]MatchEvaluation `json:"track_evaluations"`
	MeanTrackScore    int                     `json:"mean_track_score"`
	WeakestTrackScore int                     `json:"weakest_track_score"`
	MissingTracks     int                     `json:"missing_tracks"`
	ExtraTracks       int                     `json:"extra_tracks"`
	ID                string                  `json:"id"`
	Source            string                  `json:"source"` // "ytmusic_album", "soundcloud_set", "direct_url"
	Title             string                  `json:"title"`
	Artist            string                  `json:"artist"`
	URL               string                  `json:"url"`
	TrackCount        int                     `json:"track_count"`
	CoverURL          string                  `json:"cover_url"`
	Tracks            []AlbumTrackCandidate   `json:"tracks"`
	MatchedCount      int                     `json:"matched_count"`
	TotalCount        int                     `json:"total_count"`
	MatchedRatio      float64                 `json:"matched_ratio"` // 0.0 - 1.0
	TrackMapping      map[int]string          `json:"track_mapping"` // targetIdx -> candidateURL
	IsCyrillic        bool                    `json:"is_cyrillic"`
	CensorStatus      string                  `json:"censor_status"` // "red_flag" | "recommended" | "clean" | "neutral"
	CensorBadge       string                  `json:"censor_badge"`  // e.g. "🚩 Офиц. релиз (возможна цензура)" | "⭐ SoundCloud (оригинал)" | "⭐ Официальный релиз"
	Score             int                     `json:"score"`
}

// ScoreReleaseCandidate вычисляет оценку кандидата с учетом контекста языка (RU vs Зарубежный) и совпадения треклиста.
func ScoreReleaseCandidate(source string, isCyrillic bool, matchedRatio float64) (int, string, string) {
	if matchedRatio < 0 {
		matchedRatio = 0
	}
	if matchedRatio > 1 {
		matchedRatio = 1
	}
	return int(matchedRatio * 100), "unknown", "Цензура не проверена"
}

// FullReleaseMatchResult представляет результаты сопоставления цельного релиза.
type FullReleaseMatchResult struct {
	SourceURL    string         `json:"source_url"`
	SourceType   string         `json:"source_type"` // "ytmusic_album", "soundcloud_set"
	ReleaseTitle string         `json:"release_title"`
	MatchedCount int            `json:"matched_count"`
	TotalCount   int            `json:"total_count"`
	TrackURLs    map[int]string `json:"track_urls"` // индекс исходного трека (0..N-1) -> прямой AudioSourceURL
}

// FullReleaseMatcher координирует поиск и сопоставление цельных релизов (альбомов, EP, плейлистов).
type FullReleaseMatcher struct {
	ytMusic    *YouTubeMusicClient
	scSearcher *SoundCloudSetSearcher
}

// NewFullReleaseMatcher создает новый экземпляр FullReleaseMatcher.
func NewFullReleaseMatcher(ytMusic *YouTubeMusicClient, scSearcher ...*SoundCloudSetSearcher) *FullReleaseMatcher {
	if ytMusic == nil {
		ytMusic = NewYouTubeMusicClient("")
	}
	var sc *SoundCloudSetSearcher
	if len(scSearcher) > 0 {
		sc = scSearcher[0]
	} else {
		sc = NewSoundCloudSetSearcher("")
	}
	return &FullReleaseMatcher{
		ytMusic:    ytMusic,
		scSearcher: sc,
	}
}

// MatchTracklists сопоставляет треклист целевого альбома с треклистом кандидата.
// Возвращает маппинг индексов целевых треков на прямые URL и количество успешно сопоставленных треков.
func (m *FullReleaseMatcher) MatchTracklists(targetTracks []ResolvedTrack, candTracks []AlbumTrackCandidate) (map[int]string, int) {
	return assignTracklists(targetTracks, candTracks)
}

// MatchFullRelease ищет официальный релиз в каталоге YouTube Music и при совпадении треклиста >= 85% возвращает результат.
func (m *FullReleaseMatcher) MatchFullRelease(ctx context.Context, targetArtist, targetAlbum string, targetTracks []ResolvedTrack) (*FullReleaseMatchResult, error) {
	if len(targetTracks) < 2 {
		return nil, nil
	}

	// 1. Поиск официального альбома в YouTube Music
	albumCand, err := m.ytMusic.SearchAlbumRelease(ctx, targetArtist, targetAlbum)
	if err != nil || albumCand == nil || len(albumCand.Tracks) == 0 {
		return nil, err
	}

	// 2. Сверка треклиста
	urls, matched := m.MatchTracklists(targetTracks, albumCand.Tracks)
	total := len(targetTracks)
	ratio := float64(matched) / float64(total)

	// Порог принятия: не менее 85% совпадений
	if ratio >= 0.85 {
		sourceURL := ""
		if albumCand.PlaylistID != "" {
			sourceURL = fmt.Sprintf("https://music.youtube.com/playlist?list=%s", albumCand.PlaylistID)
		} else if albumCand.BrowseID != "" {
			sourceURL = fmt.Sprintf("https://music.youtube.com/browse/%s", albumCand.BrowseID)
		}

		return &FullReleaseMatchResult{
			SourceURL:    sourceURL,
			SourceType:   "ytmusic_album",
			ReleaseTitle: albumCand.Title,
			MatchedCount: matched,
			TotalCount:   total,
			TrackURLs:    urls,
		}, nil
	}

	return nil, nil
}

// SearchReleaseCandidates выполняет мульти-поиск кандидатов релиза (YouTube Music и SoundCloud)
// и возвращает отранжированный список кандидатов с маппингом треков и контекстными бейджами цензуры.
func (m *FullReleaseMatcher) SearchReleaseCandidates(ctx context.Context, targetArtist, targetAlbum string, targetTracks []ResolvedTrack, soundCloudLimit ...int) ([]ReleaseCandidateItem, bool, error) {
	limit := 30
	if len(soundCloudLimit) > 0 {
		limit = SoundCloudCandidateLimit(soundCloudLimit[0])
	}
	isCyrillic := ContainsCyrillic(targetArtist) || ContainsCyrillic(targetAlbum)
	if !isCyrillic {
		for _, t := range targetTracks {
			if ContainsCyrillic(t.Title) || ContainsCyrillic(t.Artist) {
				isCyrillic = true
				break
			}
		}
	}

	var items []ReleaseCandidateItem

	// 1. Поиск кандидатов на YouTube Music
	if m.ytMusic != nil {
		ytCands, err := m.ytMusic.SearchAlbumReleaseCandidates(ctx, targetArtist, targetAlbum, 4)
		if err == nil {
			for idx, cand := range ytCands {
				mapping, matched := m.MatchTracklists(targetTracks, cand.Tracks)
				ratio := 0.0
				if len(targetTracks) > 0 {
					ratio = float64(matched) / float64(len(targetTracks))
				}
				score, censorStatus, censorBadge := ScoreReleaseCandidate("ytmusic_album", isCyrillic, ratio)

				candURL := ""
				if cand.PlaylistID != "" {
					candURL = fmt.Sprintf("https://music.youtube.com/playlist?list=%s", cand.PlaylistID)
				} else if cand.BrowseID != "" {
					candURL = fmt.Sprintf("https://music.youtube.com/browse/%s", cand.BrowseID)
				}

				items = append(items, ReleaseCandidateItem{
					ID:           fmt.Sprintf("ytm_%d_%s", idx, cand.BrowseID),
					Source:       "ytmusic_album",
					Title:        cand.Title,
					Artist:       cand.Artist,
					URL:          candURL,
					TrackCount:   len(cand.Tracks),
					CoverURL:     cand.CoverURL,
					Tracks:       cand.Tracks,
					MatchedCount: matched,
					TotalCount:   len(targetTracks),
					MatchedRatio: ratio,
					TrackMapping: mapping,
					IsCyrillic:   isCyrillic,
					CensorStatus: censorStatus,
					CensorBadge:  censorBadge,
					Score:        score,
				})
			}
		}
	}

	// 2. Поиск кандидатов на SoundCloud
	if m.scSearcher != nil {
		scCands, err := m.scSearcher.SearchSoundCloudSets(ctx, targetArtist, targetAlbum, limit)
		if err == nil {
			for idx, cand := range scCands {
				mapping, matched := m.MatchTracklists(targetTracks, cand.Tracks)
				ratio := 0.0
				if len(targetTracks) > 0 {
					ratio = float64(matched) / float64(len(targetTracks))
				}
				score, censorStatus, censorBadge := ScoreReleaseCandidate("soundcloud_set", isCyrillic, ratio)

				items = append(items, ReleaseCandidateItem{
					ID:           fmt.Sprintf("sc_%d_%s", idx, cand.BrowseID),
					Source:       "soundcloud_set",
					ResultType:   cand.ResultType,
					Title:        cand.Title,
					Artist:       cand.Artist,
					URL:          cand.PlaylistID,
					TrackCount:   len(cand.Tracks),
					CoverURL:     cand.CoverURL,
					Tracks:       cand.Tracks,
					MatchedCount: matched,
					TotalCount:   len(targetTracks),
					MatchedRatio: ratio,
					TrackMapping: mapping,
					IsCyrillic:   isCyrillic,
					CensorStatus: censorStatus,
					CensorBadge:  censorBadge,
					Score:        score,
				})
			}
		}
	}

	// 3. Ранжирование кандидатов
	sort.Slice(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].MatchedCount > items[j].MatchedCount
	})

	return RankReleaseItems(items, targetArtist, targetAlbum, targetTracks, PolicyRelease), isCyrillic, nil
}

// ParseAndMatchReleaseURL разбирает произвольную прямую ссылку на сет/альбом и сопоставляет её с целевыми треками.
func (m *FullReleaseMatcher) ParseAndMatchReleaseURL(ctx context.Context, rawURL, targetArtist, targetAlbum string, targetTracks []ResolvedTrack) (*ReleaseCandidateItem, error) {
	if m.scSearcher == nil {
		m.scSearcher = NewSoundCloudSetSearcher("")
	}

	cand, err := m.scSearcher.ParseSetURL(ctx, rawURL)
	if err != nil {
		return nil, err
	}

	isCyrillic := ContainsCyrillic(targetArtist) || ContainsCyrillic(targetAlbum)
	if !isCyrillic {
		for _, t := range targetTracks {
			if ContainsCyrillic(t.Title) || ContainsCyrillic(t.Artist) {
				isCyrillic = true
				break
			}
		}
	}

	sourceType := "direct_url"
	if strings.Contains(rawURL, "soundcloud.com") {
		sourceType = "soundcloud_set"
	} else if strings.Contains(rawURL, "youtube.com") || strings.Contains(rawURL, "youtu.be") {
		sourceType = "ytmusic_album"
	}

	mapping, matched := m.MatchTracklists(targetTracks, cand.Tracks)
	ratio := 0.0
	if len(targetTracks) > 0 {
		ratio = float64(matched) / float64(len(targetTracks))
	}
	score, censorStatus, censorBadge := ScoreReleaseCandidate(sourceType, isCyrillic, ratio)

	return &ReleaseCandidateItem{
		ID:           "direct_custom",
		Source:       sourceType,
		Title:        cand.Title,
		Artist:       cand.Artist,
		URL:          rawURL,
		TrackCount:   len(cand.Tracks),
		CoverURL:     cand.CoverURL,
		Tracks:       cand.Tracks,
		MatchedCount: matched,
		TotalCount:   len(targetTracks),
		MatchedRatio: ratio,
		TrackMapping: mapping,
		IsCyrillic:   isCyrillic,
		CensorStatus: censorStatus,
		CensorBadge:  censorBadge,
		Score:        score,
	}, nil
}
