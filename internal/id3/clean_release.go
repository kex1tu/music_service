package id3

import (
	"regexp"
	"strings"
)

var (
	deluxeRemasterRegex = regexp.MustCompile(`(?i)\s*[\(\[](?:[^\)\]]*\b)?(?:deluxe|expanded|remaster(?:ed)?|special|bonus|anniversary|reissue|collectors?|edition|version|ep|single|official|explicit)[^\)\]]*[\)\]]`)
	cleanupPunctRegex   = regexp.MustCompile(`[^\p{L}\p{N}\s]`)
)

// CleanReleaseTitle normalizes a release or album title by stripping deluxe/remaster/single/ep suffixes,
// punctuation, and extra whitespace, converting to lowercase.
func CleanReleaseTitle(title string) string {
	cleaned := deluxeRemasterRegex.ReplaceAllString(title, "")
	cleaned = cleanupPunctRegex.ReplaceAllString(cleaned, " ")
	return strings.ToLower(strings.Join(strings.Fields(cleaned), " "))
}

// IsDeluxeOrRemaster checks if a title contains deluxe, remaster, or special edition markers.
func IsDeluxeOrRemaster(title string) bool {
	return deluxeRemasterRegex.MatchString(title)
}
