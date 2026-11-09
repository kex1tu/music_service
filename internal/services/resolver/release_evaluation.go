package resolver

import "sort"

// RankReleaseItems separates usable coverage, identity and title-only version
// claims. Platform and alphabet never establish censorship status.
func RankReleaseItems(items []ReleaseCandidateItem, artist, album string, targets []ResolvedTrack, policy VersionPolicy) []ReleaseCandidateItem {
	if !ValidVersionPolicy(policy) {
		policy = PolicyRelease
	}
	for i := range items {
		c := &items[i]
		c.MappingLimited = len(targets) > 500 || len(c.Tracks) > 1000
		c.Policy = policy
		c.Evaluation = EvaluateTrack(album, artist, 0, c.Title, c.Artist, 0)
		c.TrackEvaluations = map[int]MatchEvaluation{}
		c.MissingTracks = len(targets) - c.MatchedCount
		c.ExtraTracks = max(0, len(c.Tracks)-c.MatchedCount)
		sum := 0
		c.WeakestTrackScore = 100
		for idx, raw := range c.TrackMapping {
			for _, tr := range c.Tracks {
				if candidateKey(releaseTrackURL(tr)) == candidateKey(raw) {
					a := tr.Artist
					if a == "" {
						a = c.Artist
					}
					e := EvaluateTrack(targets[idx].Title, targets[idx].Artist, targets[idx].Duration, tr.Title, a, tr.Duration)
					c.TrackEvaluations[idx] = e
					sum += e.Score
					c.WeakestTrackScore = min(c.WeakestTrackScore, e.Score)
					break
				}
			}
		}
		if len(c.TrackEvaluations) > 0 {
			c.MeanTrackScore = sum / len(c.TrackEvaluations)
		} else {
			c.WeakestTrackScore = 0
		}
		if c.MatchedCount == 0 {
			c.Score = 0
		} else {
			c.Score = int(70*c.MatchedRatio + 20*c.Evaluation.TitleSimilarity + 10*c.Evaluation.ArtistSimilarity)
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
