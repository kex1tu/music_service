package resolver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"strings"
	"time"

	"music-manager/internal/config"
	"music-manager/internal/core/mediaexec"
	"music-manager/internal/db"
)

// YtCandidate represents a search entry returned by yt-dlp.
type YtCandidate struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Uploader   string  `json:"uploader"`
	Channel    string  `json:"channel"`
	Duration   float64 `json:"duration"`
	URL        string  `json:"webpage_url"`
	UploadDate string  `json:"upload_date,omitempty"`
	Timestamp  int64   `json:"timestamp,omitempty"`
}

// EarlyExitMatchThreshold is retained for compatibility with integrations.
// Search no longer exits early or treats this uncalibrated rating as confidence.
const EarlyExitMatchThreshold = 85.0

// AudioMatcher matches Deezer track metadata to SoundCloud or YouTube audio streams.
type AudioMatcher struct {
	cookiesPath   string
	db            *db.DB
	cfg           *config.Config
	deezerClient  *DeezerClient
	itunesClient  *ITunesClient
	ytmusicClient *YouTubeMusicClient
	searchFn      func(ctx context.Context, query string) ([]YtCandidate, error)
}

// MatchResult содержит детальные сведения о подобранном аудио-источнике.
type MatchResult struct {
	Suggestions []CandidateTrackItem `json:"suggestions,omitempty"`
	URL         string
	Status      string // marked, likely, unverified, manual, or "" (for youtube)
	Duration    int
	Candidates  []db.SoundCloudCandidateItem
	MatchScore  float64
}

// NewAudioMatcher creates a new AudioMatcher.
func NewAudioMatcher(cookiesPath string) *AudioMatcher {
	return &AudioMatcher{
		cookiesPath: cookiesPath,
	}
}

// SetDB привязывает SQLite базу данных для кэширования кандидатов SoundCloud.
func (m *AudioMatcher) SetDB(database *db.DB) {
	m.db = database
}

// SetConfig устанавливает конфигурационные параметры для резолвера.
func (m *AudioMatcher) SetConfig(cfg *config.Config) {
	m.cfg = cfg
}

// SetCatalogClients привязывает клиенты каталогов для извлечения эталонных длительностей.
func (m *AudioMatcher) SetCatalogClients(deezer *DeezerClient, itunes *ITunesClient) {
	m.deezerClient = deezer
	m.itunesClient = itunes
}

// SetYtMusicClient привязывает клиент YouTube Music для быстрого поиска кандидатов.
func (m *AudioMatcher) SetYtMusicClient(ytmusic *YouTubeMusicClient) {
	m.ytmusicClient = ytmusic
}

// SetSearchFn устанавливает кастомную функцию поиска кандидатов (для изоляции сети и тестирования).
func (m *AudioMatcher) SetSearchFn(fn func(ctx context.Context, query string) ([]YtCandidate, error)) {
	m.searchFn = fn
}

// FindBestMatch searches SoundCloud first (for uncut/explicit audio), falling back to YouTube.
func (m *AudioMatcher) FindBestMatch(ctx context.Context, title, artist string, duration int) (string, error) {
	res, err := m.FindBestMatchWithDetails(ctx, title, artist, "", duration)
	if err != nil {
		return "", err
	}
	if res.URL == "" {
		return "", fmt.Errorf("manual source selection required")
	}
	return res.URL, nil
}

// FindBestMatchWithDetails returns a combined suggestion list. Cached/pinned
// URLs do not authorize application; URL stays empty pending a user choice.
func (m *AudioMatcher) FindBestMatchWithDetails(ctx context.Context, title, artist, isrc string, duration int) (*MatchResult, error) {
	items, err := m.SearchMultiCandidates(ctx, artist, title, duration)
	if err != nil {
		return nil, err
	}
	return &MatchResult{Status: "waiting_source", Suggestions: items}, nil
}

