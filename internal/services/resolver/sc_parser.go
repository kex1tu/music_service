package resolver

import (
	"path/filepath"
	"regexp"
	"strings"

	"music-manager/internal/id3"
)

var (
	// Очистка промо-маркеров SoundCloud: [FREE DL], (Buy = Free DL), [OUT NOW], etc.
	scPromoJunkRegex = regexp.MustCompile(`(?i)\s*[\(\[]\s*(?:free\s+(?:dl|download)|buy\s*=\s*free\s+dl|out\s+now|exclusive|free\s*dl|free|dl)\s*[\)\]]`)
	// Очистка (prod. by ...) / (prod. ...)
	scProdJunkRegex = regexp.MustCompile(`(?i)\s*[\(\[]\s*prod\.?(?:\s+by)?\s+[^\]\)]+[\)\]]`)
	// Очистка лидирующих номеров: "01. ", "1 - "
	scLeadingNumRegex = regexp.MustCompile(`^\s*\d{1,3}[\.\-_\s]+\s*`)
)

// CleanSoundCloudTitle удаляет рекламный и продюсерский мусор из заголовка SoundCloud.
func CleanSoundCloudTitle(rawTitle string) string {
	t := strings.TrimSpace(rawTitle)
	t = scPromoJunkRegex.ReplaceAllString(t, "")
	t = scProdJunkRegex.ReplaceAllString(t, "")
	t = scLeadingNumRegex.ReplaceAllString(t, "")
	t = ytTrailingJunkRegex.ReplaceAllString(t, "")
	t = ytVideoJunkRegex.ReplaceAllString(t, "")
	t = strings.TrimSpace(t)
	return id3.CleanTitle(t, filepath.Base(t))
}

// ParseSoundCloudTitle разбирает заголовок трека SoundCloud и возвращает нормализованных артиста и название трека.
func ParseSoundCloudTitle(rawTitle, uploader string) (artist, title string) {
	cleaned := CleanSoundCloudTitle(rawTitle)
	if cleaned == "" {
		cleaned = strings.TrimSpace(rawTitle)
	}

	cleanUploader := CleanChannelName(uploader)
	cleanUploader = strings.TrimSuffix(cleanUploader, "-music")
	cleanUploader = strings.TrimSuffix(cleanUploader, " Music")
	cleanUploader = strings.TrimSuffix(cleanUploader, " Records")
	cleanUploader = strings.TrimSpace(cleanUploader)

	// Стандартные разделители между артистом и названием
	delims := []string{" — ", " – ", " - ", " : ", " // "}

	var leftPart, rightPart string
	splitDone := false

	for _, delim := range delims {
		if idx := strings.Index(cleaned, delim); idx != -1 {
			leftPart = strings.TrimSpace(cleaned[:idx])
			rightPart = strings.TrimSpace(cleaned[idx+len(delim):])
			if len(leftPart) >= 2 && len(rightPart) >= 1 {
				splitDone = true
				break
			}
		}
	}

	if splitDone {
		// Проверяем: возможно, левая часть — это известный агрегатор/паблик/лейбл (например, "Русская Рэп-Сцена")
		if isLabelChannel(leftPart) {
			// Пробуем разделить оставшуюся правую часть еще раз
			innerSplit := false
			for _, delim := range delims {
				if idx := strings.Index(rightPart, delim); idx != -1 {
					innerLeft := strings.TrimSpace(rightPart[:idx])
					innerRight := strings.TrimSpace(rightPart[idx+len(delim):])
					if len(innerLeft) >= 2 && len(innerRight) >= 1 {
						artist = innerLeft
						title = innerRight
						innerSplit = true
						break
					}
				}
			}
			if !innerSplit {
				title = rightPart
				if cleanUploader != "" && !isLabelChannel(cleanUploader) {
					artist = cleanUploader
				} else {
					artist = "Unknown Artist"
				}
			}
		} else {
			artist = leftPart
			title = rightPart
		}
	} else {
		// Разделителя нет: заголовок — это название песни
		title = cleaned
		if cleanUploader != "" && !isLabelChannel(cleanUploader) {
			artist = cleanUploader
		} else if cleanUploader != "" {
			artist = cleanUploader
		} else {
			artist = "Unknown Artist"
		}
	}

	return strings.TrimSpace(artist), strings.TrimSpace(title)
}
