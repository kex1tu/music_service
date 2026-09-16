package organizer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// FileSHA256 calculates SHA-256 checksum of a file.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var companionExtensions = []string{".lrc", ".txt", ".jpg", ".jpeg", ".png", ".webp"}

// MoveTrackWithCompanions moves an audio track and any accompanying lyrics (.lrc, .txt) and images (.jpg, .png).
// If destMP3 already exists and overwrite is false:
// - If identical (size and SHA-256 match), srcMP3 is safely removed (deduplication) and destMP3 is returned.
// - If different, a unique canonical path "{base} ({N}).mp3" is selected.
// Any companion files are moved along with the matching suffix.
func MoveTrackWithCompanions(srcMP3, destMP3 string, overwrite bool) (string, error) {
	if srcMP3 == destMP3 {
		return destMP3, nil
	}
	if !fileExists(srcMP3) {
		return "", fmt.Errorf("source track does not exist: %s", srcMP3)
	}

	targetMP3 := destMP3

	if fileExists(targetMP3) && !overwrite {
		// Compare size and hash
		srcStat, err1 := os.Stat(srcMP3)
		destStat, err2 := os.Stat(targetMP3)
		if err1 == nil && err2 == nil && srcStat.Size() == destStat.Size() {
			srcHash, hErr1 := FileSHA256(srcMP3)
			destHash, hErr2 := FileSHA256(targetMP3)
			if hErr1 == nil && hErr2 == nil && srcHash == destHash {
				// Files are identical! Deduplicate: remove source
				_ = os.Remove(srcMP3)
				cleanCompanionFiles(srcMP3, targetMP3)
				return targetMP3, nil
			}
		}

		// Files differ or cannot be compared: find unique suffixed name
		dir := filepath.Dir(destMP3)
		ext := filepath.Ext(destMP3)
		base := strings.TrimSuffix(filepath.Base(destMP3), ext)

		found := false
		for n := 1; n < 1000; n++ {
			candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, n, ext))
			if !fileExists(candidate) {
				targetMP3 = candidate
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("failed to generate unique filename for %s", destMP3)
		}
	}

	// Ensure destination directory exists
	_ = os.MkdirAll(filepath.Dir(targetMP3), 0755)

	if err := moveFile(srcMP3, targetMP3); err != nil {
		return "", fmt.Errorf("failed to move audio file %s to %s: %w", srcMP3, targetMP3, err)
	}

	// Move companion files (.lrc, .txt, .jpg, .png, etc.)
	moveCompanionFiles(srcMP3, targetMP3)

	return targetMP3, nil
}

func cleanCompanionFiles(srcAudio, destAudio string) {
	srcBase := strings.TrimSuffix(srcAudio, filepath.Ext(srcAudio))
	destBase := strings.TrimSuffix(destAudio, filepath.Ext(destAudio))

	for _, ext := range companionExtensions {
		srcComp := srcBase + ext
		destComp := destBase + ext
		if fileExists(srcComp) {
			if fileExists(destComp) {
				_ = os.Remove(srcComp)
			} else {
				_ = moveFile(srcComp, destComp)
			}
		}
	}
}

func moveCompanionFiles(srcAudio, destAudio string) {
	srcBase := strings.TrimSuffix(srcAudio, filepath.Ext(srcAudio))
	destBase := strings.TrimSuffix(destAudio, filepath.Ext(destAudio))

	for _, ext := range companionExtensions {
		srcComp := srcBase + ext
		destComp := destBase + ext
		if fileExists(srcComp) && srcComp != destComp {
			_ = moveFile(srcComp, destComp)
		}
	}
}
