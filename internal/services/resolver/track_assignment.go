package resolver

import (
	"fmt"
	"math"
	"strings"
)

func releaseTrackURL(c AlbumTrackCandidate) string {
	if c.DirectURL != "" {
		if IsAllowedURL(c.DirectURL) {
			return c.DirectURL
		}
		return ""
	}
	if strings.HasPrefix(c.VideoID, "https://") || strings.HasPrefix(c.VideoID, "http://") {
		if IsAllowedURL(c.VideoID) {
			return c.VideoID
		}
		return ""
	}
	if c.VideoID != "" {
		return fmt.Sprintf("https://music.youtube.com/watch?v=%s", c.VideoID)
	}
	return ""
}

// assignTracklists uses maximum-weight bipartite assignment with a private dummy
// column for every target. No track is forced to match, or used twice by URL.
func assignTracklists(targets []ResolvedTrack, candidates []AlbumTrackCandidate) (map[int]string, int) {
	out := map[int]string{}
	// The cubic assignment is intended for releases, not unbounded libraries.
	if len(targets) > 500 || len(candidates) > 1000 {
		return out, 0
	}
	if len(targets) == 0 {
		return out, 0
	}
	unique := []AlbumTrackCandidate{}
	seen := map[string]bool{}
	for _, c := range candidates {
		u := releaseTrackURL(c)
		if u == "" || seen[candidateKey(u)] {
			continue
		}
		seen[candidateKey(u)] = true
		unique = append(unique, c)
	}
	n, m := len(targets), len(unique)+len(targets)
	weights := make([][]float64, n)
	for i, t := range targets {
		weights[i] = make([]float64, m)
		for j, c := range unique {
			e := EvaluateTrack(t.Title, t.Artist, t.Duration, c.Title, c.Artist, c.Duration)
			if e.Compatible && e.TitleSimilarity >= 0.65 {
				weights[i][j] = float64(e.Score)
			}
		}
	}
	// Rectangular Hungarian minimization, costs are negative match ratings.
	u, v := make([]float64, n+1), make([]float64, m+1)
	p, way := make([]int, m+1), make([]int, m+1)
	for i := 1; i <= n; i++ {
		p[0] = i
		j0 := 0
		minv := make([]float64, m+1)
		used := make([]bool, m+1)
		for j := 1; j <= m; j++ {
			minv[j] = math.Inf(1)
		}
		for {
			used[j0] = true
			i0 := p[j0]
			delta := math.Inf(1)
			j1 := 0
			for j := 1; j <= m; j++ {
				if !used[j] {
					cur := -weights[i0-1][j-1] - u[i0] - v[j]
					if cur < minv[j] {
						minv[j] = cur
						way[j] = j0
					}
					if minv[j] < delta {
						delta = minv[j]
						j1 = j
					}
				}
			}
			for j := 0; j <= m; j++ {
				if used[j] {
					u[p[j]] += delta
					v[j] -= delta
				} else {
					minv[j] -= delta
				}
			}
			j0 = j1
			if p[j0] == 0 {
				break
			}
		}
		for {
			j1 := way[j0]
			p[j0] = p[j1]
			j0 = j1
			if j0 == 0 {
				break
			}
		}
	}
	for j := 1; j <= len(unique); j++ {
		if p[j] > 0 && weights[p[j]-1][j-1] > 0 {
			out[p[j]-1] = releaseTrackURL(unique[j-1])
		}
	}
	return out, len(out)
}
