package resolver

import (
	"math"
	"testing"
)

func TestLevenshteinRatio(t *testing.T) {
	tests := []struct {
		name     string
		s1       string
		s2       string
		wantMin  float64
		wantMax  float64
		exactVal *float64
	}{
		{
			name:     "identical strings",
			s1:       "hello world",
			s2:       "hello world",
			exactVal: floatPtr(1.0),
		},
		{
			name:     "both empty",
			s1:       "",
			s2:       "",
			exactVal: floatPtr(1.0),
		},
		{
			name:     "first empty",
			s1:       "",
			s2:       "something",
			exactVal: floatPtr(0.0),
		},
		{
			name:     "second empty",
			s1:       "something",
			s2:       "",
			exactVal: floatPtr(0.0),
		},
		{
			name:     "completely different",
			s1:       "abc",
			s2:       "xyz",
			exactVal: floatPtr(0.0),
		},
		{
			name:    "apostrophe typo (don't vs dont)",
			s1:      "don't",
			s2:      "dont",
			wantMin: 0.88,
			wantMax: 1.0,
		},
		{
			name:    "fuzzy title variant (Dont Stop Believing vs Don't Stop Believin')",
			s1:      "dont stop believing",
			s2:      "don't stop believin'",
			wantMin: 0.85,
			wantMax: 1.0,
		},
		{
			name:     "cyrillic identical",
			s1:       "людей",
			s2:       "людей",
			exactVal: floatPtr(1.0),
		},
		{
			name:    "cyrillic single typo (похрюкай vs похрюкаи)",
			s1:      "похрюкай",
			s2:      "похрюкаи",
			wantMin: 0.85,
			wantMax: 0.95,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := LevenshteinRatio(tc.s1, tc.s2)
			if tc.exactVal != nil {
				if math.Abs(got-*tc.exactVal) > 1e-6 {
					t.Errorf("LevenshteinRatio(%q, %q) = %f, want exact %f", tc.s1, tc.s2, got, *tc.exactVal)
				}
			} else {
				if got < tc.wantMin || got > tc.wantMax {
					t.Errorf("LevenshteinRatio(%q, %q) = %f, want within [%f, %f]", tc.s1, tc.s2, got, tc.wantMin, tc.wantMax)
				}
			}
		})
	}
}

func TestCalculateScore_Weighted70_30(t *testing.T) {
	m := NewAudioMatcher("")

	// 1. Title matches, channel is a third-party aggregator (Trap Nation / DistroKid)
	scoreMatch := m.CalculateScore(
		"Faded",
		"Alan Walker",
		212,
		"Alan Walker - Faded",
		"Trap Nation",
		212,
	)

	// With 70% weight on title, even if the channel doesn't match the artist name,
	// the title match should grant ~28 points (70% of 40), yielding >= 60 total score with duration.
	if scoreMatch < 60.0 {
		t.Errorf("expected score >= 60.0 for matching title on aggregator channel, got %.1f", scoreMatch)
	}

	// 2. Both title and channel mismatch
	scoreMismatch := m.CalculateScore(
		"Faded",
		"Alan Walker",
		212,
		"Alan Walker - The Spectre",
		"Trap Nation",
		212,
	)

	// Difference between matching title and non-matching title should reflect the 70% title weight (at least 20 points difference)
	scoreDiff := scoreMatch - scoreMismatch
	if scoreDiff < 20.0 {
		t.Errorf("expected score difference >= 20.0 due to 70%% title weight, got %.1f (match=%.1f, mismatch=%.1f)", scoreDiff, scoreMatch, scoreMismatch)
	}

	// 3. Typo resilience test with punctuation difference
	scoreTypo := m.CalculateScore(
		"Don't Stop Believin'",
		"Journey",
		250,
		"Journey - Dont Stop Believing",
		"Journey",
		250,
	)
	if scoreTypo < 70.0 {
		t.Errorf("expected resilient score >= 70.0 for typo/punctuation variant, got %.1f", scoreTypo)
	}
}

func floatPtr(f float64) *float64 {
	return &f
}
