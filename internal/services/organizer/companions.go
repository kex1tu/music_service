package organizer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
)

// FileSHA256 calculates SHA-256 checksum of a file.
func FileSHA256(path string) (string, error) {
	f, err := safefs.Open(path)
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

func maxCompanionExtLen(primaryExt string) int {
	maxLen := len(primaryExt)
	for _, ext := range companionExtensions {
		if len(ext) > maxLen {
			maxLen = len(ext)
		}
	}
	return maxLen
}

// MoveTrackWithCompanions moves an audio track and any accompanying lyrics (.lrc, .txt) and images (.jpg, .png).
// If destMP3 already exists and overwrite is false:
// - If identical (size and SHA-256 match), srcMP3 is safely removed (deduplication) and destMP3 is returned.
// - If different, a unique canonical path "{base} ({N}).mp3" is selected.
// Any companion files are moved along with the matching suffix.
func MoveTrackWithCompanions(srcMP3, destMP3 string, overwrite bool) (string, error) {
	return moveTrackWithCompanions(srcMP3, destMP3, overwrite, func(target string, _ bool, move func() error) (string, error) {
		if err := move(); err != nil {
			return "", err
		}
		return target, nil
	})
}

func moveTrackWithCompanions(srcMP3, destMP3 string, overwrite bool, apply func(string, bool, func() error) (string, error)) (string, error) {
	if srcMP3 == destMP3 {
		return apply(destMP3, false, func() error { return nil })
	}
	if !fileExists(srcMP3) {
		return "", fmt.Errorf("source track does not exist: %s", srcMP3)
	}

	targetMP3 := destMP3
	baseName := filepath.Base(targetMP3)
	if len(baseName) > MaxComponentBytes {
		dir := filepath.Dir(targetMP3)
		ext := filepath.Ext(targetMP3)
		maxExt := maxCompanionExtLen(ext)
		base := strings.TrimSuffix(baseName, ext)
		maxBaseBytes := MaxComponentBytes - maxExt
		candBase := strings.TrimRight(TruncateToByteBudget(base, maxBaseBytes), ". ")
		if candBase == "" {
			candBase = "Track"
		}
		targetMP3 = filepath.Join(dir, candBase+ext)
	}

	if fileExists(targetMP3) && !overwrite {
		if sameTrackFiles(srcMP3, targetMP3) && companionsCompatible(srcMP3, targetMP3) {
			return apply(targetMP3, true, func() error {
				// Recheck under the index lock before removing the source.
				if !sameTrackFiles(srcMP3, targetMP3) {
					return fmt.Errorf("duplicate track changed before merge: %s", srcMP3)
				}
				if err := cleanCompanionFiles(srcMP3, targetMP3); err != nil {
					return err
				}
				return safefs.Remove(srcMP3)
			})
		}

		// Files differ or cannot be compared: find unique suffixed name
		dir := filepath.Dir(destMP3)
		ext := filepath.Ext(destMP3)
		base := strings.TrimSuffix(filepath.Base(destMP3), ext)
		maxExt := maxCompanionExtLen(ext)

		found := false
		for n := 1; n < 1000; n++ {
			suffix := fmt.Sprintf(" (%d)", n)
			// Recalculate byte budget so base + suffix + longest companion extension <= MaxComponentBytes (255)
			maxBaseBytes := MaxComponentBytes - len(suffix) - maxExt
			if maxBaseBytes <= 0 {
				maxBaseBytes = 1
			}
			candBase := strings.TrimRight(TruncateToByteBudget(base, maxBaseBytes), ". ")
			if candBase == "" {
				candBase = "Track"
				candBase = strings.TrimRight(TruncateToByteBudget(candBase, maxBaseBytes), ". ")
			}
			candidate := filepath.Join(dir, fmt.Sprintf("%s%s%s", candBase, suffix, ext))
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

	return apply(targetMP3, false, func() error {
		// The collision check preceded the index lock. A concurrent move may
		// have claimed this path while we waited to record the intent.
		if !overwrite && fileExists(targetMP3) {
			return fmt.Errorf("destination track already exists: %s", targetMP3)
		}
		// Ensure destination directory exists
		if err := safefs.MkdirAll(filepath.Dir(targetMP3), 0755); err != nil {
			return err
		}

		if err := moveFile(srcMP3, targetMP3); err != nil {
			return fmt.Errorf("failed to move audio file %s to %s: %w", srcMP3, targetMP3, err)
		}

		// Move companion files (.lrc, .txt, .jpg, .png, etc.)
		if err := moveCompanionFiles(srcMP3, targetMP3, false); err != nil {
			rollback := moveFile(targetMP3, srcMP3)
			return errors.Join(err, rollback)
		}

		return nil
	})
}

func sameTrackFiles(src, dest string) bool {
	sourceInfo, sourceErr := safefs.Stat(src)
	destInfo, destErr := safefs.Stat(dest)
	if sourceErr != nil || destErr != nil || sourceInfo.Size() != destInfo.Size() {
		return false
	}
	sourceHash, sourceErr := FileSHA256(src)
	destHash, destErr := FileSHA256(dest)
	return sourceErr == nil && destErr == nil && sourceHash == destHash
}

func cleanCompanionFiles(srcAudio, destAudio string) error {
	srcBase := strings.TrimSuffix(srcAudio, filepath.Ext(srcAudio))
	destBase := strings.TrimSuffix(destAudio, filepath.Ext(destAudio))

	if srcBase == destBase {
		return nil
	}

	for _, ext := range companionExtensions {
		srcComp := srcBase + ext
		destComp := destBase + ext
		if fileExists(srcComp) {
			if fileExists(destComp) {
				a, e1 := FileSHA256(srcComp)
				b, e2 := FileSHA256(destComp)
				if e1 != nil || e2 != nil {
					return errors.Join(e1, e2)
				}
				if a != b {
					return fmt.Errorf("companion conflict: %s", destComp)
				}
				if err := safefs.Remove(srcComp); err != nil {
					return err
				}
			} else {
				if err := moveFile(srcComp, destComp); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func moveCompanionFiles(srcAudio, destAudio string, resume bool) error {
	var moved [][2]string
	srcBase := strings.TrimSuffix(srcAudio, filepath.Ext(srcAudio))
	destBase := strings.TrimSuffix(destAudio, filepath.Ext(destAudio))

	for _, ext := range companionExtensions {
		srcComp := srcBase + ext
		destComp := destBase + ext
		if srcComp == destComp {
			continue
		}
		info, moveErr := safefs.Lstat(srcComp)
		if errors.Is(moveErr, os.ErrNotExist) || (moveErr == nil && info.IsDir()) {
			continue
		}
		if moveErr == nil && !info.Mode().IsRegular() {
			moveErr = fmt.Errorf("companion is not a regular file: %s", srcComp)
		}
		if moveErr == nil {
			if destInfo, err := safefs.Lstat(destComp); err == nil {
				if resume && destInfo.Mode().IsRegular() && sameTrackFiles(srcComp, destComp) {
					// A cross-filesystem move can be interrupted after publishing
					// the complete destination and before deleting the source.
					if moveErr = safefs.Remove(srcComp); moveErr == nil {
						continue
					}
				} else {
					moveErr = fmt.Errorf("companion destination already exists: %s", destComp)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				moveErr = err
			}
		}
		if moveErr == nil {
			moveErr = moveFile(srcComp, destComp)
		}
		if moveErr != nil {
			errs := []error{moveErr}
			for i := len(moved) - 1; i >= 0; i-- {
				errs = append(errs, moveFile(moved[i][1], moved[i][0]))
			}
			return errors.Join(errs...)
		}
		moved = append(moved, [2]string{srcComp, destComp})
	}
	return nil
}

// Never deduplicate audio when doing so would discard a different sidecar.
func companionsCompatible(srcAudio, dstAudio string) bool {
	src := strings.TrimSuffix(srcAudio, filepath.Ext(srcAudio))
	dst := strings.TrimSuffix(dstAudio, filepath.Ext(dstAudio))
	for _, ext := range companionExtensions {
		if fileExists(src+ext) && fileExists(dst+ext) {
			a, e1 := FileSHA256(src + ext)
			b, e2 := FileSHA256(dst + ext)
			if e1 != nil || e2 != nil || a != b {
				return false
			}
		}
	}
	return true
}
