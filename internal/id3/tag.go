package id3

import (
	"regexp"
	"strings"
)

var (
	// Регулярка для детекции утекших заголовков ID3-фреймов
	leakedFrameHeaderRegex = regexp.MustCompile(`(?i)\b(TALB|TIT2|TPE1|TPE2|TCON|TRCK|TPOS|COMM|APIC)\b`)
	// Регулярка для отсечения повторяющихся префиксов номеров дорожек (05. 05. 05. -> '')
	// Требует разделитель (., -, _, – или —) и обязательно хотя бы один пробел после него (\s+),
	// чтобы не срезать префиксы у исполнителей и названий вида "1.Kla$", "1.Царь", "3.14", "21 Guns", "1979", "505".
	trackPrefixRegex = regexp.MustCompile(`^(?:\d{1,3}\s*[\.\-_–—]\s+)+`)

	// Разделители для множественных значений артистов (null-byte, 1+ backslashes, ;, |)
	multiValueSplitRegex = regexp.MustCompile(`(?:\s*\\+\s*|\x00|\s*[;|]\s*)`)
	// Разделители для жанров (null-byte, 1+ backslashes, /, ;, |, ,)
	genreMultiSplitRegex = regexp.MustCompile(`(?:\s*\\+\s*|\x00|\s*[/;,|]\s*|\r?\n)`)
)

// TrackTags представляет полные метаданные аудиодорожки.
type TrackTags struct {
	Path           string  `json:"path"`
	Title          string  `json:"title"`
	Artist         string  `json:"artist"`
	Album          string  `json:"album"`
	AlbumArtist    string  `json:"album_artist"`
	Year           int     `json:"year"`
	Genre          string  `json:"genre"`
	TrackNumber    int     `json:"track_number"`
	TrackTotal     int     `json:"track_total"`
	DiscNumber     int     `json:"disc_number"`
	DiscTotal      int     `json:"disc_total"`
	Duration       float64 `json:"duration"`
	Bitrate        int     `json:"bitrate"`
	HasCover       bool    `json:"has_cover"`
	LyricsStatus   string  `json:"lyrics_status"` // "lrc", "txt", "none"
	LyricsPath     string  `json:"lyrics_path,omitempty"`
	SourceURL      string  `json:"-"`
	ISRC           string  `json:"-"`
	HasTrackNumber bool    `json:"-"`
}

const sourceURLDescription = "MUSIC_MANAGER_SOURCE_URL"

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
		cleanFallback = strings.TrimSuffix(cleanFallback, ".MP3")
		cleanFallback = strings.TrimSpace(cleanFallback)
		if cleanFallback != "" {
			return cleanFallback
		}
		return "Unknown Title"
	}

	return t
}

// ParseMultiValue разбивает строку вида "Artist 1\\Artist 2" или с разделителями в слайс.
func ParseMultiValue(val string) []string {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return nil
	}
	parts := multiValueSplitRegex.Split(trimmed, -1)
	var res []string
	seen := make(map[string]bool)
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			lower := strings.ToLower(t)
			if !seen[lower] {
				seen[lower] = true
				res = append(res, t)
			}
		}
	}
	return res
}

// ParseGenres разбивает строку жанров вида "Alternative\\Rock" или "Rap/Hip Hop" в нормализованный слайс жанров.
func ParseGenres(val string) []string {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return nil
	}
	parts := genreMultiSplitRegex.Split(trimmed, -1)
	var res []string
	seen := make(map[string]bool)
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t == "" {
			continue
		}
		norm := NormalizeSingleGenre(t)
		if norm == "" {
			norm = t
		}
		lower := strings.ToLower(norm)
		if !seen[lower] {
			seen[lower] = true
			res = append(res, norm)
		}
	}
	return res
}

// FormatMultiValue объединяет слайс значений в строку с разделителем '\\' для UI.
func FormatMultiValue(items []string) string {
	var clean []string
	seen := make(map[string]bool)
	for _, it := range items {
		t := strings.TrimSpace(it)
		if t != "" {
			lower := strings.ToLower(t)
			if !seen[lower] {
				seen[lower] = true
				clean = append(clean, t)
			}
		}
	}
	return strings.Join(clean, "\\\\")
}

// FormatGenres объединяет слайс жанров в каноническую строку с разделителем '\\' для UI.
func FormatGenres(items []string) string {
	var clean []string
	seen := make(map[string]bool)
	for _, it := range items {
		t := strings.TrimSpace(it)
		if t != "" {
			norm := NormalizeSingleGenre(t)
			if norm == "" {
				norm = t
			}
			lower := strings.ToLower(norm)
			if !seen[lower] {
				seen[lower] = true
				clean = append(clean, norm)
			}
		}
	}
	return strings.Join(clean, "\\\\")
}
