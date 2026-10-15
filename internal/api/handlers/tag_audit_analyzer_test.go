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

func TestAnalyzeTrackIssues_MissingTPE2(t *testing.T) {
	// 1. Missing AlbumArtist (empty string passed as variadic)
	itemMissing := analyzeTrackIssues(1, "/test.mp3", "Valid Title", "Main Artist", "Valid Album", 2024, 1, 200.0, "")
	if itemMissing == nil {
		t.Fatal("expected item to have issues when album_artist is empty")
	}

	foundMissing := false
	for _, iss := range itemMissing.Issues {
		if iss == "missing_album_artist" {
			foundMissing = true
			break
		}
	}
	if !foundMissing {
		t.Errorf("expected missing_album_artist issue, got %v", itemMissing.Issues)
	}
	if itemMissing.Severity != "safe_autofix" {
		t.Errorf("expected safe_autofix severity, got %s", itemMissing.Severity)
	}
	if itemMissing.Proposed.AlbumArtist != "Main Artist" {
		t.Errorf("expected proposed album_artist 'Main Artist', got %q", itemMissing.Proposed.AlbumArtist)
	}

	// 2. Present AlbumArtist
	itemPresent := analyzeTrackIssues(2, "/test2.mp3", "Valid Title", "Main Artist", "Valid Album", 2024, 1, 200.0, "Main Artist")
	if itemPresent != nil {
		for _, iss := range itemPresent.Issues {
			if iss == "missing_album_artist" {
				t.Errorf("did not expect missing_album_artist when album_artist is set")
			}
		}
	}
}

