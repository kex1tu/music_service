package resolver

import "testing"

func TestComputeCatalogMatchScore_ExactMatch(t *testing.T) {
	score := ComputeCatalogMatchScore(
		"Shape of You",
		"Ed Sheeran",
		233.0,
		CatalogCandidate{
			Title:    "Shape of You",
			Artist:   "Ed Sheeran",
			Duration: 233,
		},
	)
	if score < 95 {
		t.Errorf("expected score >= 95 for exact match, got %d", score)
	}
}

func TestComputeCatalogMatchScore_PartialMatch(t *testing.T) {
	score := ComputeCatalogMatchScore(
		"Shape of You",
		"Ed Sheeran",
		233.0,
		CatalogCandidate{
			Title:    "Shape of You Live Concert Edition",
			Artist:   "Ed Sheeran",
			Duration: 230,
		},
	)
	if score < 70 || score > 95 {
		t.Errorf("expected score in range [70, 95] for partial match, got %d", score)
	}
}

func TestComputeCatalogMatchScore_NoMatch(t *testing.T) {
	score := ComputeCatalogMatchScore(
		"Shape of You",
		"Ed Sheeran",
		233.0,
		CatalogCandidate{
			Title:    "Despacito",
			Artist:   "Luis Fonsi",
			Duration: 280,
		},
	)
	if score > 20 {
		t.Errorf("expected score <= 20 for unrelated track, got %d", score)
	}
}

func TestComputeCatalogMatchScore_DurationPenalty(t *testing.T) {
	scoreNormal := ComputeCatalogMatchScore(
		"Long Song",
		"Some Artist",
		300.0,
		CatalogCandidate{
			Title:    "Long Song",
			Artist:   "Some Artist",
			Duration: 300,
		},
	)

	scoreWithDiff := ComputeCatalogMatchScore(
		"Long Song",
		"Some Artist",
		300.0,
		CatalogCandidate{
			Title:    "Long Song",
			Artist:   "Some Artist",
			Duration: 180, // 120s difference > 60s
		},
	)

	if scoreNormal <= scoreWithDiff {
		t.Errorf("expected scoreNormal (%d) to be greater than scoreWithDiff (%d)", scoreNormal, scoreWithDiff)
	}
}

func TestComputeCatalogMatchScore_Ranking(t *testing.T) {
	targetTitle := "Starboy"
	targetArtist := "The Weeknd"
	targetDuration := 230.0

	exactCand := CatalogCandidate{
		Title:    "Starboy",
		Artist:   "The Weeknd",
		Duration: 230,
	}
	deltaCand := CatalogCandidate{
		Title:    "Starboy",
		Artist:   "The Weeknd",
		Duration: 215,
	}
	remixCand := CatalogCandidate{
		Title:    "Starboy Remix",
		Artist:   "The Weeknd",
		Duration: 240,
	}
	unrelatedCand := CatalogCandidate{
		Title:    "Blinding Lights",
		Artist:   "The Weeknd",
		Duration: 200,
	}

	scoreExact := ComputeCatalogMatchScore(targetTitle, targetArtist, targetDuration, exactCand)
	scoreDelta := ComputeCatalogMatchScore(targetTitle, targetArtist, targetDuration, deltaCand)
	scoreRemix := ComputeCatalogMatchScore(targetTitle, targetArtist, targetDuration, remixCand)
	scoreUnrelated := ComputeCatalogMatchScore(targetTitle, targetArtist, targetDuration, unrelatedCand)

	if !(scoreExact > scoreDelta && scoreDelta > scoreRemix && scoreRemix > scoreUnrelated) {
		t.Errorf("expected ranking scoreExact(%d) > scoreDelta(%d) > scoreRemix(%d) > scoreUnrelated(%d)",
			scoreExact, scoreDelta, scoreRemix, scoreUnrelated)
	}
}

