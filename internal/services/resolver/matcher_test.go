package resolver

import (
	"testing"
)

func TestCleanQueryText(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Hello (Official Video)", "Hello"},
		{"Track [feat. Artist]", "Track"},
		{"Song {Audio}", "Song"},
		{"Clean   Text!", "Clean Text"},
	}

	for _, tt := range tests {
		got := CleanQueryText(tt.input)
		if got != tt.want {
			t.Errorf("CleanQueryText(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCalculateScore(t *testing.T) {
	m := NewAudioMatcher("")

	// Exact match
	score := m.CalculateScore(
		"Never Gonna Give You Up",
		"Rick Astley",
		213,
		"Rick Astley - Never Gonna Give You Up (Official Music Video)",
		"Rick Astley",
		213,
	)
	if score < 70.0 {
		t.Errorf("expected high score for exact match, got %.1f", score)
	}

	// Topic channel
	topicScore := m.CalculateScore(
		"Похрюкай",
		"МЭЙБИ БЭЙБИ",
		145,
		"Похрюкай",
		"МЭЙБИ БЭЙБИ - Topic",
		145,
	)
	if topicScore < 80.0 {
		t.Errorf("expected high score for Topic channel, got %.1f", topicScore)
	}

	// Live / Bad keyword penalty
	badScore := m.CalculateScore(
		"Song",
		"Artist",
		200,
		"Song (Live at Wembley)",
		"Artist",
		200,
	)
	goodScore := m.CalculateScore(
		"Song",
		"Artist",
		200,
		"Song",
		"Artist",
		200,
	)
	if badScore >= goodScore {
		t.Errorf("bad candidate score (%.1f) should be lower than good candidate score (%.1f)", badScore, goodScore)
	}
}
