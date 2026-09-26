package resolver

import (
	"math"
	"strings"
)

// CatalogCandidate represents metadata of a track candidate retrieved from an online catalog.
type CatalogCandidate struct {
	Title    string
	Artist   string
	Duration int
}

// ComputeCatalogMatchScore computes the match confidence percentage (0..100) between the target track
// and a candidate retrieved from an online catalog (Yandex Music, Deezer, YouTube Music, iTunes).
//
// Scoring algorithm:
//   - Title similarity: 0-45 points (exact match: 45, substring: 35, word overlap ratio: up to 30)
//   - Artist similarity: 0-35 points (equivalent: 35, primary artist match: 25, word overlap ratio: up to 18)
//   - Duration similarity: -15..+20 points (diff <= 3s: +20, <= 8s: +15, <= 15s: +8, > 60s: -15, missing: +12)
func ComputeCatalogMatchScore(targetTitle, targetArtist string, targetDuration float64, candidate CatalogCandidate) int {
	score := 0

	// 1. Title similarity (0-45)
	tTitle := strings.ToLower(CleanQueryText(targetTitle))
	mTitle := strings.ToLower(CleanQueryText(candidate.Title))
	if tTitle != "" && mTitle != "" {
		if tTitle == mTitle {
			score += 45
		} else if strings.Contains(mTitle, tTitle) || strings.Contains(tTitle, mTitle) {
			score += 35
		} else {
			tWords := strings.Fields(tTitle)
			matchedWords := 0
			for _, w := range tWords {
				if len(w) > 2 && strings.Contains(mTitle, w) {
					matchedWords++
				}
			}
			if len(tWords) > 0 {
				ratio := float64(matchedWords) / float64(len(tWords))
				score += int(ratio * 30.0)
			}
		}
	}

	// 2. Artist similarity (0-35)
	if AreArtistsEquivalent(targetArtist, candidate.Artist) {
		score += 35
	} else if strings.Contains(strings.ToLower(candidate.Artist), strings.ToLower(ExtractPrimaryArtist(targetArtist))) {
		score += 25
	} else {
		tArtWords := strings.Fields(strings.ToLower(CleanQueryText(targetArtist)))
		mArt := strings.ToLower(candidate.Artist)
		matchedArt := 0
		for _, w := range tArtWords {
			if len(w) > 2 && strings.Contains(mArt, w) {
				matchedArt++
			}
		}
		if len(tArtWords) > 0 {
			score += int((float64(matchedArt) / float64(len(tArtWords))) * 18.0)
		}
	}

	// 3. Duration match (0-20, penalty if > 60s diff)
	if targetDuration > 0 && candidate.Duration > 0 {
		diff := math.Abs(targetDuration - float64(candidate.Duration))
		if diff <= 3 {
			score += 20
		} else if diff <= 8 {
			score += 15
		} else if diff <= 15 {
			score += 8
		} else if diff > 60 {
			score -= 15
		}
	} else {
		score += 12
	}

	if score > 100 {
		score = 100
	}
	if score < 0 {
		score = 0
	}
	return score
}
