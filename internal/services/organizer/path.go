package organizer

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"music-manager/internal/id3"
)

const (
	// MaxComponentBytes defines the maximum allowed byte length for a single filesystem path component
	// (directory or file name) on standard Linux filesystems (NAME_MAX = 255 bytes).
	MaxComponentBytes = 255

	// MaxCompanionExtLen is the maximum byte length of companion extensions (.jpeg, .webp = 5 bytes).
	MaxCompanionExtLen = 5

	// MaxBaseFilenameBytes is the maximum safe byte budget for a base filename without extension,
	// guaranteeing that any companion extension (.jpeg, .webp, .lrc, etc.) fits within MaxComponentBytes (250 bytes).
	MaxBaseFilenameBytes = MaxComponentBytes - MaxCompanionExtLen // 250 bytes

	// SuffixReserveBytes is the reserved safety budget for collision suffixes (e.g. " (999)")
	// and companion files to allow collision resolution without excessive truncation.
	SuffixReserveBytes = 16
)

var (
	// dangerousFsChars matches filesystem forbidden chars: / \ : * ? " < > | \x00 and ASCII control codes (0-31)
	dangerousFsChars = regexp.MustCompile(`[\x00-\x1f/\\:*?"<>|]`)
	multiSepRegex    = regexp.MustCompile(`(\\+|\x00)`)
)

// TruncateToByteBudget truncates a UTF-8 string to fit within maxBytes without breaking multi-byte runes.
// It stops at the last complete UTF-8 rune boundary, never splitting a multi-byte character (Cyrillic, emoji, etc.).
// If maxBytes <= 0, it returns an empty string.
func TruncateToByteBudget(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}

	lastValid := 0
	for i := 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:])
		if i+size > maxBytes {
			break
		}
		i += size
		lastValid = i
	}
	return s[:lastValid]
}

func sanitizeString(s string) string {
	clean := strings.TrimSpace(s)
	if clean == "" {
		return ""
	}
	// 1. Replace artist/genre multi-value separators
	clean = multiSepRegex.ReplaceAllString(clean, " & ")
	// 2. Replace illegal filesystem and control characters with underscore
	clean = dangerousFsChars.ReplaceAllString(clean, "_")
	// 3. Trim leading/trailing dots and spaces
	return strings.Trim(clean, ". ")
}

// SanitizeComponentWithBudget sanitizes a directory or filename component:
// - Removes illegal filesystem characters (/, \, :, *, ?, ", <, >, |, \x00, control ASCII 0-31).
// - Replaces multiple separators (\\, \x00) with " & ".
// - Restricts length by rune count (maxRunes) with clean rune boundary slicing.
// - Restricts length by byte budget (maxBytes) without breaking UTF-8 runes.
// - Removes trailing dots and spaces to prevent Windows/crossplatform issues.
// - If the result is empty, falls back to fallback (also sanitized and constrained to budgets).
// - For an impossible budget (maxBytes <= 0), returns an empty string.
func SanitizeComponentWithBudget(name, fallback string, maxRunes, maxBytes int) string {
	if maxRunes <= 0 {
		maxRunes = 180
	}
	if maxBytes <= 0 {
		return ""
	}

	clean := sanitizeString(name)
	if clean == "" {
		clean = sanitizeString(fallback)
	}

	// Truncate by rune count (preserving existing maxLength behavior)
	if utf8.RuneCountInString(clean) > maxRunes {
		runes := []rune(clean)
		clean = strings.TrimRight(string(runes[:maxRunes]), ". ")
	}

	// Truncate by byte budget without breaking UTF-8 sequences
	if len(clean) > maxBytes {
		clean = strings.TrimRight(TruncateToByteBudget(clean, maxBytes), ". ")
	}

	if clean == "" {
		fallbackClean := sanitizeString(fallback)
		if utf8.RuneCountInString(fallbackClean) > maxRunes {
			runes := []rune(fallbackClean)
			fallbackClean = strings.TrimRight(string(runes[:maxRunes]), ". ")
		}
		if len(fallbackClean) > maxBytes {
			fallbackClean = strings.TrimRight(TruncateToByteBudget(fallbackClean, maxBytes), ". ")
		}
		clean = fallbackClean
	}

	return clean
}

// SanitizeComponent sanitizes a directory or filename component:
// - Preserves existing signature and maxLength (runes) behavior.
// - Additionally guarantees that the byte length does not exceed MaxComponentBytes (255 bytes).
func SanitizeComponent(name, fallback string, maxLength int) string {
	return SanitizeComponentWithBudget(name, fallback, maxLength, MaxComponentBytes)
}

// CleanTitle removes duplicate leading track number prefixes (e.g. "01. 01. Song" -> "Song").
func CleanTitle(title string, origFilename string) string {
	return id3.CleanTitle(title, origFilename)
}

