package resolver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// YtCandidate represents a search entry returned by yt-dlp.
type YtCandidate struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Uploader string  `json:"uploader"`
	Channel  string  `json:"channel"`
	Duration float64 `json:"duration"`
	URL      string  `json:"webpage_url"`
}

// AudioMatcher matches Deezer track metadata to SoundCloud or YouTube audio streams.
type AudioMatcher struct {
	cookiesPath string
}

// NewAudioMatcher creates a new AudioMatcher.
func NewAudioMatcher(cookiesPath string) *AudioMatcher {
	return &AudioMatcher{
		cookiesPath: cookiesPath,
	}
}

// FindBestMatch searches SoundCloud first (for uncut/explicit audio), falling back to YouTube.
func (m *AudioMatcher) FindBestMatch(ctx context.Context, title, artist string, duration int) (string, error) {
	if scURL, scErr := m.FindSoundCloudMatch(ctx, title, artist, duration); scErr == nil && scURL != "" {
		return scURL, nil
	}
	return m.FindYouTubeMatch(ctx, title, artist, duration)
}

// FindSoundCloudMatch specifically searches SoundCloud streams with scoring.
func (m *AudioMatcher) FindSoundCloudMatch(ctx context.Context, title, artist string, duration int) (string, error) {
	cleanArt := CleanQueryText(artist)
	cleanTit := CleanQueryText(title)
	cleanTitFull := CleanQueryTextFull(title)

	scQueries := []string{}
	if cleanTitFull != "" && cleanTitFull != cleanTit {
		scQueries = append(scQueries, fmt.Sprintf("scsearch5:%s %s", cleanArt, cleanTitFull))
	}
	if cleanTit != "" {
		scQueries = append(scQueries, fmt.Sprintf("scsearch5:%s %s", cleanArt, cleanTit))
	}
	for _, artVar := range TransliterateArtist(artist) {
		cleanVar := CleanQueryText(artVar)
		if cleanVar != "" && cleanVar != cleanArt {
			scQueries = append(scQueries, fmt.Sprintf("scsearch5:%s %s", cleanVar, cleanTit))
		}
	}

	for _, scQ := range scQueries {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		scCandidates, scErr := m.runSearch(ctx, scQ)
		if scErr == nil && len(scCandidates) > 0 {
			var bestScCandidate *YtCandidate
			bestScScore := -1.0
			for _, cand := range scCandidates {
				candChan := cand.Channel
				if candChan == "" {
					candChan = cand.Uploader
				}
				score := m.CalculateScore(title, artist, duration, cand.Title, candChan, int(cand.Duration))
				if score > bestScScore {
					bestScScore = score
					cCopy := cand
					bestScCandidate = &cCopy
				}
			}
			if bestScCandidate != nil && bestScScore >= 55.0 {
				if bestScCandidate.URL != "" {
					return bestScCandidate.URL, nil
				}
				if bestScCandidate.ID != "" {
					return fmt.Sprintf("https://soundcloud.com/%s", bestScCandidate.ID), nil
				}
			}
		}
	}

	return "", fmt.Errorf("no soundcloud match found for '%s - %s'", artist, title)
}

// FindYouTubeMatch specifically searches YouTube streams with scoring.
func (m *AudioMatcher) FindYouTubeMatch(ctx context.Context, title, artist string, duration int) (string, error) {
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
		fmt.Sprintf("ytsearch5:%s %s Audio", cleanArt, cleanTit),
		fmt.Sprintf("ytsearch5:%s %s", cleanArt, cleanTit),
	)
	for _, artVar := range TransliterateArtist(artist) {
		cleanVar := CleanQueryText(artVar)
		if cleanVar != "" && cleanVar != cleanArt {
			queries = append(queries, fmt.Sprintf("ytsearch5:%s %s Topic", cleanVar, cleanTit))
		}
	}

	var bestCandidate *YtCandidate
	bestScore := -1.0

	for _, q := range queries {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
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
			if score > bestScore {
				bestScore = score
				cCopy := cand
				bestCandidate = &cCopy
			}
		}

		if bestScore >= 65.0 {
			break
		}
	}

	if bestCandidate != nil && bestScore >= 25.0 {
		if bestCandidate.ID != "" {
			return fmt.Sprintf("https://www.youtube.com/watch?v=%s", bestCandidate.ID), nil
		}
		if bestCandidate.URL != "" {
			return bestCandidate.URL, nil
		}
	}

	return "", fmt.Errorf("no suitable audio match found for '%s - %s' (best score: %.1f)", artist, title, bestScore)
}

func (m *AudioMatcher) runSearch(ctx context.Context, query string) ([]YtCandidate, error) {
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
	if m.cookiesPath != "" {
		if _, err := os.Stat(m.cookiesPath); err == nil {
			args = append(args, "--cookies", m.cookiesPath)
		}
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "yt-dlp", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stdout.Len() == 0 {
			return nil, fmt.Errorf("yt-dlp search error: %w: %s", err, stderr.String())
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
