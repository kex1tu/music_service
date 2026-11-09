package resolver

import ()

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

// CalculateScore returns an uncalibrated metadata match rating, never an authorization.
func (m *AudioMatcher) CalculateScore(targetTitle, targetArtist string, targetDuration int, candidateTitle, candidateChannel string, candidateDuration int) float64 {
	return float64(EvaluateTrack(targetTitle, targetArtist, targetDuration, candidateTitle, candidateChannel, candidateDuration, true).Score)
}