// ComputeAlbumDirWithBudget calculates canonical album directory name: {Album} - {Album Artist}
// constrained after assembly to at most maxBytes (valid UTF-8, no broken runes, fair budget split when both parts are long).
func ComputeAlbumDirWithBudget(albumArtist, albumTitle string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}

	artist := strings.TrimSpace(albumArtist)
	album := strings.TrimSpace(albumTitle)

	if artist == "" {
		artist = "Unknown Artist"
	}
	if album == "" {
		album = "Unknown Album"
	}

	safeAlbum := SanitizeComponentWithBudget(album, "Unknown Album", 100, maxBytes)
	safeArtist := SanitizeComponentWithBudget(artist, "Unknown Artist", 100, maxBytes)

	// If budget is very small (less than separator " - " + 2 bytes):
	if maxBytes < 5 {
		res := safeAlbum
		if res == "" {
			res = safeArtist
		}
		return strings.TrimRight(TruncateToByteBudget(res, maxBytes), ". ")
	}

	sep := " - "
	sepLen := len(sep) // 3 bytes
	avail := maxBytes - sepLen

	// Check if combined parts exceed budget
	if len(safeAlbum)+len(safeArtist) > avail {
		// Both parts need to share avail bytes fairly.
		half := avail / 2
		lenAlb := len(safeAlbum)
		lenArt := len(safeArtist)

		var albBudget, artBudget int
		if lenAlb > half && lenArt > half {
			albBudget = half
			artBudget = avail - albBudget
		} else if lenAlb <= half {
			albBudget = lenAlb
			artBudget = avail - albBudget
		} else {
			artBudget = lenArt
			albBudget = avail - artBudget
		}

		safeAlbum = strings.TrimRight(TruncateToByteBudget(safeAlbum, albBudget), ". ")
		safeArtist = strings.TrimRight(TruncateToByteBudget(safeArtist, artBudget), ". ")
	}

	var assembled string
	if safeAlbum != "" && safeArtist != "" {
		assembled = fmt.Sprintf("%s%s%s", safeAlbum, sep, safeArtist)
	} else if safeAlbum != "" {
		assembled = safeAlbum
	} else if safeArtist != "" {
		assembled = safeArtist
	} else {
		assembled = "Unknown Album - Unknown Artist"
	}

	// Enforce assembled full component byte budget
	assembled = strings.TrimRight(TruncateToByteBudget(assembled, maxBytes), ". ")
	if assembled == "" {
		assembled = strings.TrimRight(TruncateToByteBudget("Unknown Album - Unknown Artist", maxBytes), ". ")
	}
	return assembled
}

// ComputeAlbumDir calculates canonical album directory name: {Album} - {Album Artist}.
// Full component is bounded to MaxComponentBytes (255 bytes).
func ComputeAlbumDir(albumArtist, albumTitle string) string {
	return ComputeAlbumDirWithBudget(albumArtist, albumTitle, MaxComponentBytes)
}

// BuildTrackFilename formats a canonical track filename: "{TrackNumber:02d}. {CleanTitle}.mp3".
// It ensures that:
// - Duplicate case-insensitive .mp3 extensions are stripped from title.
// - The assembled filename does not exceed maxBytes (defaulting to MaxComponentBytes).
// - Sufficient room (MaxCompanionExtLen) is reserved so companion extensions (.jpeg, .webp, .lrc) also fit.
// - Truncation stops cleanly on rune boundaries without trailing dots/spaces before .mp3.
func BuildTrackFilename(trackNum int, title, fallback string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}

	if trackNum <= 0 {
		trackNum = 1
	}

	prefix := fmt.Sprintf("%02d. ", trackNum)
	ext := ".mp3"

	// Strip duplicate or case-insensitive .mp3 suffixes from title and fallback
	cleanTitle := strings.TrimSpace(title)
	for strings.HasSuffix(strings.ToLower(cleanTitle), ".mp3") {
		cleanTitle = strings.TrimSpace(cleanTitle[:len(cleanTitle)-4])
	}
	cleanFallback := strings.TrimSpace(fallback)
	for strings.HasSuffix(strings.ToLower(cleanFallback), ".mp3") {
		cleanFallback = strings.TrimSpace(cleanFallback[:len(cleanFallback)-4])
	}
	if cleanFallback == "" {
		cleanFallback = fmt.Sprintf("Track_%02d", trackNum)
	}

	// Budget calculation:
	// We ensure base filename + MaxCompanionExtLen <= maxBytes so companion files (.jpeg/.webp) also fit.
	maxBaseBytes := maxBytes - MaxCompanionExtLen
	if maxBaseBytes <= 0 {
		// Extremely small budget: truncate prefix+cleanTitle+ext directly to maxBytes
		return strings.TrimRight(TruncateToByteBudget(fmt.Sprintf("%s%s%s", prefix, cleanTitle, ext), maxBytes), ". ")
	}

	maxTitleBytes := maxBaseBytes - len(prefix)
	if maxTitleBytes < 1 {
		maxTitleBytes = 0
	}

	safeTitle := ""
	if maxTitleBytes > 0 {
		safeTitle = SanitizeComponentWithBudget(cleanTitle, cleanFallback, 180, maxTitleBytes)
		for strings.HasSuffix(strings.ToLower(safeTitle), ".mp3") {
			safeTitle = strings.TrimSpace(safeTitle[:len(safeTitle)-4])
		}
	}

	if safeTitle == "" && maxTitleBytes > 0 {
		safeTitle = SanitizeComponentWithBudget(cleanFallback, fmt.Sprintf("Track_%02d", trackNum), 180, maxTitleBytes)
		for strings.HasSuffix(strings.ToLower(safeTitle), ".mp3") {
			safeTitle = strings.TrimSpace(safeTitle[:len(safeTitle)-4])
		}
	}

	var filename string
	if safeTitle != "" {
		filename = fmt.Sprintf("%s%s%s", prefix, safeTitle, ext)
	} else if maxBytes >= len(prefix)+len(ext) {
		filename = fmt.Sprintf("%s%s", strings.TrimRight(prefix, " "), ext)
	} else {
		filename = strings.TrimRight(TruncateToByteBudget(fmt.Sprintf("%s%s", prefix, ext), maxBytes), ". ")
	}

	// Enforce maxBytes limit on assembled filename
	if len(filename) > maxBytes {
		filename = strings.TrimRight(TruncateToByteBudget(filename, maxBytes), ". ")
	}
	return filename
}

