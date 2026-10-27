package id3

import (
	"regexp"
	"strings"
)

var (
	cyrillicRegex   = regexp.MustCompile(`\p{Cyrillic}`)
	genreSplitRegex = regexp.MustCompile(`(?:\s*\\+\s*|\x00|\s*[/;,|]\s*|\r?\n)`)
	hipHopRegex     = regexp.MustCompile(`(?i)\b(?:hip[- ]?hop|хип[- ]?хоп|hiphop)\b`)
)

// RussianGenreMap содержит отображение русскоязычных названий жанров на канонические английские.
var RussianGenreMap = map[string]string{
	"поп\\рок":              "Pop\\\\Rock",
	"поп\\\\рок":            "Pop\\\\Rock",
	"поп/рок":               "Pop\\\\Rock",
	"поп-рок":               "Pop-Rock",
	"поп рок":               "Pop-Rock",
	"альтернативная музыка": "Alternative",
	"альтернатива":          "Alternative",
	"танцевальная музыка":   "Dance Music",
	"танцевальная":          "Dance Music",
	"русский рэп":          "Russian Hip-Hop",
	"рэп":                  "Rap",
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

// CanonicalGenreMap нормализует регистр и написание стандартных английских жанров.
var CanonicalGenreMap = map[string]string{
	"hip-hop":             "Hip-Hop",
	"hip hop":             "Hip-Hop",
	"hiphop":              "Hip-Hop",
	"rap":                 "Rap",
	"rock":                "Rock",
	"pop":                 "Pop",
	"alternative":         "Alternative",
	"alternative rock":    "Alternative Rock",
	"indie":               "Indie",
	"indie rock":          "Indie Rock",
	"pop-rock":            "Pop-Rock",
	"pop rock":            "Pop-Rock",
	"hard rock":           "Hard Rock",
	"punk":                "Punk",
	"punk rock":           "Punk Rock",
	"metal":               "Metal",
	"heavy metal":         "Heavy Metal",
	"jazz":                "Jazz",
	"blues":               "Blues",
	"electronic":          "Electronic",
	"dance":               "Dance",
	"dance music":         "Dance Music",
	"house":               "House",
	"techno":              "Techno",
	"trance":              "Trance",
	"dubstep":             "Dubstep",
	"drum and bass":       "Drum and Bass",
	"drum & bass":         "Drum and Bass",
	"dnb":                 "Drum and Bass",
	"phonk":               "Phonk",
	"drill":               "Drill",
	"classical":           "Classical",
	"soundtrack":          "Soundtrack",
	"ambient":             "Ambient",
	"disco":               "Disco",
	"reggae":              "Reggae",
	"chanson":             "Chanson",
	"folk":                "Folk",
	"soul":                "Soul",
	"funk":                "Funk",
	"r&b":                 "R&B",
	"rnb":                 "R&B",
	"r and b":             "R&B",
	"k-pop":               "K-Pop",
	"kpop":                "K-Pop",
	"j-pop":               "J-Pop",
	"jpop":                "J-Pop",
	"lo-fi":               "Lo-Fi",
	"lofi":                "Lo-Fi",
	"edm":                 "EDM",
	"synthpop":            "Synthpop",
	"synth-pop":           "Synthpop",
	"russian rock":        "Russian Rock",
	"russian rap":         "Russian Rap",
	"russian hip-hop":     "Russian Hip-Hop",
	"russian pop":         "Russian Pop",
	"russian chanson":     "Russian Chanson",
}

// NormalizeSingleGenre приводит отдельное значение жанра к каноническому виду.
func NormalizeSingleGenre(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	if canon, ok := CanonicalGenreMap[lower]; ok {
		return canon
	}
	replaced := hipHopRegex.ReplaceAllString(trimmed, "Hip-Hop")
	if replaced != trimmed {
		return replaced
	}
	if strings.ToLower(trimmed) == trimmed && !cyrillicRegex.MatchString(trimmed) {
		words := strings.Fields(trimmed)
		for i, w := range words {
			subWords := strings.Split(w, "-")
			for j, sw := range subWords {
				if len(sw) > 0 {
					subWords[j] = strings.ToUpper(sw[:1]) + sw[1:]
				}
			}
			words[i] = strings.Join(subWords, "-")
		}
		return strings.Join(words, " ")
	}
	return trimmed
}

// GenreCheckResult содержит результат анализа тега жанра.
type GenreCheckResult struct {
	OriginalGenre  string
	HasCyrillic    bool
	IsNonCanonical bool
	IsAutoFixable  bool
	CanonicalGenre string
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
				if spClean == "" {
					continue
				}
				norm := NormalizeSingleGenre(spClean)
				lowerNorm := strings.ToLower(norm)
				if !seen[lowerNorm] {
					seen[lowerNorm] = true
					normalizedParts = append(normalizedParts, norm)
				}
			}
		} else if !cyrillicRegex.MatchString(sub) {
			norm := NormalizeSingleGenre(sub)
			lowerNorm := strings.ToLower(norm)
			if !seen[lowerNorm] {
				seen[lowerNorm] = true
				normalizedParts = append(normalizedParts, norm)
			}
		} else {
			allMapped = false
			norm := NormalizeSingleGenre(sub)
			lowerNorm := strings.ToLower(norm)
			if !seen[lowerNorm] {
				seen[lowerNorm] = true
				normalizedParts = append(normalizedParts, norm)
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

// CanonicalizeGenre возвращает канонический жанр (Title Case, латиница, без дубликатов).
// Если жанр пустой или неизвестный не-кириллический, возвращает очищенное исходное значение.
func CanonicalizeGenre(raw string) string {
	res := AnalyzeAndNormalizeGenre(raw)
	if res.CanonicalGenre != "" {
		return res.CanonicalGenre
	}
	return strings.TrimSpace(raw)
}