// FindUncensoredSoundCloudMatch executes candidate ranking on SoundCloud and caches the result.
func (m *AudioMatcher) FindUncensoredSoundCloudMatch(ctx context.Context, title, artist, isrc string, duration int) (*MatchResult, error) {
	cleanArt := CleanQueryText(artist)
	cleanTit := CleanQueryText(title)
	cleanTitFull := CleanQueryTextFull(title)

	scQueries := []string{}
	if cleanTitFull != "" && cleanTitFull != cleanTit {
		scQueries = append(scQueries, fmt.Sprintf("scsearch10:%s %s", cleanArt, cleanTitFull))
	}
	if cleanTit != "" {
		scQueries = append(scQueries, fmt.Sprintf("scsearch10:%s %s", cleanArt, cleanTit))
	}
	for _, artVar := range TransliterateArtist(artist) {
		cleanVar := CleanQueryText(artVar)
		if cleanVar != "" && cleanVar != cleanArt {
			scQueries = append(scQueries, fmt.Sprintf("scsearch10:%s %s", cleanVar, cleanTit))
		}
	}

	var allCandidates []YtCandidate
	seenIDs := make(map[string]bool)

	for _, scQ := range scQueries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		candidates, err := m.runSearch(ctx, scQ)
		if err == nil {
			for _, c := range candidates {
				if !seenIDs[c.ID] {
					seenIDs[c.ID] = true
					allCandidates = append(allCandidates, c)
				}
			}
		}
	}

	if len(allCandidates) == 0 {
		return nil, fmt.Errorf("no soundcloud candidates found for '%s - %s'", artist, title)
	}

	// Определение эталонных хронометражей
	validDurs, earlyDur, refMissing := DetermineValidDurations(ctx, m.deezerClient, m.itunesClient, artist, title, duration)

	// Конфигурационные параметры
	tolerance := 2
	limit := 10
	preferYear := 2023
	var distributors []string
	if m.cfg != nil {
		if m.cfg.UncensoredDurationTolerance > 0 {
			tolerance = m.cfg.UncensoredDurationTolerance
		}
		if m.cfg.UncensoredCandidatesLimit > 0 {
			limit = m.cfg.UncensoredCandidatesLimit
		}
		if m.cfg.UncensoredPreferYear > 0 {
			preferYear = m.cfg.UncensoredPreferYear
		}
		distributors = m.cfg.UncensoredDistributors
	}
	if len(distributors) == 0 {
		distributors = []string{"distrokid", "believe", "zvonko", "the orchard", "tunecore", "warner", "universal", "sony"}
	}

	ranked := RankCandidates(allCandidates, artist, title, validDurs, earlyDur, refMissing, tolerance, limit, preferYear, distributors)
	if len(ranked) == 0 {
		return nil, fmt.Errorf("all soundcloud candidates rejected for '%s - %s'", artist, title)
	}

	best := ranked[0]
	cacheKey := BuildCacheKey(isrc, artist, title)

	// Сохранение в SQLite кэш
	if m.db != nil {
		entry := db.SoundCloudCacheEntry{
			CacheKey:    cacheKey,
			TrackTitle:  title,
			ArtistName:  artist,
			SelectedURL: best.URL,
			Status:      best.Status,
			Duration:    best.Duration,
			IsManual:    false,
			Candidates:  ranked,
		}
		_ = m.db.SaveSoundCloudCache(ctx, entry)
	}

	return &MatchResult{
		URL:        best.URL,
		Status:     best.Status,
		Duration:   best.Duration,
		Candidates: ranked,
	}, nil
}

// FindSoundCloudMatch specifically searches SoundCloud streams with scoring.
func (m *AudioMatcher) FindSoundCloudMatch(ctx context.Context, title, artist string, duration int) (string, error) {
	res, err := m.FindUncensoredSoundCloudMatch(ctx, title, artist, "", duration)
	if err == nil && res != nil && res.URL != "" {
		return res.URL, nil
	}
	return "", err
}

// FindYouTubeMatch specifically searches YouTube streams with scoring.
func (m *AudioMatcher) FindYouTubeMatch(ctx context.Context, title, artist string, duration int) (string, error) {
	url, _, err := m.FindYouTubeMatchWithScore(ctx, title, artist, duration)
	return url, err
}

