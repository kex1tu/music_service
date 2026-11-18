package organizer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
)

// MergeDuplicateGroup keeps the first track. A group with differing audio or
// sidecars is left intact. Returned paths identify the audio files actually removed.
func MergeDuplicateGroup(sb *sandbox.PathSandbox, paths []string) (removed []string, err error) {
	if len(paths) < 2 {
		return nil, nil
	}
	if sb == nil {
		return nil, errors.New("music sandbox not initialized")
	}

	dir := filepath.Dir(paths[0])
	lock := getAlbumLock(dir)
	lock.Lock()
	defer lock.Unlock()

	fullPaths := make([]string, 0, len(paths))
	seen := make(map[string]bool)
	companionHashes := make(map[string]string)
	var audioHash string
	for i, rel := range paths {
		full, err := sb.Resolve(rel, false)
		if err != nil {
			return nil, err
		}
		if filepath.Dir(rel) != dir || seen[full] || !strings.EqualFold(filepath.Ext(full), ".mp3") {
			return nil, fmt.Errorf("invalid duplicate group path: %s", rel)
		}
		seen[full] = true
		fullPaths = append(fullPaths, full)

		hash, err := regularFileSHA256(full)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			audioHash = hash
		} else if hash != audioHash {
			return nil, nil
		}

		base := strings.TrimSuffix(rel, filepath.Ext(rel))
		for _, ext := range companionExtensions {
			sidecar, err := sb.Resolve(base+ext, false)
			if err != nil {
				return nil, err
			}
			hash, err := regularFileSHA256(sidecar)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if previous, ok := companionHashes[ext]; ok && previous != hash {
				return nil, nil
			}
			companionHashes[ext] = hash
		}
	}

	for i, full := range fullPaths[1:] {
		if err := cleanCompanionFiles(full, fullPaths[0]); err != nil {
			return removed, err
		}
		if err := safefs.Remove(full); err != nil {
			return removed, err
		}
		removed = append(removed, paths[i+1])
	}
	return removed, nil
}

func regularFileSHA256(path string) (string, error) {
	info, err := safefs.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("expected a regular file: %s", path)
	}
	return FileSHA256(path)
}
