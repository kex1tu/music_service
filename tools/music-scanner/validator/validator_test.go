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
	// 1. Clean English canonical genre
	res := AnalyzeAndNormalizeGenre("Hip-Hop")
	if res.HasCyrillic || res.IsNonCanonical || res.CanonicalGenre != "Hip-Hop" {
		t.Fatalf("expected clean English genre Hip-Hop, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Rock")
	if res.HasCyrillic || res.IsNonCanonical || res.CanonicalGenre != "Rock" {
		t.Fatalf("expected clean English genre Rock, got %+v", res)
	}

	// 2. Rock collapse rules (any rock collapses to Rock)
	res = AnalyzeAndNormalizeGenre("Alternative \\ Rock")
	if res.HasCyrillic || !res.IsNonCanonical || res.CanonicalGenre != "Rock" {
		t.Fatalf("expected Alternative \\ Rock to collapse to Rock, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Alternative \\ Indie Rock \\ Rock")
	if !res.IsNonCanonical || res.CanonicalGenre != "Rock" {
		t.Fatalf("expected Alternative \\ Indie Rock \\ Rock to collapse to Rock, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Pop-Rock")
	if !res.IsNonCanonical || res.CanonicalGenre != "Rock" {
		t.Fatalf("expected Pop-Rock to collapse to Rock, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Поп\\Рок")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Rock" {
		t.Fatalf("expected Поп\\Рок to collapse to Rock, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Рок / Поп")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Rock" {
		t.Fatalf("expected Рок / Поп to collapse to Rock, got %+v", res)
	}

	// 3. Hip-Hop / Pop / Rap collapse rules (pop/hip/rap collapses to Hip-Hop)
	res = AnalyzeAndNormalizeGenre("Русский рэп")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Hip-Hop" {
		t.Fatalf("expected Русский рэп to collapse to Hip-Hop, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Поп")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Hip-Hop" {
		t.Fatalf("expected Поп to collapse to Hip-Hop, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Pop")
	if res.HasCyrillic || !res.IsNonCanonical || res.CanonicalGenre != "Hip-Hop" {
		t.Fatalf("expected Pop to collapse to Hip-Hop, got %+v", res)
	}

	// 4. Other genres mapping and space stripping
	res = AnalyzeAndNormalizeGenre("Альтернативная музыка")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Alternative" {
		t.Fatalf("expected Alternative, got %+v", res)
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
