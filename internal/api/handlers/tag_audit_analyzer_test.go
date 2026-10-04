package handlers

import (
	"testing"
)

func TestAnalyzeTrackIssues_CensoredDetection(t *testing.T) {
	tests := []struct {
		title         string
		artist        string
		expectedIssue bool
	}{
		{"Song Title", "Artist", false},
		{"Song Title (Clean)", "Artist", true},
		{"Song Title - Radio Edit", "Artist", true},
		{"Song Title Censored Version", "Artist", true},
		{"Song (с цензурой)", "Артист", true},
		{"Original Song", "Artist", false},
		{"Rather Be", "Clean Bandit", false}, // Название не содержит маркеров, артист 'Clean Bandit' не триггерит дефект
		{"Song Uncensored", "Artist", false}, // Маркер-спаситель не детектируется как плохой
		{"Song Uncut", "Artist", false},      // Маркер-спаситель не детектируется как плохой
	}

	for _, tc := range tests {
		item := analyzeTrackIssues(1, "/test.mp3", tc.title, tc.artist, "Album", 2023, 1, 180.0)
		if tc.expectedIssue {
			if item == nil {
				t.Fatalf("Expected censored_version issue for '%s', got nil", tc.title)
			}
			hasCensored := false
			for _, iss := range item.Issues {
				if iss == "censored_version" {
					hasCensored = true
					break
				}
			}
			if !hasCensored {
				t.Errorf("Expected censored_version issue for '%s', got %v", tc.title, item.Issues)
			}
			if item.Severity != "review_needed" {
				t.Errorf("Expected severity 'review_needed' for '%s', got '%s'", tc.title, item.Severity)
			}
		} else {
			if item != nil {
				for _, iss := range item.Issues {
					if iss == "censored_version" {
						t.Errorf("Unexpected censored_version issue for '%s'", tc.title)
					}
				}
			}
		}
	}
}