// ComputeTargetPathWithBudget calculates destination paths: {AlbumDir}/{TrackFilename}
// with explicit byte budget for path components.
func ComputeTargetPathWithBudget(albumArtist, albumTitle string, trackNum int, title, origFilename string, maxComponentBytes int) (albumDir, trackRelPath string) {
	cleanTitle := CleanTitle(title, origFilename)
	if trackNum <= 0 {
		trackNum = 1
	}

	artist := strings.TrimSpace(albumArtist)
	album := strings.TrimSpace(albumTitle)

	// Single track logic: if album is empty or equal to "Single"/"Singles", album = track title
	if album == "" || strings.EqualFold(album, "single") || strings.EqualFold(album, "singles") {
		if cleanTitle != "" {
			album = cleanTitle
		} else {
			album = fmt.Sprintf("Track_%02d", trackNum)
		}
	}

	albumDir = ComputeAlbumDirWithBudget(artist, album, maxComponentBytes)
	filename := BuildTrackFilename(trackNum, cleanTitle, fmt.Sprintf("Track_%02d", trackNum), maxComponentBytes)
	trackRelPath = filepath.Join(albumDir, filename)
	return albumDir, trackRelPath
}

// ComputeTargetPath calculates destination paths: {Album} - {Album Artist}/{TrackNumber:02d}. {CleanTitle}.mp3.
// If albumTitle is empty or indicates a single ("Single"/"Singles"), album is set to track title.
// All components are bounded to MaxComponentBytes (255 bytes).
func ComputeTargetPath(albumArtist, albumTitle string, trackNum int, title, origFilename string) (albumDir, trackRelPath string) {
	return ComputeTargetPathWithBudget(albumArtist, albumTitle, trackNum, title, origFilename, MaxComponentBytes)
}

// TruncateToByteBudget receiver method on Service.
func (s *Service) TruncateToByteBudget(str string, maxBytes int) string {
	return TruncateToByteBudget(str, maxBytes)
}

// SanitizeComponentWithBudget receiver method on Service.
func (s *Service) SanitizeComponentWithBudget(name, fallback string, maxRunes, maxBytes int) string {
	return SanitizeComponentWithBudget(name, fallback, maxRunes, maxBytes)
}

// SanitizeComponent receiver method on Service.
func (s *Service) SanitizeComponent(name, fallback string, maxLength int) string {
	return SanitizeComponent(name, fallback, maxLength)
}

// ComputeAlbumDirWithBudget receiver method on Service.
func (s *Service) ComputeAlbumDirWithBudget(albumArtist, albumTitle string, maxBytes int) string {
	return ComputeAlbumDirWithBudget(albumArtist, albumTitle, maxBytes)
}

// ComputeAlbumDir receiver method on Service.
func (s *Service) ComputeAlbumDir(albumArtist, albumTitle string) string {
	return ComputeAlbumDir(albumArtist, albumTitle)
}

// ComputeTargetPathWithBudget receiver method on Service.
func (s *Service) ComputeTargetPathWithBudget(albumArtist, albumTitle string, trackNum int, title, origFilename string, maxComponentBytes int) (albumDir, trackRelPath string) {
	return ComputeTargetPathWithBudget(albumArtist, albumTitle, trackNum, title, origFilename, maxComponentBytes)
}

// ComputeTargetPath receiver method on Service.
func (s *Service) ComputeTargetPath(albumArtist, albumTitle string, trackNum int, title, origFilename string) (albumDir, trackRelPath string) {
	return ComputeTargetPath(albumArtist, albumTitle, trackNum, title, origFilename)
}
