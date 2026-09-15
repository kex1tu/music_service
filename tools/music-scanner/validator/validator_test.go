package validator

import (
	"testing"
)

func TestValidateTrackPath(t *testing.T) {
	// 1. Valid path
	isValid, suggested, reason := ValidateTrackPath("Bandana I/01. Intro.mp3", "Bandana I", 1, "Intro")
	if !isValid {
		t.Fatalf("expected valid, got invalid: %s, suggested: %s", reason, suggested)
	}

	// 2. Invalid folder
	isValid, suggested, reason = ValidateTrackPath("Big Baby Tape/Bandana I/01. Intro.mp3", "Bandana I", 1, "Intro")
	if isValid {
		t.Fatalf("expected invalid for subfolder path, got valid")
	}
	if suggested != "Bandana I/01. Intro.mp3" {
		t.Fatalf("expected suggested 'Bandana I/01. Intro.mp3', got '%s'", suggested)
	}

	// 3. Invalid track number formatting
	isValid, suggested, _ = ValidateTrackPath("Bandana I/1. Intro.mp3", "Bandana I", 1, "Intro")
	if isValid {
		t.Fatalf("expected invalid for single-digit track number")
	}
	if suggested != "Bandana I/01. Intro.mp3" {
		t.Fatalf("expected suggested 'Bandana I/01. Intro.mp3', got '%s'", suggested)
	}
}

func TestAnalyzeAndNormalizeGenre(t *testing.T) {
	// 1. English genre
	res := AnalyzeAndNormalizeGenre("Hip-Hop")
	if res.HasCyrillic || res.CanonicalGenre != "Hip-Hop" {
		t.Fatalf("expected clean English genre, got %+v", res)
	}

	// 2. Russian genre auto-fixable
	res = AnalyzeAndNormalizeGenre("Русский рэп")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Russian Hip-Hop" {
		t.Fatalf("expected Russian Hip-Hop, got %+v", res)
	}

	// 3. Multi-genre with Russian
	res = AnalyzeAndNormalizeGenre("Рок / Поп")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Rock \\\\ Pop" {
		t.Fatalf("expected Rock \\\\ Pop, got %+v", res)
	}

	// 4. Specific new mappings requested
	res = AnalyzeAndNormalizeGenre("Поп\\Рок")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Pop \\Rock\\Pop-Rock" {
		t.Fatalf("expected Pop \\Rock\\Pop-Rock, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Альтернативная музыка")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Alternative" {
		t.Fatalf("expected Alternative, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Поп")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Pop" {
		t.Fatalf("expected Pop, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Танцевальная музыка")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Dance Music" {
		t.Fatalf("expected Dance Music, got %+v", res)
	}

	// 5. Unknown Russian genre
	res = AnalyzeAndNormalizeGenre("Кастомный неизвестный жанр")
	if !res.HasCyrillic || res.IsAutoFixable {
		t.Fatalf("expected unfixable custom Cyrillic genre, got %+v", res)
	}
}
