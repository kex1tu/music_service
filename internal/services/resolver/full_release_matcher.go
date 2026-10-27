package resolver

import (
	"context"
	"fmt"
	"math"
	"strings"
)

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
	ytMusic *YouTubeMusicClient
}

// NewFullReleaseMatcher создает новый экземпляр FullReleaseMatcher.
func NewFullReleaseMatcher(ytMusic *YouTubeMusicClient) *FullReleaseMatcher {
	if ytMusic == nil {
		ytMusic = NewYouTubeMusicClient("")
	}
	return &FullReleaseMatcher{
		ytMusic: ytMusic,
	}
}

// MatchTracklists сопоставляет треклист целевого альбома с треклистом кандидата.
// Возвращает маппинг индексов целевых треков на прямые URL и количество успешно сопоставленных треков.
func (m *FullReleaseMatcher) MatchTracklists(targetTracks []ResolvedTrack, candTracks []AlbumTrackCandidate) (map[int]string, int) {
	urls := make(map[int]string)
	matched := 0
	usedCandidates := make(map[int]bool)

	for targetIdx, target := range targetTracks {
		normTarget := strings.ToLower(NormalizeTrackTitleForMatching(target.Title))

		bestCandIdx := -1
		bestScore := 0.0

		for candIdx, cand := range candTracks {
			if usedCandidates[candIdx] {
				continue
			}

			// Проверка дельты длительности: не более 12 секунд
			if target.Duration > 0 && cand.Duration > 0 {
				delta := math.Abs(float64(target.Duration - cand.Duration))
				if delta > 12.0 {
					continue
				}
			}

			normCand := strings.ToLower(NormalizeTrackTitleForMatching(cand.Title))

			sim := LevenshteinRatio(normTarget, normCand)
			if (normTarget != "" && strings.Contains(normCand, normTarget)) || (normCand != "" && strings.Contains(normTarget, normCand)) {
				if sim < 0.75 {
					sim = 0.75
				}
			}

			// Бонус за близкую длительность
			durBonus := 0.0
			if target.Duration > 0 && cand.Duration > 0 {
				diff := math.Abs(float64(target.Duration - cand.Duration))
				if diff <= 2.0 {
					durBonus = 0.15
				} else if diff <= 5.0 {
					durBonus = 0.08
				}
			}

			score := sim + durBonus
			if sim >= 0.65 && score > bestScore {
				bestScore = score
				bestCandIdx = candIdx
			}
		}

		if bestCandIdx != -1 {
			usedCandidates[bestCandIdx] = true
			urls[targetIdx] = fmt.Sprintf("https://music.youtube.com/watch?v=%s", candTracks[bestCandIdx].VideoID)
			matched++
		}
	}

	return urls, matched
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
