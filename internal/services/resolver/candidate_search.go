package resolver

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// CandidateTrackItem представляет найденного кандидата для ручного выбора источника.
type CandidateTrackItem struct {
	SavedSelection   bool            `json:"saved_selection"`
	SearchIncomplete bool            `json:"search_incomplete"`
	ID               string          `json:"id"`
	Source           string          `json:"source"` // "ytmusic", "soundcloud", "youtube", "bandcamp"
	Title            string          `json:"title"`
	Uploader         string          `json:"uploader"`
	URL              string          `json:"url"`
	Duration         int             `json:"duration"`
	DeltaSec         int             `json:"delta_seconds"`
	BitrateInfo      string          `json:"bitrate_info"` // e.g. "Opus 160k", "AAC 256k", "MP3 128k"
	IsTopic          bool            `json:"is_topic"`
	Score            int             `json:"score"`
	Evaluation       MatchEvaluation `json:"evaluation"`
	Policy           VersionPolicy   `json:"version_policy"`
}

// SearchMultiCandidates выполняет параллельный Fast Fan-Out поиск кандидатов
// по YouTube Music (нативный API), SoundCloud (scsearch) и YouTube (ytsearch).
func (m *AudioMatcher) SearchMultiCandidates(ctx context.Context, artist, title string, expectedDuration int, customQuery ...string) ([]CandidateTrackItem, error) {
	query := ""
	if len(customQuery) > 0 && strings.TrimSpace(customQuery[0]) != "" {
		query = customQuery[0]
	}
	return m.SearchMultiCandidatesWithLimit(ctx, artist, title, expectedDuration, query, 30)
}

