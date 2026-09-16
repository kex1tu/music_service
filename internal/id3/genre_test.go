package id3

import (
	"testing"
)

func TestAnalyzeAndNormalizeGenre(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantHasCyr     bool
		wantNonCanon   bool
		wantAutoFix    bool
		wantCanonical  string
	}{
		{
			name:          "Russian rap",
			input:         "русский рэп",
			wantHasCyr:    true,
			wantNonCanon:  true,
			wantAutoFix:   true,
			wantCanonical: "Russian Hip-Hop",
		},
		{
			name:          "Russian rock",
			input:         "русский рок",
			wantHasCyr:    true,
			wantNonCanon:  true,
			wantAutoFix:   true,
			wantCanonical: "Russian Rock",
		},
		{
			name:          "Multiple Russian genres with slash",
			input:         "поп / рок",
			wantHasCyr:    true,
			wantNonCanon:  true,
			wantAutoFix:   true,
			wantCanonical: "Pop\\\\Rock",
		},
		{
			name:          "Canonical English Hip-Hop",
			input:         "Hip-Hop",
			wantHasCyr:    false,
			wantNonCanon:  false,
			wantAutoFix:   false,
			wantCanonical: "Hip-Hop",
		},
		{
			name:          "Lower english genre",
			input:         "rock",
			wantHasCyr:    false,
			wantNonCanon:  true,
			wantAutoFix:   true,
			wantCanonical: "Rock",
		},
		{
			name:          "Empty string",
			input:         "",
			wantHasCyr:    false,
			wantNonCanon:  false,
			wantAutoFix:   false,
			wantCanonical: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AnalyzeAndNormalizeGenre(tt.input)
			if got.HasCyrillic != tt.wantHasCyr {
				t.Errorf("HasCyrillic = %v, want %v", got.HasCyrillic, tt.wantHasCyr)
			}
			if got.IsNonCanonical != tt.wantNonCanon {
				t.Errorf("IsNonCanonical = %v, want %v", got.IsNonCanonical, tt.wantNonCanon)
			}
			if got.IsAutoFixable != tt.wantAutoFix {
				t.Errorf("IsAutoFixable = %v, want %v", got.IsAutoFixable, tt.wantAutoFix)
			}
			if got.CanonicalGenre != tt.wantCanonical {
				t.Errorf("CanonicalGenre = %q, want %q", got.CanonicalGenre, tt.wantCanonical)
			}
		})
	}
}
