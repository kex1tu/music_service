package validator

import (
	"regexp"
	"strings"
)

var cyrillicRegex = regexp.MustCompile(`\p{Cyrillic}`)
var genreSplitRegex = regexp.MustCompile(`(\\\\+|\x00|/|;|,|\|)`)

var RussianGenreMap = map[string]string{
	"поп\\рок":              "Pop \\Rock\\Pop-Rock",
	"поп/рок":               "Pop \\Rock\\Pop-Rock",
	"поп-рок":               "Pop \\Rock\\Pop-Rock",
	"поп рок":               "Pop \\Rock\\Pop-Rock",
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

// GenreCheckResult содержит результат анализа тега жанра.
type GenreCheckResult struct {
	OriginalGenre   string
	HasCyrillic     bool
	IsAutoFixable   bool
	CanonicalGenre  string
}

// AnalyzeAndNormalizeGenre проверяет тег жанра на наличие кириллицы и находит английский аналог.
func AnalyzeAndNormalizeGenre(rawGenre string) GenreCheckResult {
	trimmed := strings.TrimSpace(rawGenre)
	if trimmed == "" {
		return GenreCheckResult{
			OriginalGenre:  rawGenre,
			HasCyrillic:    false,
			IsAutoFixable:  false,
			CanonicalGenre: "",
		}
	}

	hasCyrillic := cyrillicRegex.MatchString(trimmed)
	if !hasCyrillic {
		return GenreCheckResult{
			OriginalGenre:  rawGenre,
			HasCyrillic:    false,
			IsAutoFixable:  false,
			CanonicalGenre: trimmed,
		}
	}

	// 1. Проверяем точное совпадение всей строки (для составных жанров вида Поп\Рок)
	trimmedLower := strings.ToLower(trimmed)
	if directEn, ok := RussianGenreMap[trimmedLower]; ok {
		return GenreCheckResult{
			OriginalGenre:  trimmed,
			HasCyrillic:    true,
			IsAutoFixable:  true,
			CanonicalGenre: directEn,
		}
	}

	// 2. Разбиваем на отдельные жанры, если указано несколько через разделители
	parts := genreSplitRegex.Split(trimmed, -1)
	var normalizedParts []string
	allMapped := true

	for _, p := range parts {
		sub := strings.TrimSpace(p)
		if sub == "" {
			continue
		}
		subLower := strings.ToLower(sub)
		if en, ok := RussianGenreMap[subLower]; ok {
			normalizedParts = append(normalizedParts, en)
		} else if !cyrillicRegex.MatchString(sub) {
			normalizedParts = append(normalizedParts, sub)
		} else {
			allMapped = false
			normalizedParts = append(normalizedParts, sub)
		}
	}

	canonical := strings.Join(normalizedParts, " \\\\ ")
	if len(normalizedParts) == 0 {
		canonical = trimmed
	}

	return GenreCheckResult{
		OriginalGenre:  trimmed,
		HasCyrillic:    true,
		IsAutoFixable:  allMapped && len(normalizedParts) > 0,
		CanonicalGenre: canonical,
	}
}
