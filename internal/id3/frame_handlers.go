package id3

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"music-manager/internal/core/safefs"
)

// applyFrame применяет декодированное значение фрейма к структуре TrackTags.
func applyFrame(id string, data []byte, tags *TrackTags) {
	val := decodeText(data)
	if val == "" {
		return
	}

	switch id {
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
		parseTrackNum(val, &tags.TrackNumber, &tags.TrackTotal)
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
func parseTrackNum(val string, num *int, total *int) {
	parts := strings.Split(val, "/")
	if len(parts) > 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil && n > 0 {
			*num = n
		}
	}
	if len(parts) > 1 {
		if t, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && t > 0 {
			*total = t
		}
	}
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

// estimateDurationAndBitrate рассчитывает приблизительный битрейт и длительность трека по размеру аудиопотока.
func estimateDurationAndBitrate(file *os.File, fileSize int64, tagSize int, tags *TrackTags) {
	audioSize := fileSize - int64(tagSize)
	if audioSize <= 0 {
		return
	}
	// По умолчанию стандартный 320 kbps MP3
	tags.Bitrate = 320
	// 320 kbps = 40 000 байт/сек
	tags.Duration = float64(audioSize) / 40000.0
}
