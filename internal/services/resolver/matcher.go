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
	parenRegex    = regexp.MustCompile(`[\(\[\{].*?[\)\]\}]`)
	combiningReg  = regexp.MustCompile(`[\p{M}]`)
	nonAlnumReg   = regexp.MustCompile(`[^\p{L}\p{N}\s]`)
	multiSpaceReg = regexp.MustCompile(`\s+`)
	badKeywords   = []string{
		"live", "concert", "cover", "karaoke", "remix", "instrumental",
		"acoustic", "tutorial", "reaction", "speed up", "slowed",
		"nightcore", "bass boosted", "8d", "clean", "radio edit",
	}
	cyrToLat = map[rune]string{
		'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
		'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
		'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
		'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
		'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
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

// NormalizeUnicodeText converts decomposed combining characters (NFD) to precomposed NFC.
func NormalizeUnicodeText(s string) string {
	s = strings.ReplaceAll(s, "\u0438\u0306", "й")
	s = strings.ReplaceAll(s, "\u0418\u0306", "Й")
	s = strings.ReplaceAll(s, "\u0435\u0308", "ё")
	s = strings.ReplaceAll(s, "\u0415\u0308", "Ё")
	s = strings.ReplaceAll(s, "—", "-")
	s = strings.ReplaceAll(s, "–", "-")
	s = strings.ReplaceAll(s, "“", "\"")
	s = strings.ReplaceAll(s, "”", "\"")
	s = strings.ReplaceAll(s, "«", "\"")
	s = strings.ReplaceAll(s, "»", "\"")
	s = combiningReg.ReplaceAllString(s, "")
	return s
}

// TransliterateArtist generates Latin equivalents for Cyrillic artist names (e.g. "макс корж" -> ["maks korzh", "max korzh"]).
func TransliterateArtist(s string) []string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		if lat, ok := cyrToLat[r]; ok {
			sb.WriteString(lat)
		} else {
			sb.WriteRune(r)
		}
	}
	t1 := sb.String()
	t2 := strings.ReplaceAll(t1, "ks", "x")
	if t1 == t2 {
		return []string{t1}
	}
	return []string{t1, t2}
}

// CleanQueryText normalizes text for search queries and matching.
func CleanQueryText(s string) string {
	s = NormalizeUnicodeText(s)
	s = parenRegex.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "ё", "е")
	s = strings.ReplaceAll(s, "Ё", "Е")
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

	// 3. Artist similarity (up to 20 points, with transliteration support)
	tArt := strings.ToLower(CleanQueryText(targetArtist))
	cChan := strings.ToLower(CleanQueryText(candidateChannel))
	if tArt != "" {
		matched := false
		if strings.Contains(cTitle, tArt) || (cChan != "" && strings.Contains(cChan, tArt)) {
			matched = true
		} else {
			// Check transliterated variants
			tVariants := TransliterateArtist(tArt)
			cChanTrans := TransliterateArtist(cChan)
			cTitleTrans := TransliterateArtist(cTitle)

			for _, tv := range tVariants {
				if len(tv) < 3 {
					continue
				}
				for _, cct := range cChanTrans {
					if strings.Contains(cct, tv) || strings.Contains(tv, cct) {
						matched = true
						break
					}
				}
				if matched {
					break
				}
				for _, ctt := range cTitleTrans {
					if strings.Contains(ctt, tv) {
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
		}

		if matched {
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

// FindBestMatch searches SoundCloud first (for uncut/explicit audio), falling back to YouTube.
func (m *AudioMatcher) FindBestMatch(ctx context.Context, title, artist string, duration int) (string, error) {
	cleanArt := CleanQueryText(artist)
	cleanTit := CleanQueryText(title)

	// Step 1: SoundCloud Search (priority for explicit/original audio)
	scQuery := fmt.Sprintf("scsearch3:%s %s", cleanArt, cleanTit)
	scCandidates, scErr := m.runSearch(ctx, scQuery)
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
		if bestScCandidate != nil && bestScScore >= 65.0 {
			if bestScCandidate.URL != "" {
				return bestScCandidate.URL, nil
			}
			if bestScCandidate.ID != "" {
				return fmt.Sprintf("https://soundcloud.com/%s", bestScCandidate.ID), nil
			}
		}
	}

	// Step 2: YouTube Search Fallback
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
		"--default-search", defaultSearch,
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
