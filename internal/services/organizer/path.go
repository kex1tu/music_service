package organizer

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"music-manager/internal/id3"
)

var (
	// dangerousFsChars matches filesystem forbidden chars: / \ : * ? " < > | \x00 and ASCII control codes (0-31)
	dangerousFsChars = regexp.MustCompile(`[\x00-\x1f/\\:*?"<>|]`)
	multiSepRegex    = regexp.MustCompile(`(\\+|\x00)`)
)

// SanitizeComponent sanitizes a directory or filename component:
// - Removes illegal filesystem characters (/, \, :, *, ?, ", <, >, |, \x00, control ASCII 0-31).
// - Replaces multiple separators (\\, \x00) with " & ".
// - Restricts length to maxLength (default 180) with clean rune boundary slicing.
// - Removes trailing dots and spaces to prevent Windows/crossplatform issues.
func SanitizeComponent(name, fallback string, maxLength int) string {
	if maxLength <= 0 {
		maxLength = 180
	}
	clean := strings.TrimSpace(name)
	if clean == "" {
		return fallback
	}

	// 1. Replace artist/genre multi-value separators
	clean = multiSepRegex.ReplaceAllString(clean, " & ")

	// 2. Replace illegal filesystem and control characters with underscore
	clean = dangerousFsChars.ReplaceAllString(clean, "_")

	// 3. Trim leading/trailing dots and spaces
	clean = strings.Trim(clean, ". ")

	// 4. Truncate safely by rune count without breaking UTF-8 sequences
	if utf8.RuneCountInString(clean) > maxLength {
		runes := []rune(clean)
		clean = strings.TrimRight(string(runes[:maxLength]), ". ")
	}

	if clean == "" {
		return fallback
	}
	return clean
}

// CleanTitle removes duplicate leading track number prefixes (e.g. "01. 01. Song" -> "Song").
func CleanTitle(title string, origFilename string) string {
	return id3.CleanTitle(title, origFilename)
}

// ComputeAlbumDir calculates canonical album directory name: {Album} - {Album Artist}.
func ComputeAlbumDir(albumArtist, albumTitle string) string {
	artist := strings.TrimSpace(albumArtist)
	album := strings.TrimSpace(albumTitle)

	if artist == "" {
		artist = "Unknown Artist"
	}
	if album == "" {
		album = "Unknown Album"
	}

	safeAlbum := SanitizeComponent(album, "Unknown Album", 100)
	safeArtist := SanitizeComponent(artist, "Unknown Artist", 100)

	return fmt.Sprintf("%s - %s", safeAlbum, safeArtist)
}

// ComputeTargetPath calculates destination paths: {Album} - {Album Artist}/{TrackNumber:02d}. {CleanTitle}.mp3.
// If albumTitle is empty or indicates a single ("Single"/"Singles"), album is set to track title.
func ComputeTargetPath(albumArtist, albumTitle string, trackNum int, title, origFilename string) (albumDir, trackRelPath string) {
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

	albumDir = ComputeAlbumDir(artist, album)
	safeTitle := SanitizeComponent(cleanTitle, fmt.Sprintf("Track_%02d", trackNum), 180)
	safeTitle = strings.TrimSuffix(safeTitle, ".mp3")
	safeTitle = strings.TrimSuffix(safeTitle, ".MP3")
	if safeTitle == "" {
		safeTitle = fmt.Sprintf("Track_%02d", trackNum)
	}

	filename := fmt.Sprintf("%02d. %s.mp3", trackNum, safeTitle)
	trackRelPath = filepath.Join(albumDir, filename)
	return albumDir, trackRelPath
}

// SanitizeComponent receiver method on Service.
func (s *Service) SanitizeComponent(name, fallback string, maxLength int) string {
	return SanitizeComponent(name, fallback, maxLength)
}

// ComputeAlbumDir receiver method on Service.
func (s *Service) ComputeAlbumDir(albumArtist, albumTitle string) string {
	return ComputeAlbumDir(albumArtist, albumTitle)
}

// ComputeTargetPath receiver method on Service.
func (s *Service) ComputeTargetPath(albumArtist, albumTitle string, trackNum int, title, origFilename string) (albumDir, trackRelPath string) {
	return ComputeTargetPath(albumArtist, albumTitle, trackNum, title, origFilename)
}
