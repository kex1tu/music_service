package id3

import (
	"path/filepath"
	"strconv"
	"strings"

	"music-manager/internal/core/safefs"
)

// applyFrame применяет декодированное значение фрейма к структуре TrackTags.
func applyFrame(id string, data []byte, tags *TrackTags) {
	if id == "TXXX" {
		if len(data) > 1 && data[0] == 3 {
			description, value, ok := strings.Cut(string(data[1:]), "\x00")
			value = strings.TrimRight(value, "\x00")
			if ok && strings.EqualFold(description, sourceURLDescription) && !strings.ContainsRune(value, 0) {
				tags.SourceURL = strings.TrimSpace(value)
			}
		}
		return
	}
	val := decodeText(data)
	if val == "" {
		return
	}

	switch id {
	case "TSRC":
		tags.ISRC = val
	case "TIT2":
		tags.Title = val
	case "TPE1":
		tags.Artist = val
	case "TPE2":
		tags.AlbumArtist = val
	case "TALB":
		tags.Album = val
	case "TCON":
		tags.Genre = val
	case "TRCK":
		tags.HasTrackNumber = parseTrackNum(val, &tags.TrackNumber, &tags.TrackTotal) || tags.HasTrackNumber
	case "TPOS":
		parseTrackNum(val, &tags.DiscNumber, &tags.DiscTotal)
	case "TDRC", "TYER":
		if len(val) >= 4 {
			if yr, err := strconv.Atoi(val[:4]); err == nil && yr > 1900 && yr < 2100 {
				tags.Year = yr
			}
		}
	case "TLEN":
		if ms, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil && ms > 0 {
			tags.Duration = ms / 1000.0
		}
	}
}

// parseTrackNum парсит номер и общее количество треков/дисков из формата "n" или "n/total".
func parseTrackNum(val string, num *int, total *int) bool {
	parts := strings.Split(val, "/")
	n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	hasNumber := err == nil && n > 0
	if hasNumber {
		*num = n
	}
	if len(parts) > 1 {
		if t, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && t > 0 {
			*total = t
		}
	}
	return hasNumber
}

// parseAPIC извлекает бинарные данные обложки и MIME-тип из фрейма APIC.
func parseAPIC(data []byte) *CoverInfo {
	if len(data) < 5 {
		return nil
	}
	// encoding: data[0]
	// MIME type: string terminated by \x00
	idx := 1
	for idx < len(data) && data[idx] != 0 {
		idx++
	}
	if idx >= len(data) {
		return nil
	}
	mime := string(data[1:idx])
	idx++ // пропуск \x00

	if idx >= len(data) {
		return nil
	}
	// picture type: data[idx] (3 = Cover Front)
	idx++

	// description: terminated by \x00 (or \x00\x00 in utf16)
	for idx < len(data) && data[idx] != 0 {
		idx++
	}
	for idx < len(data) && data[idx] == 0 {
		idx++
	}

	if idx >= len(data) {
		return nil
	}

	imgData := data[idx:]
	if len(imgData) == 0 {
		return nil
	}

	if mime == "" {
		if len(imgData) >= 2 && imgData[0] == 0xFF && imgData[1] == 0xD8 {
			mime = "image/jpeg"
		} else if len(imgData) >= 8 && string(imgData[1:4]) == "PNG" {
			mime = "image/png"
		} else {
			mime = "image/jpeg"
		}
	}

	return &CoverInfo{
		MimeType: mime,
		Data:     imgData,
	}
}

// checkLyrics проверяет наличие внешнего файла текста (.lrc или .txt) рядом с треком.
func checkLyrics(filePath string) string {
	ext := filepath.Ext(filePath)
	base := strings.TrimSuffix(filePath, ext)
	if st, err := safefs.Stat(base + ".lrc"); err == nil && !st.IsDir() {
		return "lrc"
	}
	if st, err := safefs.Stat(base + ".txt"); err == nil && !st.IsDir() {
		return "txt"
	}
	return "none"
}
