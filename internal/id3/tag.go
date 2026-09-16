package id3

import (
	"regexp"
	"strings"
)

var (
	// Регулярка для детекции утекших заголовков ID3-фреймов
	leakedFrameHeaderRegex = regexp.MustCompile(`(?i)\b(TALB|TIT2|TPE1|TPE2|TCON|TRCK|TPOS|COMM|APIC)\b`)
	// Регулярка для отсечения повторяющихся префиксов дорожек (05. 05. 05. -> '')
	trackPrefixRegex = regexp.MustCompile(`^(?:\d{1,3}[\s._-]+)+`)
)

// TrackTags представляет полные метаданные аудиодорожки.
type TrackTags struct {
	Path         string  `json:"path"`
	Title        string  `json:"title"`
	Artist       string  `json:"artist"`
	Album        string  `json:"album"`
	AlbumArtist  string  `json:"album_artist"`
	Year         int     `json:"year"`
	Genre        string  `json:"genre"`
	TrackNumber  int     `json:"track_number"`
	TrackTotal   int     `json:"track_total"`
	DiscNumber   int     `json:"disc_number"`
	DiscTotal    int     `json:"disc_total"`
	Duration     float64 `json:"duration"`
	Bitrate      int     `json:"bitrate"`
	HasCover     bool    `json:"has_cover"`
	LyricsStatus string  `json:"lyrics_status"` // "lrc", "txt", "none"
	LyricsPath   string  `json:"lyrics_path,omitempty"`
}

// RawFrame represents an unparsed or pass-through ID3v2 frame.
type RawFrame struct {
	ID    string
	Flags [2]byte
	Data  []byte
}

// CleanTitle возвращает очищенное от префиксов номеров и утекших фреймов название трека.
func CleanTitle(title string, fallbackFileName string) string {
	t := strings.TrimSpace(title)

	// 1. Проверка на утечку фрейма (баг ПоTALB)
	if leakedFrameHeaderRegex.MatchString(t) || strings.Contains(t, "& _ & _") {
		// Отбрасываем поврежденный тег и берем имя файла
		t = fallbackFileName
	}

	// 2. Очистка расширения .mp3 при наличии
	t = strings.TrimSuffix(t, ".mp3")
	t = strings.TrimSuffix(t, ".MP3")

	// 3. Отсечение каскада префиксов номеров дорожек: "05. 05. 05. Похрюкай" -> "Похрюкай"
	t = trackPrefixRegex.ReplaceAllString(t, "")
	t = strings.TrimSpace(t)

	if t == "" {
		// Если после очистки ничего не осталось, берем fallback
		cleanFallback := trackPrefixRegex.ReplaceAllString(fallbackFileName, "")
		cleanFallback = strings.TrimSuffix(cleanFallback, ".mp3")
		cleanFallback = strings.TrimSpace(cleanFallback)
		if cleanFallback != "" {
			return cleanFallback
		}
		return "Unknown Title"
	}

	return t
}

// ParseMultiValue разбивает строку вида "Pop\\Rock" или с разделителями в слайс.
func ParseMultiValue(val string) []string {
	if strings.TrimSpace(val) == "" {
		return nil
	}
	raw := strings.ReplaceAll(val, "\x00", "\\\\")
	parts := strings.Split(raw, "\\\\")
	var res []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}

// FormatMultiValue объединяет слайс значений в строку с разделителем '\\' для UI.
func FormatMultiValue(items []string) string {
	var clean []string
	for _, it := range items {
		t := strings.TrimSpace(it)
		if t != "" {
			clean = append(clean, t)
		}
	}
	return strings.Join(clean, "\\\\")
}
