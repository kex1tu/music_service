package validator

import (
	"regexp"
	"strings"
)

var (
	cyrillicRegex   = regexp.MustCompile(`\p{Cyrillic}`)
	genreSplitRegex = regexp.MustCompile(`(\\\\+|\x00|/|;|,|\|)`)
	rockRegex       = regexp.MustCompile(`(?i)(?:^|[\s/\\;,\-|_\(\)\[\]])(rock|рок)(?:$|[\s/\\;,\-|_\(\)\[\]])|rock|рок`)
	hipPopRapRegex  = regexp.MustCompile(`(?i)\b(?:pop|поп|hip[- ]?hop|хип[- ]?хоп|хипхоп|hip|хип|rap|рэп)\b|(?:^|[\s/\\;,\-|_\(\)\[\]])(?:pop|поп|hip|хип|rap|рэп)(?:$|[\s/\\;,\-|_\(\)\[\]])|hip[- ]?hop|хип[- ]?хоп|хипхоп|pop|поп|рэп|\brap\b`)
)

var RussianGenreMap = map[string]string{
	"поп\\рок":              "Pop \\\\Rock\\\\Pop-Rock",
	"поп\\\\рок":            "Pop \\\\Rock\\\\Pop-Rock",
	"поп/рок":               "Pop \\\\Rock\\\\Pop-Rock",
	"поп-рок":               "Pop \\\\Rock\\\\Pop-Rock",
	"поп рок":               "Pop \\\\Rock\\\\Pop-Rock",
	"альтернативная музыка": "Alternative",
	"альтернатива":          "Alternative",
	"танцевальная музыка":   "Dance Music",
	"танцевальная":          "Dance Music",
	"русский рэп":          "Russian Hip-Hop",
	"рэп":                  "Hip-Hop",
	"хип-хоп":              "Hip-Hop",
	"хип хоп":              "Hip-Hop",
	"хипхоп":               "Hip-Hop",
	"русский хип-хоп":      "Russian Hip-Hop",
	"рок":                  "Rock",
	"русский рок":          "Russian Rock",
	"альтернативный рок":   "Alternative Rock",
	"поп":                  "Pop",
	"поп-музыка":           "Pop",
	"поп музыка":           "Pop",
	"русский поп":          "Russian Pop",
	"электроника":          "Electronic",
	"электронная музыка":   "Electronic",
	"шансон":              "Chanson",
	"русский шансон":      "Russian Chanson",
	"метал":                "Metal",
	"металл":               "Metal",
	"хэви-метал":           "Heavy Metal",
	"тяжелый рок":          "Hard Rock",
	"тяжёлый рок":          "Hard Rock",
	"джаз":                 "Jazz",
	"блюз":                 "Blues",
	"фонк":                 "Phonk",
	"дрилл":                "Drill",
	"панк":                 "Punk",
	"панк-рок":             "Punk Rock",
	"панк рок":             "Punk Rock",
	"инди":                 "Indie",
	"инди-рок":             "Indie Rock",
	"диско":                "Disco",
	"регги":                "Reggae",
	"классика":             "Classical",
	"классическая музыка":   "Classical",
	"саундтрек":            "Soundtrack",
	"эмбиент":              "Ambient",
	"хаус":                 "House",
	"техно":                "Techno",
	"транс":                "Trance",
	"драм-н-бэйс":          "Drum and Bass",
	"дабстеп":              "Dubstep",
}


func isRock(s string) bool {
	lower := strings.ToLower(s)
	lower = strings.ReplaceAll(lower, "барокко", "")
	lower = strings.ReplaceAll(lower, "baroque", "")
	return rockRegex.MatchString(lower)
}

func isHipPopRap(s string) bool {
	lower := strings.ToLower(s)
	return hipPopRapRegex.MatchString(lower)
}

// GenreCheckResult содержит результат анализа тега жанра.
type GenreCheckResult struct {
	OriginalGenre   string
	HasCyrillic     bool
	IsNonCanonical  bool
	IsAutoFixable   bool
	CanonicalGenre  string
}