func (m *AudioMatcher) SearchMultiCandidatesWithLimit(ctx context.Context, artist, title string, expectedDuration int, query string, soundCloudLimit int) ([]CandidateTrackItem, error) {
	q := strings.TrimSpace(fmt.Sprintf("%s - %s", artist, title))
	if strings.TrimSpace(query) != "" {
		q = strings.TrimSpace(query)
	}
	if q == "" || q == "-" {
		return nil, fmt.Errorf("empty query for candidate search")
	}

	searchCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var mu sync.Mutex
	var allCandidates []CandidateTrackItem
	seenURLs := make(map[string]bool)
	incomplete := false
	markIncomplete := func() { mu.Lock(); incomplete = true; mu.Unlock() }

	addCandidates := func(items []CandidateTrackItem) {
		mu.Lock()
		defer mu.Unlock()
		for _, item := range items {
			key := item.Source + ":" + candidateKey(item.URL)
			if item.URL == "" || seenURLs[key] {
				continue
			}
			seenURLs[key] = true
			allCandidates = append(allCandidates, item)
		}
	}

	// 1. YouTube Music Innertube API
	wg.Add(1)
	go func() {
		defer wg.Done()
		ytmClient := m.ytmusicClient
		if ytmClient == nil {
			ytmClient = NewYouTubeMusicClient("")
		}
		ytmTracks, err := ytmClient.Search(searchCtx, q)
		if err != nil {
			markIncomplete()
		}
		if err == nil && len(ytmTracks) > 0 {
			var res []CandidateTrackItem
			for _, tr := range ytmTracks {
				if tr.VideoID == "" {
					continue
				}
				delta := 0
				if expectedDuration > 0 && tr.Duration > 0 {
					delta = tr.Duration - expectedDuration
				}
				url := fmt.Sprintf("https://music.youtube.com/watch?v=%s", tr.VideoID)
				res = append(res, CandidateTrackItem{
					ID:          tr.VideoID,
					Source:      "ytmusic",
					Title:       tr.Title,
					Uploader:    tr.Artist,
					URL:         url,
					Duration:    tr.Duration,
					DeltaSec:    delta,
					BitrateInfo: "Opus 160k",
					IsTopic:     true,
					Score:       calculateCandidateScore(tr.Title, tr.Artist, title, artist, tr.Duration, expectedDuration, true, "ytmusic"),
				})
			}
			addCandidates(res)
		}
	}()

	// 2. SoundCloud via yt-dlp scsearch
	wg.Add(1)
	go func() {
		defer wg.Done()
		cleanQ := CleanQueryText(q)
		if cleanQ == "" {
			cleanQ = q
		}
		scQuery := fmt.Sprintf("scsearch%d:%s", SoundCloudCandidateLimit(soundCloudLimit), cleanQ)
		scCandidates, err := m.runSearch(searchCtx, scQuery)
		if err != nil {
			markIncomplete()
		}
		if err == nil && len(scCandidates) > 0 {
			var res []CandidateTrackItem
			for _, cand := range scCandidates {
				dur := int(cand.Duration + 0.5)
				delta := 0
				if expectedDuration > 0 && dur > 0 {
					delta = dur - expectedDuration
				}
				res = append(res, CandidateTrackItem{
					ID:          cand.ID,
					Source:      "soundcloud",
					Title:       cand.Title,
					Uploader:    cand.Channel,
					URL:         cand.URL,
					Duration:    dur,
					DeltaSec:    delta,
					BitrateInfo: "MP3 128k",
					IsTopic:     false,
					Score:       calculateCandidateScore(cand.Title, cand.Channel, title, artist, dur, expectedDuration, false, "soundcloud"),
				})
			}
			addCandidates(res)
		}
	}()

	// 3. YouTube Search (Topic and standard videos)
	wg.Add(1)
	go func() {
		defer wg.Done()
		cleanQ := CleanQueryText(q)
		if cleanQ == "" {
			cleanQ = q
		}
		ytQuery := fmt.Sprintf("ytsearch10:%s", cleanQ)
		ytCandidates, err := m.runSearch(searchCtx, ytQuery)
		if err != nil {
			markIncomplete()
		}
		if err == nil && len(ytCandidates) > 0 {
			var res []CandidateTrackItem
			for _, cand := range ytCandidates {
				dur := int(cand.Duration + 0.5)
				delta := 0
				if expectedDuration > 0 && dur > 0 {
					delta = dur - expectedDuration
				}
				chanLower := strings.ToLower(cand.Channel)
				titleLower := strings.ToLower(cand.Title)
				isTopic := strings.Contains(chanLower, "- topic") || strings.Contains(chanLower, "topic") || strings.Contains(titleLower, "provided to youtube")

				bitrate := "AAC 128k"
				if isTopic {
					bitrate = "Opus 160k"
				}

				itemURL := cand.URL
				if itemURL == "" && cand.ID != "" {
					itemURL = fmt.Sprintf("https://www.youtube.com/watch?v=%s", cand.ID)
				}

				res = append(res, CandidateTrackItem{
					ID:          cand.ID,
					Source:      "youtube",
					Title:       cand.Title,
					Uploader:    cand.Channel,
					URL:         itemURL,
					Duration:    dur,
					DeltaSec:    delta,
					BitrateInfo: bitrate,
					IsTopic:     isTopic,
					Score:       calculateCandidateScore(cand.Title, cand.Channel, title, artist, dur, expectedDuration, isTopic, "youtube"),
				})
			}
			addCandidates(res)
		}
	}()

	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for i := range allCandidates {
		allCandidates[i].SearchIncomplete = incomplete
	}
	if len(allCandidates) == 0 && incomplete {
		return nil, fmt.Errorf("candidate search incomplete: providers unavailable")
	}

	// Сортировка кандидатов по убыванию Score, а при равенстве — по минимальному |DeltaSec|
	sort.Slice(allCandidates, func(i, j int) bool {
		if allCandidates[i].Score != allCandidates[j].Score {
			return allCandidates[i].Score > allCandidates[j].Score
		}
		return math.Abs(float64(allCandidates[i].DeltaSec)) < math.Abs(float64(allCandidates[j].DeltaSec))
	})

	return RankCandidateItems(allCandidates, title, artist, expectedDuration, PolicyRelease), nil
}

// calculateCandidateScore вычисляет оценку соответствия кандидата от 0 до 100.
func calculateCandidateScore(candTitle, candChannel, origTitle, origArtist string, candDur, expectedDur int, isTopic bool, source string) int {
	return EvaluateTrack(origTitle, origArtist, expectedDur, candTitle, candChannel, candDur, source != "ytmusic").Score
}
