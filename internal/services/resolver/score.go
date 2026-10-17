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

	// 4. Channel bonus (YouTube Music Topic is the official studio track)
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

	// 5. Explicit bonus (strict priority over clean / censored)
	rawTargetLower := strings.ToLower(targetTitle)
	for _, ekw := range explicitKeywords {
		if strings.Contains(rawCandLower, ekw) {
			score += 25.0
			break
		}
	}

	// 6. Bad keywords penalty (censorship & unwanted versions)
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
