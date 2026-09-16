package resolver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var (
	parenRegex   = regexp.MustCompile(`[\(\[\{].*?[\)\]\}]`)
	nonAlnumReg  = regexp.MustCompile(`[^\p{L}\p{N}\s]`)
	multiSpaceReg = regexp.MustCompile(`\s+`)
	badKeywords  = []string{
		"live", "concert", "cover", "karaoke", "remix", "instrumental",
		"acoustic", "tutorial", "reaction", "speed up", "slowed",
		"nightcore", "bass boosted", "8d", "clean", "radio edit",
	}
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

// AudioMatcher matches Deezer track metadata to YouTube/YouTube Music streams.
type AudioMatcher struct {
	cookiesPath string
}

// NewAudioMatcher creates a new AudioMatcher.
func NewAudioMatcher(cookiesPath string) *AudioMatcher {
	return &AudioMatcher{
		cookiesPath: cookiesPath,
	}
}

// CleanQueryText normalizes text for search queries.
func CleanQueryText(s string) string {
	s = parenRegex.ReplaceAllString(s, " ")
	s = nonAlnumReg.ReplaceAllString(s, " ")
	s = multiSpaceReg.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// CalculateScore computes a confidence score (0..100) for a candidate match.
func (m *AudioMatcher) CalculateScore(targetTitle, targetArtist string, targetDuration int, candidateTitle, candidateChannel string, candidateDuration int) float64 {
	score := 0.0

	tTitle := strings.ToLower(CleanQueryText(targetTitle))
	cTitle := strings.ToLower(CleanQueryText(candidateTitle))

	// 1. Duration similarity (up to 40 points)
	if targetDuration > 0 && candidateDuration > 0 {
		diff := math.Abs(float64(targetDuration - candidateDuration))
		if diff <= 2 {
			score += 40.0
		} else if diff <= 5 {
			score += 35.0
		} else if diff <= 10 {
			score += 25.0
		} else if diff <= 20 {
			score += 10.0
		} else if diff > 60 {
			score -= 30.0
		}
	}

	// 2. Title similarity (up to 35 points)
	if tTitle != "" && cTitle != "" {
		if strings.Contains(cTitle, tTitle) || strings.Contains(tTitle, cTitle) {
			score += 35.0
		} else {
			// Count overlapping words
			tWords := strings.Fields(tTitle)
			matches := 0
			for _, w := range tWords {
				if len(w) > 2 && strings.Contains(cTitle, w) {
					matches++
				}
			}
			if len(tWords) > 0 {
				ratio := float64(matches) / float64(len(tWords))
				score += ratio * 30.0
			}
		}
	}

	// 3. Artist similarity (up to 20 points)
	tArt := strings.ToLower(CleanQueryText(targetArtist))
	cChan := strings.ToLower(CleanQueryText(candidateChannel))
	if tArt != "" {
		if strings.Contains(cTitle, tArt) || (cChan != "" && strings.Contains(cChan, tArt)) {
			score += 20.0
		}
	}

	// 4. Channel bonus
	candLower := strings.ToLower(candidateChannel)
	if strings.HasSuffix(candLower, " - topic") {
		score += 15.0 // Official YouTube Music track
	} else if strings.Contains(candLower, "vevo") || strings.Contains(candLower, "official") {
		score += 10.0
	}

	// 5. Bad keywords penalty
	rawCandLower := strings.ToLower(candidateTitle)
	rawTargetLower := strings.ToLower(targetTitle)
	for _, kw := range badKeywords {
		if strings.Contains(rawCandLower, kw) && !strings.Contains(rawTargetLower, kw) {
			score -= 35.0
			break
		}
	}

	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

// FindBestMatch searches for a YouTube audio match and returns the video URL.
func (m *AudioMatcher) FindBestMatch(ctx context.Context, title, artist string, duration int) (string, error) {
	cleanArt := CleanQueryText(artist)
	cleanTit := CleanQueryText(title)

	queries := []string{
		fmt.Sprintf("ytsearch5:%s %s Official Audio", cleanArt, cleanTit),
		fmt.Sprintf("ytsearch5:%s %s Topic", cleanArt, cleanTit),
		fmt.Sprintf("ytsearch5:%s %s", cleanArt, cleanTit),
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
			score := m.CalculateScore(title, artist, duration, cand.Title, cand.Channel, int(cand.Duration))
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
	args := []string{
		"--dump-json",
		"--default-search", "ytsearch",
		"--no-playlist",
		"--no-warnings",
		"--ignore-errors",
		query,
	}
	if m.cookiesPath != "" {
		if _, err := os.Stat(m.cookiesPath); err == nil {
			args = append(args, "--cookies", m.cookiesPath)
		} else {
			args = append(args, "--extractor-args", "youtube:player_client=ios,visionos")
		}
	} else {
		args = append(args, "--extractor-args", "youtube:player_client=ios,visionos")
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "yt-dlp", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// yt-dlp might exit with 1 on some skipped entries, but still produce valid JSON lines
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
