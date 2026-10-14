package resolver

import (
	"context"
	"testing"
)

func TestIsAllowedURL(t *testing.T) {
	tests := []struct {
		url     string
		allowed bool
	}{
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", true},
		{"https://music.youtube.com/watch?v=dQw4w9WgXcQ", true},
		{"https://youtu.be/dQw4w9WgXcQ", true},
		{"https://soundcloud.com/artist/track", true},
		{"https://artist.bandcamp.com/track/song", true},
		{"https://bandcamp.com/album/xyz", true},
		{"https://deezer.com/album/123", true},
		{"https://evil.com/malware.mp3", false},
		{"http://localhost:8080/internal", false},
		{"not-a-url", false},
		{"", false},
	}

	for _, tt := range tests {
		got := IsAllowedURL(tt.url)
		if got != tt.allowed {
			t.Errorf("IsAllowedURL(%q) = %v, want %v", tt.url, got, tt.allowed)
		}
	}
}

func TestCalculateCandidateScore(t *testing.T) {
	// 1. Точное совпадение с Topic / YT Music и delta <= 2s
	scoreExact := calculateCandidateScore(
		"Numb",
		"Linkin Park - Topic",
		"Numb",
		"Linkin Park",
		187,
		187,
		true,
		"ytmusic",
	)
	if scoreExact < 90 {
		t.Errorf("expected high score for exact topic match, got %d", scoreExact)
	}

	// 2. Slowed / Reverb штраф
	scoreSlowed := calculateCandidateScore(
		"Numb (Slowed + Reverb)",
		"Random Uploader",
		"Numb",
		"Linkin Park",
		240,
		187,
		false,
		"youtube",
	)
	if scoreSlowed >= 60 {
		t.Errorf("expected penalty for slowed/reverb version, got %d", scoreSlowed)
	}

	// 3. Remix когда в оригинале нет remix
	scoreRemix := calculateCandidateScore(
		"Numb (DJ Remix)",
		"Remixer",
		"Numb",
		"Linkin Park",
		187,
		187,
		false,
		"youtube",
	)
	if scoreRemix >= scoreExact {
		t.Errorf("expected remix score %d to be lower than exact score %d", scoreRemix, scoreExact)
	}

	// 4. Длительность сильно расходится (|Delta| > 30s)
	scoreDurationMismatch := calculateCandidateScore(
		"Numb",
		"Linkin Park",
		"Numb",
		"Linkin Park",
		300,
		187,
		false,
		"youtube",
	)
	if scoreDurationMismatch >= scoreExact {
		t.Errorf("expected duration mismatch to have lower score, got %d", scoreDurationMismatch)
	}
}

func TestSearchMultiCandidates_EmptyQuery(t *testing.T) {
	matcher := NewAudioMatcher("")
	_, err := matcher.SearchMultiCandidates(context.Background(), "", "", 0, "")
	if err == nil {
		t.Error("expected error on empty query, got nil")
	}
}