// FindYouTubeMatchWithScore searches YouTube streams and returns the best candidate URL and confidence score.
func (m *AudioMatcher) FindYouTubeMatchWithScore(ctx context.Context, title, artist string, duration int) (string, float64, error) {
	cleanArt := CleanQueryText(artist)
	cleanTit := CleanQueryText(title)
	cleanTitFull := CleanQueryTextFull(title)

	queries := []string{}
	// Приоритет Topic-каналов и Explicit студийных треков над клиповыми версиями с цензурой
	queries = append(queries,
		fmt.Sprintf("ytsearch5:%s %s Topic", cleanArt, cleanTit),
		fmt.Sprintf("ytsearch5:%s %s Explicit", cleanArt, cleanTit),
	)
	if cleanTitFull != "" && cleanTitFull != cleanTit {
		queries = append(queries, fmt.Sprintf("ytsearch5:%s %s Topic", cleanArt, cleanTitFull))
	}
	queries = append(queries,
		fmt.Sprintf("ytsearch10:%s %s Audio", cleanArt, cleanTit),
		fmt.Sprintf("ytsearch10:%s %s", cleanArt, cleanTit),
	)
	for _, artVar := range TransliterateArtist(artist) {
		cleanVar := CleanQueryText(artVar)
		if cleanVar != "" && cleanVar != cleanArt {
			queries = append(queries, fmt.Sprintf("ytsearch5:%s %s Topic", cleanVar, cleanTit))
		}
	}

	// Фоллбэк запросы на случай фильтрации специфических слов в названии SafeSearch алгоритмами
	words := strings.Fields(cleanTit)
	if len(words) >= 2 {
		queries = append(queries, fmt.Sprintf("ytsearch10:%s %s", cleanArt, words[0]))
	}
	if len(words) >= 3 {
		queries = append(queries, fmt.Sprintf("ytsearch10:%s %s %s", cleanArt, words[0], words[1]))
	}

	var bestCandidate *YtCandidate
	bestScore := -1.0

	for _, q := range queries {
		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		default:
		}

		candidates, err := m.runSearch(ctx, q)
		if err != nil {
			continue
		}

		for _, cand := range candidates {
			candChan := cand.Channel
			if candChan == "" {
				candChan = cand.Uploader
			}
			score := m.CalculateScore(title, artist, duration, cand.Title, candChan, int(cand.Duration))
			if score > bestScore && EvaluateTrack(title, artist, duration, cand.Title, candChan, int(cand.Duration), true).Compatible {
				bestScore = score
				cCopy := cand
				bestCandidate = &cCopy
			}
		}

	}

	if bestCandidate != nil && bestScore >= 25.0 {
		if bestCandidate.ID != "" {
			return fmt.Sprintf("https://www.youtube.com/watch?v=%s", bestCandidate.ID), bestScore, nil
		}
		if bestCandidate.URL != "" {
			return bestCandidate.URL, bestScore, nil
		}
	}

	return "", bestScore, fmt.Errorf("no suitable audio match found for '%s - %s' (best score: %.1f)", artist, title, bestScore)
}

func (m *AudioMatcher) runSearch(ctx context.Context, query string) ([]YtCandidate, error) {
	if m.searchFn != nil {
		return m.searchFn(ctx, query)
	}

	defaultSearch := "ytsearch"
	if strings.HasPrefix(query, "scsearch") {
		defaultSearch = "scsearch"
	}

	args := []string{
		"--ignore-config",
		"--no-download-archive",
		"--dump-json",
		"--flat-playlist",
		"--default-search", defaultSearch,
		"--no-playlist",
		"--no-warnings",
		"--ignore-errors",
		query,
	}
	useCookies := IsValidNetscapeCookiesFile(m.cookiesPath)
	if useCookies {
		args = append(args, "--cookies", m.cookiesPath)
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := mediaexec.CommandContext(cmdCtx, "yt-dlp", args...)
	defer cmd.Close()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if useCookies && strings.Contains(stderr.String(), "does not look like a Netscape format cookies file") {
			argsNoCookies := make([]string, 0, len(args))
			for i := 0; i < len(args); i++ {
				if args[i] == "--cookies" {
					i++
					continue
				}
				argsNoCookies = append(argsNoCookies, args[i])
			}
			cmdRetry := mediaexec.CommandContext(cmdCtx, "yt-dlp", argsNoCookies...)
			defer cmdRetry.Close()
			stdout.Reset()
			stderr.Reset()
			cmdRetry.Stdout = &stdout
			cmdRetry.Stderr = &stderr
			err = cmdRetry.Run()
		}
		if err != nil && stdout.Len() == 0 {
			cleanStderr := CleanYtDlpStderr(stderr.String())
			return nil, fmt.Errorf("yt-dlp search error: %w: %s", err, cleanStderr)
		}
	}

	var results []YtCandidate
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var item YtCandidate
		if err := json.Unmarshal(line, &item); err == nil && item.ID != "" {
			if item.Channel == "" && item.Uploader != "" {
				item.Channel = item.Uploader
			}
			results = append(results, item)
		}
	}

	return results, nil
}