// AnalyzeAndNormalizeGenre проверяет тег жанра на наличие кириллицы и находит канонический аналог.
func AnalyzeAndNormalizeGenre(rawGenre string) GenreCheckResult {
	trimmed := strings.TrimSpace(rawGenre)
	if trimmed == "" {
		return GenreCheckResult{
			OriginalGenre:  rawGenre,
			HasCyrillic:    false,
			IsNonCanonical: false,
			IsAutoFixable:  false,
			CanonicalGenre: "",
		}
	}

	hasCyrillic := cyrillicRegex.MatchString(trimmed)

	// 1. Rock / Рок (барокко исключается)
	if isRock(trimmed) {
		isCanonical := (trimmed == "Rock")
		return GenreCheckResult{
			OriginalGenre:  trimmed,
			HasCyrillic:    hasCyrillic,
			IsNonCanonical: !isCanonical,
			IsAutoFixable:  !isCanonical,
			CanonicalGenre: "Rock",
		}
	}

	// 2. Hip-Hop / Pop / Rap
	if isHipPopRap(trimmed) {
		isCanonical := (trimmed == "Hip-Hop")
		return GenreCheckResult{
			OriginalGenre:  trimmed,
			HasCyrillic:    hasCyrillic,
			IsNonCanonical: !isCanonical,
			IsAutoFixable:  !isCanonical,
			CanonicalGenre: "Hip-Hop",
		}
	}

	// 3. Прямое совпадение всей строки (для устойчивых составных жанров)
	trimmedLower := strings.ToLower(trimmed)
	trimmedSingle := strings.ReplaceAll(trimmedLower, "\\\\", "\\")
	if directEn, ok := RussianGenreMap[trimmedLower]; ok {
		return GenreCheckResult{
			OriginalGenre:  trimmed,
			HasCyrillic:    hasCyrillic,
			IsNonCanonical: true,
			IsAutoFixable:  true,
			CanonicalGenre: directEn,
		}
	}
	if directEn, ok := RussianGenreMap[trimmedSingle]; ok {
		return GenreCheckResult{
			OriginalGenre:  trimmed,
			HasCyrillic:    hasCyrillic,
			IsNonCanonical: true,
			IsAutoFixable:  true,
			CanonicalGenre: directEn,
		}
	}

	// 4. Покомпонентная нормализация
	parts := genreSplitRegex.Split(trimmed, -1)
	var normalizedParts []string
	seen := make(map[string]bool)
	allMapped := true

	for _, p := range parts {
		sub := strings.TrimSpace(p)
		if sub == "" {
			continue
		}
		subLower := strings.ToLower(sub)
		if en, ok := RussianGenreMap[subLower]; ok {
			subParts := genreSplitRegex.Split(en, -1)
			for _, sp := range subParts {
				spClean := strings.TrimSpace(sp)
				if spClean != "" && !seen[strings.ToLower(spClean)] {
					seen[strings.ToLower(spClean)] = true
					normalizedParts = append(normalizedParts, spClean)
				}
			}
		} else if !cyrillicRegex.MatchString(sub) {
			if !seen[subLower] {
				seen[subLower] = true
				normalizedParts = append(normalizedParts, sub)
			}
		} else {
			allMapped = false
			if !seen[subLower] {
				seen[subLower] = true
				normalizedParts = append(normalizedParts, sub)
			}
		}
	}

	canonical := strings.Join(normalizedParts, "\\\\")
	if len(normalizedParts) == 0 {
		canonical = trimmed
	}

	isCanonical := (trimmed == canonical) && !hasCyrillic

	return GenreCheckResult{
		OriginalGenre:  trimmed,
		HasCyrillic:    hasCyrillic,
		IsNonCanonical: !isCanonical,
		IsAutoFixable:  allMapped && !isCanonical,
		CanonicalGenre: canonical,
	}
}
