package resolver

import (
	"context"
	"testing"
)

func TestUncensoredMatcher_H8Hood_And_Apologiya(t *testing.T) {
	distributors := []string{"distrokid", "believe", "zvonko"}

	t.Run("h8.hood: Rejects unrelated candidate with same duration and accepts valid", func(t *testing.T) {
		validDurations := []int{150}

		candidates := []YtCandidate{
			{
				ID:         "unrelated_phonk",
				Title:      "KSLV Noh - Disaster (Phonk)",
				Uploader:   "Phonk Nation",
				Duration:   150,
				UploadDate: "20230501",
			},
			{
				ID:         "valid_h8hood",
				Title:      "h8.hood - Дым (Official Audio)",
				Uploader:   "h8.hood",
				Duration:   151,
				UploadDate: "20240115",
			},
		}

		ranked := RankCandidates(candidates, "h8.hood", "Дым", validDurations, 150, false, 2, 10, 2023, distributors)
		if len(ranked) == 0 {
			t.Fatalf("expected at least 1 valid candidate, got 0")
		}

		// The top candidate MUST be the valid h8.hood track with status 'likely'
		top := ranked[0]
		if top.Status != "likely" {
			t.Errorf("expected top candidate status 'likely', got %q", top.Status)
		}
		if top.Title != "h8.hood - Дым (Official Audio)" {
			t.Errorf("expected h8.hood track to be selected, got %q", top.Title)
		}

		// Verify unrelated candidate was either dropped or not marked 'likely'
		for _, r := range ranked {
			if r.Title == "KSLV Noh - Disaster (Phonk)" && r.Status == "likely" {
				t.Errorf("unrelated track should NOT have status 'likely'")
			}
		}
	})

	t.Run("апология: Supports transliterated artist and handles savior markers", func(t *testing.T) {
		validDurations := []int{190}

		candidates := []YtCandidate{
			{
				ID:         "foreign_artist",
				Title:      "Some Other Artist - Unrelated Song",
				Uploader:   "Random Uploader",
				Duration:   190,
				UploadDate: "20230101",
			},
			{
				ID:         "apologiya_savior",
				Title:      "апология - Внутри (до цензуры)",
				Uploader:   "апология",
				Duration:   190,
				UploadDate: "20231120",
			},
			{
				ID:         "apologiya_clean",
				Title:      "apologiya - Внутри (Audio)",
				Uploader:   "apologiya - Topic",
				Duration:   191,
				UploadDate: "20231120",
			},
		}

		ranked := RankCandidates(candidates, "апология", "Внутри", validDurations, 190, false, 2, 10, 2023, distributors)
		if len(ranked) < 2 {
			t.Fatalf("expected at least 2 valid candidates, got %d", len(ranked))
		}

		// Savior candidate should rank highest with status 'marked'
		if ranked[0].Status != "marked" {
			t.Errorf("expected rank[0] status 'marked', got %q", ranked[0].Status)
		}
		if ranked[0].Title != "апология - Внутри (до цензуры)" {
			t.Errorf("expected savior track at index 0, got %q", ranked[0].Title)
		}

		// Transliterated topic track should have status 'likely'
		if ranked[1].Status != "likely" {
			t.Errorf("expected rank[1] status 'likely', got %q", ranked[1].Status)
		}

		// Foreign artist must be rejected
		for _, r := range ranked {
			if r.URL == "https://soundcloud.com/foreign_artist" {
				t.Errorf("unrelated candidate should have been completely discarded")
			}
		}
	})

	t.Run("Short track title matching (e.g. 3-4 letters)", func(t *testing.T) {
		match, titleScore, artistScore := CheckTitleAndArtistMatch("h8.hood - Шок (Snippet Audio)", "h8.hood", "Шок", "h8.hood")
		if !match {
			t.Errorf("expected match=true for short title 'Шок', got false (titleScore=%.2f, artistScore=%.2f)", titleScore, artistScore)
		}
	})
}

func TestScoreDurationShield_TotalMismatch(t *testing.T) {
	matcher := NewAudioMatcher("")
	// Unrelated track that happens to have the exact same duration (diff = 0)
	score := matcher.CalculateScore("Track A", "Artist A", 180, "Completely Different Song", "Other Channel", 180)
	if score >= 25.0 {
		t.Errorf("expected score < 25.0 for total text mismatch with same duration, got %.1f", score)
	}
}

func TestFindYouTubeMatchWithScore_RejectsUnrelatedSameDuration(t *testing.T) {
	matcher := NewAudioMatcher("")
	matcher.SetSearchFn(func(ctx context.Context, query string) ([]YtCandidate, error) {
		return []YtCandidate{
			{
				ID:       "dummy_unrelated",
				Title:    "Another Band - Something Else",
				Uploader: "Unrelated VEVO",
				Duration: 180,
			},
		}, nil
	})

	_, _, err := matcher.FindYouTubeMatchWithScore(context.Background(), "Дым", "h8.hood", 180)
	if err == nil {
		t.Errorf("expected error because candidate is completely unrelated despite matching duration")
	}
}
