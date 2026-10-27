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
	ID          string `json:"id"`
	Source      string `json:"source"`       // "ytmusic", "soundcloud", "youtube", "bandcamp"
	Title       string `json:"title"`
	Uploader    string `json:"uploader"`
	URL         string `json:"url"`
	Duration    int    `json:"duration"`
	DeltaSec    int    `json:"delta_seconds"`
	BitrateInfo string `json:"bitrate_info"` // e.g. "Opus 160k", "AAC 256k", "MP3 128k"
	IsTopic     bool   `json:"is_topic"`
	Score       int    `json:"score"`
}

// SearchMultiCandidates выполняет параллельный Fast Fan-Out поиск кандидатов
// по YouTube Music (нативный API), SoundCloud (scsearch) и YouTube (ytsearch).
func (m *AudioMatcher) SearchMultiCandidates(ctx context.Context, artist, title string, expectedDuration int, customQuery ...string) ([]CandidateTrackItem, error) {
	q := strings.TrimSpace(fmt.Sprintf("%s - %s", artist, title))
	if len(customQuery) > 0 && strings.TrimSpace(customQuery[0]) != "" {
		q = strings.TrimSpace(customQuery[0])
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

	addCandidates := func(items []CandidateTrackItem) {
		mu.Lock()
		defer mu.Unlock()
		for _, item := range items {
			if item.URL == "" || seenURLs[item.URL] {
				continue
			}
			seenURLs[item.URL] = true
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
		scQuery := fmt.Sprintf("scsearch10:%s", cleanQ)
		scCandidates, err := m.runSearch(searchCtx, scQuery)
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

	// Сортировка кандидатов по убыванию Score, а при равенстве — по минимальному |DeltaSec|
	sort.Slice(allCandidates, func(i, j int) bool {
		if allCandidates[i].Score != allCandidates[j].Score {
			return allCandidates[i].Score > allCandidates[j].Score
		}
		return math.Abs(float64(allCandidates[i].DeltaSec)) < math.Abs(float64(allCandidates[j].DeltaSec))
	})

	return allCandidates, nil
}

// calculateCandidateScore вычисляет оценку соответствия кандидата от 0 до 100.
func calculateCandidateScore(candTitle, candChannel, origTitle, origArtist string, candDur, expectedDur int, isTopic bool, source string) int {
	score := 50

	// 1. Бонус за источник
	if isTopic || source == "ytmusic" {
		score += 25
	} else if source == "soundcloud" {
		score += 10
	}

	// 2. Дельта длительности
	if expectedDur > 0 && candDur > 0 {
		diff := int(math.Abs(float64(candDur - expectedDur)))
		if diff <= 2 {
			score += 25
		} else if diff <= 5 {
			score += 15
		} else if diff <= 10 {
			score += 5
		} else if diff > 30 {
			score -= 30
		}
	}

	// 3. Совпадение названия
	cleanCandTitle := strings.ToLower(CleanQueryText(candTitle))
	cleanOrigTitle := strings.ToLower(CleanQueryText(origTitle))
	if cleanOrigTitle != "" && strings.Contains(cleanCandTitle, cleanOrigTitle) {
		score += 15
	}

	// 4. Совпадение исполнителя
	cleanCandChan := strings.ToLower(CleanQueryText(candChannel))
	cleanOrigArtist := strings.ToLower(CleanQueryText(origArtist))
	if cleanOrigArtist != "" && (strings.Contains(cleanCandChan, cleanOrigArtist) || strings.Contains(cleanCandTitle, cleanOrigArtist)) {
		score += 10
	}

	// Штрафы за нежелательные слова (live, concert, 8d audio, slowed, remix если в оригинале нет remix)
	if !strings.Contains(cleanOrigTitle, "live") && strings.Contains(cleanCandTitle, "live") {
		score -= 20
	}
	if !strings.Contains(cleanOrigTitle, "remix") && strings.Contains(cleanCandTitle, "remix") {
		score -= 15
	}
	if strings.Contains(cleanCandTitle, "slowed") || strings.Contains(cleanCandTitle, "reverb") || strings.Contains(cleanCandTitle, "speed up") {
		score -= 35
	}

	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return score
}
