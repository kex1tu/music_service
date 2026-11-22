package resolver

import (
	"fmt"
	"math"
	"net/url"
	"strings"
)

func releaseTrackURL(c AlbumTrackCandidate) string {
	raw := c.DirectURL
	if raw == "" && c.VideoID != "" {
		if strings.HasPrefix(c.VideoID, "https://") || strings.HasPrefix(c.VideoID, "http://") {
			raw = c.VideoID
		} else {
			raw = fmt.Sprintf("https://music.youtube.com/watch?v=%s", url.QueryEscape(c.VideoID))
		}
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	source, err := (&SourceResolver{}).DetectSource(raw)
	if err != nil {
		return ""
	}
	switch source {
	case "youtube":
		if u.Query().Get("v") != "" || (strings.EqualFold(u.Hostname(), "youtu.be") && strings.Trim(u.Path, "/") != "") {
			return raw
		}
	case "soundcloud", "bandcamp":
		return raw
	}
	return ""
}

const maxReleaseTargets, maxReleaseCandidates = 500, 1000

func assignTracklists(targets []ResolvedTrack, candidates []AlbumTrackCandidate) (map[int]string, int) {
	mapping, _, _, _ := evaluateTracklists(targets, candidates, PolicyRelease)
	return mapping, len(mapping)
}

// evaluateTracklists retains the evidence used to assign each URL. Missing
// credits or duration are suitable for browsing, but not for batch assignment.
func evaluateTracklists(targets []ResolvedTrack, candidates []AlbumTrackCandidate, policy VersionPolicy) (map[int]string, map[int]MatchEvaluation, int, int) {
	out := map[int]string{}
	evaluations := map[int]MatchEvaluation{}
	if len(targets) == 0 || len(candidates) == 0 || len(targets) > maxReleaseTargets || len(candidates) > maxReleaseCandidates {
		return out, evaluations, 0, 0
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
	if len(unique) == 0 {
		return out, evaluations, 0, 0
	}
	n, m := len(targets), len(unique)+len(targets)
	weights := make([][]float64, n)
	eligible := make([]map[int]MatchEvaluation, n)
	// In the global optimum, maximize eligible coverage first, then the number
	// of requested versions, then metadata score. Even all lower-priority
	// weights combined cannot outweigh one higher-priority unit.
	versionWeight := n*100 + 1
	matchWeight := (n + 1) * versionWeight
	desired := make([]string, n)
	for i, t := range targets {
		desired[i] = desiredReleaseTrackClaim(t, policy)
		weights[i] = make([]float64, m)
		eligible[i] = make(map[int]MatchEvaluation)
		targetArtist := artistSep.Split(t.Artist, -1)[0]
		for j, c := range unique {
			candidateArtist := artistSep.Split(CleanChannelName(c.Artist), -1)[0]
			if !AreArtistsEquivalent(targetArtist, candidateArtist) {
				continue
			}
			e := EvaluateTrack(t.Title, t.Artist, t.Duration, c.Title, c.Artist, c.Duration)
			if !e.strongIdentity() {
				continue
			}
			if (c.VersionClaim == "clean" || c.VersionClaim == "uncensored") && e.VersionClaim != c.VersionClaim {
				if e.VersionClaim != "unknown" {
					continue
				}
				e.VersionClaim = c.VersionClaim
			}
			weight := matchWeight + e.Score
			if desired[i] != "unknown" && e.VersionClaim != "unknown" {
				if e.VersionClaim != desired[i] {
					continue
				}
				weight += versionWeight
			}
			eligible[i][j] = e
			weights[i][j] = float64(weight)
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
	preferred, ambiguous := 0, 0
	for j := 1; j <= len(unique); j++ {
		if p[j] > 0 && weights[p[j]-1][j-1] > 0 {
			idx := p[j] - 1
			e := eligible[idx][j-1]
			evaluations[idx] = e
			out[idx] = releaseTrackURL(unique[j-1])
			if desired[idx] != "unknown" && e.VersionClaim == desired[idx] {
				preferred++
			}
			if ambiguousReleasePair(weights, idx, j-1, len(unique)) {
				ambiguous++
			}
		}
	}
	return out, evaluations, preferred, ambiguous
}

// Check both directions: a URL can be equally suitable for multiple target
// positions even when each of those positions has only one eligible source.
func ambiguousReleasePair(weights [][]float64, target, candidate, candidateCount int) bool {
	chosen := weights[target][candidate]
	for other, weight := range weights[target][:candidateCount] {
		if other != candidate && weight > 0 && math.Abs(weight-chosen) <= 2 {
			return true
		}
	}
	for other, row := range weights {
		if other != target && row[candidate] > 0 && math.Abs(row[candidate]-chosen) <= 2 {
			return true
		}
	}
	return false
}

func desiredReleaseTrackClaim(track ResolvedTrack, policy VersionPolicy) string {
	if policy == PolicyRelease && (track.VersionClaim == "uncensored" || track.VersionClaim == "clean") {
		return track.VersionClaim
	}
	return desiredVersionClaim(track.Title, policy)
}
