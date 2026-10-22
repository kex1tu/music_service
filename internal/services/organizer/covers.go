package organizer

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"

	"music-manager/internal/id3"
)

func hasCoverInDir(dir string) bool {
	for _, name := range []string{"cover.jpg", "cover.png", "cover.jpeg", "folder.jpg", "folder.png"} {
		if fileExists(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}

func extractCoverFromTracks(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".mp3") {
			continue
		}
		mp3Path := filepath.Join(dir, e.Name())
		cover, err := id3.ReadCover(mp3Path)
		if err == nil && cover != nil && len(cover.Data) > 0 {
			ext := ".jpg"
			if cover.MimeType == "image/png" || bytes.HasPrefix(cover.Data, []byte("\x89PNG")) {
				ext = ".png"
			}
			coverPath := filepath.Join(dir, "cover"+ext)
			if !fileExists(coverPath) {
				_ = os.WriteFile(coverPath, cover.Data, 0644)
			}
			trackCoverPath := filepath.Join(dir, strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))+ext)
			if !fileExists(trackCoverPath) {
				_ = os.WriteFile(trackCoverPath, cover.Data, 0644)
			}
			return
		}
	}
}

func syncTrackNamedCovers(dir string) {
	// Find directory-level cover file (cover.jpg, cover.png, folder.jpg, etc.)
	var dirCoverPath string
	for _, name := range []string{"cover.jpg", "cover.png", "cover.jpeg", "folder.jpg", "folder.png"} {
		p := filepath.Join(dir, name)
		if fileExists(p) {
			dirCoverPath = p
			break
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	// For every MP3 track in the directory:
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".mp3") {
			continue
		}
		trackBase := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))

		// Check if track-specific cover exists
		hasTrackCover := false
		var existingTrackCover string
		for _, imgExt := range []string{".jpg", ".jpeg", ".png", ".webp"} {
			tp := filepath.Join(dir, trackBase+imgExt)
			if fileExists(tp) {
				hasTrackCover = true
				existingTrackCover = tp
				break
			}
		}

		if !hasTrackCover {
			if dirCoverPath != "" {
				covExt := filepath.Ext(dirCoverPath)
				_ = copyFile(dirCoverPath, filepath.Join(dir, trackBase+covExt))
			} else {
				mp3Path := filepath.Join(dir, e.Name())
				cover, err := id3.ReadCover(mp3Path)
				if err == nil && cover != nil && len(cover.Data) > 0 {
					ext := ".jpg"
					if cover.MimeType == "image/png" || bytes.HasPrefix(cover.Data, []byte("\x89PNG")) {
						ext = ".png"
					}
					_ = os.WriteFile(filepath.Join(dir, trackBase+ext), cover.Data, 0644)
					if dirCoverPath == "" {
						dirCoverPath = filepath.Join(dir, "cover"+ext)
						_ = os.WriteFile(dirCoverPath, cover.Data, 0644)
					}
				}
			}
		} else if dirCoverPath == "" && existingTrackCover != "" {
			covExt := filepath.Ext(existingTrackCover)
			dirCoverPath = filepath.Join(dir, "cover"+covExt)
			_ = copyFile(existingTrackCover, dirCoverPath)
		}
	}
}

func (s *Service) moveSourceCover(sourceDir, targetFullDir string) {
	if sourceDir == "" {
		return
	}
	targetCover := filepath.Join(targetFullDir, "cover.jpg")
	if fileExists(targetCover) {
		return
	}

	var candidates []string
	if s.tmpSandbox != nil {
		cleanRel := strings.TrimPrefix(sourceDir, "tmp_downloads/")
		cleanRel = strings.TrimPrefix(cleanRel, "tmp/")
		if p, err := s.tmpSandbox.Resolve(cleanRel, false); err == nil {
			candidates = append(candidates,
				filepath.Join(p, "cover.jpg"),
				filepath.Join(p, "cover.png"),
				filepath.Join(p, "cover.jpeg"),
				filepath.Join(p, "folder.jpg"),
				filepath.Join(p, "folder.png"),
				filepath.Join(p, "raw", "cover.jpg"),
				filepath.Join(p, "raw", "cover.png"),
				filepath.Join(p, "converted", "cover.jpg"),
				filepath.Join(p, "converted", "cover.png"),
			)
		}
	}
	if p, err := s.musicSandbox.Resolve(sourceDir, false); err == nil {
		candidates = append(candidates,
			filepath.Join(p, "cover.jpg"),
			filepath.Join(p, "cover.png"),
			filepath.Join(p, "cover.jpeg"),
			filepath.Join(p, "folder.jpg"),
			filepath.Join(p, "folder.png"),
		)
	}

	for _, c := range candidates {
		if fileExists(c) && !fileExists(targetCover) {
			_ = copyFile(c, targetCover)
			break
		}
	}
}

func copyCoverFromSources(sourceDirs map[string]bool, targetFullDir string) {
	targetCover := filepath.Join(targetFullDir, "cover.jpg")
	if fileExists(targetCover) {
		return
	}

	candidates := []string{
		"cover.jpg", "cover.png", "cover.jpeg",
		"folder.jpg", "folder.png", "folder.jpeg",
		filepath.Join("raw", "cover.jpg"), filepath.Join("raw", "cover.png"),
		filepath.Join("converted", "cover.jpg"), filepath.Join("converted", "cover.png"),
	}

	for sDir := range sourceDirs {
		for _, cand := range candidates {
			candPath := filepath.Join(sDir, cand)
			if fileExists(candPath) && !fileExists(targetCover) {
				_ = copyFile(candPath, targetCover)
				return
			}
		}
	}
}

func saveBase64Cover(targetCoverPath string, base64Data string) error {
	if strings.TrimSpace(base64Data) == "" {
		return nil
	}
	parts := strings.SplitN(base64Data, ",", 2)
	raw := base64Data
	if len(parts) == 2 {
		raw = parts[1]
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return os.WriteFile(targetCoverPath, data, 0644)
}
