package resolver

import (
	"regexp"
	"strings"
)

var (
	parenRegex    = regexp.MustCompile(`[\(\[\{].*?[\)\]\}]`)
	combiningReg  = regexp.MustCompile(`[\p{M}]`)
	nonAlnumReg   = regexp.MustCompile(`[^\p{L}\p{N}\s]`)
	multiSpaceReg = regexp.MustCompile(`\s+`)
	cyrToLat      = map[rune]string{
		'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
		'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
		'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
		'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
		'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	}
)

// NormalizeUnicodeText converts decomposed combining characters (NFD) to precomposed NFC.
func NormalizeUnicodeText(s string) string {
	s = strings.ReplaceAll(s, "\u0438\u0306", "й")
	s = strings.ReplaceAll(s, "\u0418\u0306", "Й")
	s = strings.ReplaceAll(s, "\u0435\u0308", "ё")
	s = strings.ReplaceAll(s, "\u0415\u0308", "Ё")
	s = strings.ReplaceAll(s, "—", "-")
	s = strings.ReplaceAll(s, "–", "-")
	s = strings.ReplaceAll(s, "“", "\"")
	s = strings.ReplaceAll(s, "”", "\"")
	s = strings.ReplaceAll(s, "«", "\"")
	s = strings.ReplaceAll(s, "»", "\"")
	s = combiningReg.ReplaceAllString(s, "")
	return s
}

// TransliterateArtist generates Latin equivalents for Cyrillic artist names (e.g. "макс корж" -> ["maks korzh", "max korzh"]).
func TransliterateArtist(s string) []string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		if lat, ok := cyrToLat[r]; ok {
			sb.WriteString(lat)
		} else {
			sb.WriteRune(r)
		}
	}
	t1 := sb.String()
	t2 := strings.ReplaceAll(t1, "ks", "x")
	if t1 == t2 {
		return []string{t1}
	}
	return []string{t1, t2}
}

// CleanQueryText normalizes text for search queries and matching (stripping parentheses/brackets).
func CleanQueryText(s string) string {
	s = NormalizeUnicodeText(s)
	s = parenRegex.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "ё", "е")
	s = strings.ReplaceAll(s, "Ё", "Е")
	s = nonAlnumReg.ReplaceAllString(s, " ")
	s = multiSpaceReg.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// CleanQueryTextFull normalizes text for search queries while keeping parenthetical content (e.g. Remix, Feat).
func CleanQueryTextFull(s string) string {
	s = NormalizeUnicodeText(s)
	s = strings.ReplaceAll(s, "ё", "е")
	s = strings.ReplaceAll(s, "Ё", "Е")
	s = nonAlnumReg.ReplaceAllString(s, " ")
	s = multiSpaceReg.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// ExtractPrimaryArtist extracts the primary artist name from a potentially multi-valued or featured artist string
// (e.g. "Big Baby Tape\\JEEMBO" -> "Big Baby Tape", "Big Baby Tape\x00JEEMBO" -> "Big Baby Tape", "Big Baby Tape feat. JEEMBO" -> "Big Baby Tape").
func ExtractPrimaryArtist(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// 1. Split on ID3 null byte or double backslash / single backslash multi-value separators
	for _, sep := range []string{"\x00", "\\\\", "\\"} {
		if idx := strings.Index(s, sep); idx != -1 {
			first := strings.TrimSpace(s[:idx])
			if first != "" {
				s = first
				break
			}
		}
	}
	// 2. Split on " feat. ", " feat ", " ft. ", " ft ", " featuring ", " with " (case-insensitive)
	lower := strings.ToLower(s)
	for _, feat := range []string{" feat. ", " feat ", " ft. ", " ft ", " featuring ", " with "} {
		if idx := strings.Index(lower, feat); idx != -1 {
			first := strings.TrimSpace(s[:idx])
			if first != "" {
				s = first
				break
			}
		}
	}
	// 3. Split on " / " (with surrounding spaces, avoiding breaking names like AC/DC)
	if idx := strings.Index(s, " / "); idx != -1 {
		first := strings.TrimSpace(s[:idx])
		if first != "" {
			s = first
		}
	}
	return strings.TrimSpace(s)
}

// AreArtistsEquivalent checks if two artist names are equivalent (e.g. "Макс Корж" and "Max Korzh", or "Big Baby Tape\\JEEMBO" and "Big Baby Tape").
func AreArtistsEquivalent(a, b string) bool {
	aClean := ExtractPrimaryArtist(a)
	bClean := ExtractPrimaryArtist(b)
	aNorm := strings.ToLower(CleanQueryText(aClean))
	bNorm := strings.ToLower(CleanQueryText(bClean))
	if aNorm == "" || bNorm == "" {
		return false
	}
	if aNorm == bNorm {
		return true
	}
	for _, vA := range TransliterateArtist(aNorm) {
		vAClean := strings.ToLower(CleanQueryText(vA))
		if vAClean == bNorm || vA == bNorm {
			return true
		}
	}
	for _, vB := range TransliterateArtist(bNorm) {
		vBClean := strings.ToLower(CleanQueryText(vB))
		if vBClean == aNorm || vB == aNorm {
			return true
		}
	}
	return false
}


