package resolver

import (
	"math"
	"strings"
)

var badKeywords = []string{
	"live", "concert", "cover", "karaoke", "remix", "instrumental",
	"acoustic", "tutorial", "reaction", "speed up", "slowed",
	"nightcore", "bass boosted", "8d", "clean", "radio edit",
	"censored", "clean version", "tv edit", "censored version", "radio version",
	"караоке", "кавер", "минус", "минусовка", "концерт", "инструментал", "инструментальная", "реакция", "трибьют",
}

var explicitKeywords = []string{
	"explicit", "explicit version", "uncensored", "dirty", "parental advisory", "uncut",
}

// LevenshteinRatio вычисляет нормализованную схожесть двух строк от 0.0 до 1.0.
// Соответствует алгоритму Levenshtein ratio (индел-расстояние с весом замены 2):
// ratio = (len1 + len2 - dist) / (len1 + len2).
func LevenshteinRatio(s1, s2 string) float64 {
	r1, r2 := []rune(s1), []rune(s2)
	n, m := len(r1), len(r2)
	if n == 0 && m == 0 {
		return 1.0
	}
	if n == 0 || m == 0 {
		return 0.0
	}

	if n < m {
		r1, r2 = r2, r1
		n, m = m, n
	}

	dp := make([]int, m+1)
	for j := 0; j <= m; j++ {
		dp[j] = j
	}

	for i := 1; i <= n; i++ {
		prev := dp[0]
		dp[0] = i
		for j := 1; j <= m; j++ {
			temp := dp[j]
			cost := 0
			if r1[i-1] != r2[j-1] {
				cost = 2
			}
			ins := dp[j-1] + 1
			del := dp[j] + 1
			sub := prev + cost

			minVal := ins
			if del < minVal {
				minVal = del
			}
			if sub < minVal {
				minVal = sub
			}
			prev = temp
			dp[j] = minVal
		}
	}

	dist := dp[m]
	totalLen := n + m
	if dist >= totalLen {
		return 0.0
	}
	return float64(totalLen-dist) / float64(totalLen)
}

// CalculateScore computes a confidence score (0..100) for a candidate match.
func (m *AudioMatcher) CalculateScore(targetTitle, targetArtist string, targetDuration int, candidateTitle, candidateChannel string, candidateDuration int) float64 {
	score := 0.0

	tTitle := strings.ToLower(CleanQueryText(targetTitle))
	cTitle := strings.ToLower(CleanQueryText(candidateTitle))
	tArt := strings.ToLower(CleanQueryText(targetArtist))
	cChan := strings.ToLower(CleanQueryText(candidateChannel))

	// 1. Duration similarity (up to 35 points)
	if targetDuration > 0 && candidateDuration > 0 {
		diff := math.Abs(float64(targetDuration - candidateDuration))
		if diff <= 2 {
			score += 35.0
		} else if diff <= 5 {
			score += 30.0
		} else if diff <= 10 {
			score += 20.0
		} else if diff <= 20 {
			score += 10.0
		} else if diff > 60 {
			score -= 30.0
		}
	}

	// 2. Fuzzy Text Matching: 70% Title / 30% Channel/Artist (up to 40 points)
	trackRatio := 0.0
	if tTitle != "" && cTitle != "" {
		trackRatio = LevenshteinRatio(cTitle, tTitle)

		// Check if candidate title is in "Artist - Title" format
		if strings.Contains(cTitle, " - ") {
			parts := strings.SplitN(cTitle, " - ", 2)
			if len(parts) == 2 {
				partTit := strings.TrimSpace(parts[1])
				if r := LevenshteinRatio(partTit, tTitle); r > trackRatio {
					trackRatio = r
				}
			}
		}

		if strings.Contains(cTitle, tTitle) || strings.Contains(tTitle, cTitle) {
			if trackRatio < 0.95 {
				trackRatio = 0.95
			}
		}
	}

	artistRatio := 0.0
	if tArt != "" {
		if cChan != "" {
			artistRatio = LevenshteinRatio(cChan, tArt)
		}
		if strings.Contains(cTitle, tArt) {
			if artistRatio < 0.90 {
				artistRatio = 0.90
			}
		} else if strings.Contains(cTitle, " - ") {
			parts := strings.SplitN(cTitle, " - ", 2)
			if len(parts) == 2 {
				partArt := strings.TrimSpace(parts[0])
				if r := LevenshteinRatio(partArt, tArt); r > artistRatio {
					artistRatio = r
				}
			}
		}

		// Check transliterated variants if direct match is weak
		if artistRatio < 0.70 {
			for _, variant := range TransliterateArtist(tArt) {
				vLower := strings.ToLower(variant)
				if cChan != "" {
					if r := LevenshteinRatio(cChan, vLower); r > artistRatio {
						artistRatio = r
					}
				}
				if strings.Contains(cTitle, vLower) {
					if artistRatio < 0.85 {
						artistRatio = 0.85
					}
				}
				if strings.Contains(cTitle, " - ") {
					parts := strings.SplitN(cTitle, " - ", 2)
					if len(parts) == 2 {
						partArt := strings.TrimSpace(parts[0])
						if r := LevenshteinRatio(partArt, vLower); r > artistRatio {
							artistRatio = r
						}
					}
				}
			}
		}
	}

	// 70% weight for title, 30% weight for artist/channel
	fuzzyScore := (trackRatio*0.70 + artistRatio*0.30) * 40.0
	score += fuzzyScore

	// 3. Channel bonus (YouTube Music Topic is the official studio track)
	candLower := strings.ToLower(candidateChannel)
	if strings.HasSuffix(candLower, " - topic") || strings.HasSuffix(candLower, "- topic") {
		score += 25.0 // Official YouTube Music track (uncut studio master)
	} else if strings.Contains(candLower, "vevo") || strings.Contains(candLower, "official") || (tArt != "" && strings.Contains(candLower, tArt)) {
		score += 15.0
	}

	rawCandLower := strings.ToLower(candidateTitle)
	if strings.Contains(rawCandLower, "official music video") || strings.Contains(rawCandLower, "official audio") || strings.Contains(rawCandLower, "official video") {
		score += 10.0
	}

	// 4. Explicit bonus (strict priority over clean / censored)
	rawTargetLower := strings.ToLower(targetTitle)
	for _, ekw := range explicitKeywords {
		if strings.Contains(rawCandLower, ekw) {
			score += 25.0
			break
		}
	}

	// 5. Bad keywords penalty (censorship & unwanted versions)
	for _, kw := range badKeywords {
		if (strings.Contains(rawCandLower, kw) || strings.Contains(candLower, kw)) && !strings.Contains(rawTargetLower, kw) {
			score -= 40.0
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
