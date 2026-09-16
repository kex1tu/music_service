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

	// Max Korzh transliteration + Unicode NFD match test
	korzhScore := m.CalculateScore(
		"2 типа людей",
		"Макс Корж",
		255,
		"Макс Корж - 2 типа людеи\u0306 (Official audio)",
		"Max Korzh",
		253,
	)
	if korzhScore < 65.0 {
		t.Errorf("expected score >= 65.0 for Max Korzh with NFD and transliteration, got %.1f", korzhScore)
	}

	// SoundCloud exact match
	scScore := m.CalculateScore(
		"2 типа людей",
		"Макс Корж",
		255,
		"2 типа людей",
		"Макс Корж",
		255,
	)
	if scScore < 85.0 {
		t.Errorf("expected high score for exact SoundCloud match, got %.1f", scScore)
	}
}

func TestNormalizeUnicodeText(t *testing.T) {
	nfd := "людеи\u0306"
	got := CleanQueryText(nfd)
	want := "людей"
	if got != want {
		t.Errorf("CleanQueryText(%q) = %q, want %q", nfd, got, want)
	}
}

func TestTransliterateArtist(t *testing.T) {
	variants := TransliterateArtist("макс корж")
	found := false
	for _, v := range variants {
		if v == "max korzh" || v == "maks korzh" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected max korzh in transliterated variants, got %v", variants)
	}
}

func TestCleanQueryTextFull(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Малолетка (Розочка Remix)", "Малолетка Розочка Remix"},
		{"Song [feat. Artist]", "Song feat Artist"},
		{"Track (Official Audio)", "Track Official Audio"},
	}

	for _, tt := range tests {
		got := CleanQueryTextFull(tt.input)
		if got != tt.want {
			t.Errorf("CleanQueryTextFull(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCalculateScore_RemixMatch(t *testing.T) {
	m := NewAudioMatcher("")

	// Remix target should match remix candidate without penalty
	remixScore := m.CalculateScore(
		"Малолетка (Розочка Remix)",
		"Макс Корж",
		196,
		"Макс Корж - Малолетка (Розочка Remix)",
		"Remix Sound",
		196,
	)
	if remixScore < 70.0 {
		t.Errorf("expected score >= 70.0 for remix match, got %.1f", remixScore)
	}
}
