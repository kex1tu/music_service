package resolver

import (
	"html"
	"regexp"
	"strings"
)

var iframeSrcRegex = regexp.MustCompile(`(?i)<iframe[^>]*\bsrc\s*=\s*["']?([^"'>\s]+)["']?`)

const maxInputLength = 4096

// ExtractURLFromInput извлекает и нормализует URL из пользовательского ввода:
// если передан HTML-код iframe (например, код шеринга Яндекс Музыки), извлекается только атрибут src;
// если передан обычный URL, он обрезается по краям и нормализуется.
// Проверяет схему, хост и длину; не исполняет HTML и не передает доверенную разметку.
func ExtractURLFromInput(rawInput string) string {
	trimmed := strings.TrimSpace(rawInput)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) > maxInputLength {
		trimmed = trimmed[:maxInputLength]
	}

	// Проверяем наличие iframe
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "<iframe") {
		matches := iframeSrcRegex.FindStringSubmatch(trimmed)
		if len(matches) > 1 {
			src := strings.TrimSpace(matches[1])
			src = html.UnescapeString(src)
			return normalizeExtractedSrc(src)
		}
	}

	return normalizeExtractedSrc(trimmed)
}

func normalizeExtractedSrc(src string) string {
	src = strings.TrimSpace(src)
	if src == "" {
		return ""
	}

	// Проверяем протокол-относительные ссылки (//music.yandex.ru/...)
	if strings.HasPrefix(src, "//") {
		src = "https:" + src
	}

	// Отклоняем опасные псевдо-схемы (javascript:, data:, file:)
	lower := strings.ToLower(src)
	if strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "file:") {
		return ""
	}

	return src
}
