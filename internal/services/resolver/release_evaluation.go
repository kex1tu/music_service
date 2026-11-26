package resolver

import (
	"math"
	"slices"
	"sort"
)

// RankReleaseItems separates usable coverage, identity and title-only version
// claims. Platform and alphabet never establish censorship status.
func RankReleaseItems(items []ReleaseCandidateItem, artist, album string, targets []ResolvedTrack, policy VersionPolicy) []ReleaseCandidateItem {
	if !ValidVersionPolicy(policy) {
		policy = PolicyRelease
	}
	for i := range items {
		c := &items[i]
		c.MappingLimited = len(targets) > maxReleaseTargets || len(c.Tracks) > maxReleaseCandidates
		c.Policy = policy
		c.Evaluation = EvaluateTrack(album, artist, 0, c.Title, c.Artist, 0)
		c.TrackMapping, c.TrackEvaluations, c.VersionMatches, c.AmbiguousTracks = evaluateTracklists(targets, c.Tracks, policy)
		c.Tracks = slices.Clone(c.Tracks)
		for j := range c.Tracks {
			c.Tracks[j].DirectURL = releaseTrackURL(c.Tracks[j])
		}
		c.MatchedCount, c.TotalCount = len(c.TrackMapping), len(targets)
		c.MissingTracks = c.TotalCount - c.MatchedCount
		c.ExtraTracks = len(c.Tracks) - c.MatchedCount
		c.MatchedRatio, c.Score, c.MeanTrackScore, c.WeakestTrackScore = 0, 0, 0, 0
		if c.MatchedCount > 0 {
			c.MatchedRatio = float64(c.MatchedCount) / float64(c.TotalCount)
			c.WeakestTrackScore = 100
			sum := 0
			for _, e := range c.TrackEvaluations {
				sum += e.Score
				c.WeakestTrackScore = min(c.WeakestTrackScore, e.Score)
			}
			c.MeanTrackScore = sum / c.MatchedCount
			// Each uncovered position contributes zero track evidence.
			c.Score = int(math.Round(0.70*float64(sum)/float64(c.TotalCount) + 20*c.Evaluation.TitleSimilarity + 10*c.Evaluation.ArtistSimilarity))
			if !c.Evaluation.Compatible {
				c.Score = min(39, c.Score)
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if (a.MatchedCount > 0) != (b.MatchedCount > 0) {
			return a.MatchedCount > 0
		}
		if a.Evaluation.Compatible != b.Evaluation.Compatible {
			return a.Evaluation.Compatible
		}
		if a.MatchedCount != b.MatchedCount {
			return a.MatchedCount > b.MatchedCount
		}
		if a.VersionMatches != b.VersionMatches {
			return a.VersionMatches > b.VersionMatches
		}
		if policy != PolicyRelease {
			pa, pb := a.Evaluation.VersionClaim == string(policy), b.Evaluation.VersionClaim == string(policy)
			if pa != pb {
				return pa
			}
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.MeanTrackScore != b.MeanTrackScore {
			return a.MeanTrackScore > b.MeanTrackScore
		}
		return a.URL < b.URL
	})
	return items
}
