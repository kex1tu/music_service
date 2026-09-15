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
	// 1. Clean English canonical genres
	res := AnalyzeAndNormalizeGenre("Hip-Hop")
	if res.HasCyrillic || res.IsNonCanonical || res.CanonicalGenre != "Hip-Hop" {
		t.Fatalf("expected clean English genre Hip-Hop, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Rock")
	if res.HasCyrillic || res.IsNonCanonical || res.CanonicalGenre != "Rock" {
		t.Fatalf("expected clean English genre Rock, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Rap")
	if res.HasCyrillic || res.IsNonCanonical || res.CanonicalGenre != "Rap" {
		t.Fatalf("expected clean English genre Rap, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Pop")
	if res.HasCyrillic || res.IsNonCanonical || res.CanonicalGenre != "Pop" {
		t.Fatalf("expected clean English genre Pop, got %+v", res)
	}

	// 2. Multi-genres separated by \\ must NOT collapse
	res = AnalyzeAndNormalizeGenre("Rap\\\\Hip-Hop")
	if res.HasCyrillic || res.IsNonCanonical || res.CanonicalGenre != "Rap\\\\Hip-Hop" {
		t.Fatalf("expected Rap\\\\Hip-Hop to remain Rap\\\\Hip-Hop, got %+v", res)
	}

	// 3. Rap\\Hip hop must normalize to Rap\\Hip-Hop
	res = AnalyzeAndNormalizeGenre("Rap\\\\Hip hop")
	if res.HasCyrillic || !res.IsNonCanonical || !res.IsAutoFixable || res.CanonicalGenre != "Rap\\\\Hip-Hop" {
		t.Fatalf("expected Rap\\\\Hip hop to normalize to Rap\\\\Hip-Hop, got %+v", res)
	}

	// 4. Alternative \\ Rock must strip spaces and preserve both genres: Alternative\\Rock
	res = AnalyzeAndNormalizeGenre("Alternative \\ Rock")
	if res.HasCyrillic || !res.IsNonCanonical || !res.IsAutoFixable || res.CanonicalGenre != "Alternative\\\\Rock" {
		t.Fatalf("expected Alternative \\ Rock to normalize to Alternative\\\\Rock, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Alternative \\ Indie Rock \\ Rock")
	if !res.IsNonCanonical || !res.IsAutoFixable || res.CanonicalGenre != "Alternative\\\\Indie Rock\\\\Rock" {
		t.Fatalf("expected Alternative \\ Indie Rock \\ Rock to normalize to Alternative\\\\Indie Rock\\\\Rock, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Pop-Rock")
	if res.IsNonCanonical || res.CanonicalGenre != "Pop-Rock" {
		t.Fatalf("expected Pop-Rock to be canonical, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Поп\\Рок")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Pop\\\\Rock" {
		t.Fatalf("expected Поп\\Рок to translate to Pop\\\\Rock, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Рок / Поп")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Rock\\\\Pop" {
		t.Fatalf("expected Рок / Поп to translate to Rock\\\\Pop, got %+v", res)
	}

	// 5. Cyrillic translations
	res = AnalyzeAndNormalizeGenre("Русский рэп")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Russian Hip-Hop" {
		t.Fatalf("expected Русский рэп to translate to Russian Hip-Hop, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Рэп")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Rap" {
		t.Fatalf("expected Рэп to translate to Rap, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Хип-хоп")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Hip-Hop" {
		t.Fatalf("expected Хип-хоп to translate to Hip-Hop, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Поп")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Pop" {
		t.Fatalf("expected Поп to translate to Pop, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Альтернативная музыка")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Alternative" {
		t.Fatalf("expected Alternative, got %+v", res)
	}

	res = AnalyzeAndNormalizeGenre("Танцевальная музыка")
	if !res.HasCyrillic || !res.IsAutoFixable || res.CanonicalGenre != "Dance Music" {
		t.Fatalf("expected Dance Music, got %+v", res)
	}

	// 6. Unknown Russian genre
	res = AnalyzeAndNormalizeGenre("Кастомный неизвестный жанр")
	if !res.HasCyrillic || res.IsAutoFixable {
		t.Fatalf("expected unfixable custom Cyrillic genre, got %+v", res)
	}
}
