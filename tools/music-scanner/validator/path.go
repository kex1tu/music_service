package validator

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var dangerousCharsRegex = regexp.MustCompile(`[\x00-\x1f\x7f/\\:*?"<>|]`)
var multiSeparatorRegex = regexp.MustCompile(`(\\+|\x00)`)

// SanitizeComponent нормализует компонент пути (имя папки или файла) согласно правилам проекта.
func SanitizeComponent(name string, fallback string, maxLength int) string {
	if strings.TrimSpace(name) == "" {
		name = fallback
	}

	clean := multiSeparatorRegex.ReplaceAllString(name, " & ")
	clean = strings.TrimSpace(clean)
	clean = dangerousCharsRegex.ReplaceAllString(clean, "_")
	clean = strings.Trim(clean, ". ")

	runes := []rune(clean)
	if len(runes) > maxLength {
		clean = strings.TrimRight(string(runes[:maxLength]), ". ")
	}

	if strings.TrimSpace(clean) == "" {
		clean = fallback
	}

	return clean
}

// ValidateTrackPath проверяет соответствие пути шаблону {Album}/{TrackNumber:02d}. {Title}.mp3
func ValidateTrackPath(relPath string, album string, trackNum int, title string) (isValid bool, suggestedPath string, reason string) {
	relPath = filepath.ToSlash(filepath.Clean(relPath))

	cleanAlbum := SanitizeComponent(album, "Unknown Album", 180)
	cleanTitle := SanitizeComponent(title, "Unknown Title", 180)

	if trackNum <= 0 {
		trackNum = 1
	}

	expectedFileName := fmt.Sprintf("%02d. %s.mp3", trackNum, cleanTitle)
	expectedRelPath := filepath.ToSlash(filepath.Join(cleanAlbum, expectedFileName))

	if relPath == expectedRelPath {
		return true, relPath, ""
	}

	var reasons []string
	actualDir := filepath.Dir(relPath)
	actualFile := filepath.Base(relPath)

	if actualDir != cleanAlbum {
		reasons = append(reasons, fmt.Sprintf("папка '%s' не совпадает с канонической '%s'", actualDir, cleanAlbum))
	}
	if actualFile != expectedFileName {
		reasons = append(reasons, fmt.Sprintf("имя файла '%s' не совпадает с каноническим '%s'", actualFile, expectedFileName))
	}

	return false, expectedRelPath, strings.Join(reasons, "; ")
}
