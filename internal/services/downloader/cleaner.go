package downloader

import (
	"os"
	"time"
)

func (s *Service) startCleaner(maxAge time.Duration) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		s.cleanOldTempDirs(maxAge)
	}
}

func (s *Service) cleanOldTempDirs(maxAge time.Duration) {
	root := s.tmpSandbox.Root()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirPath, err := s.tmpSandbox.Resolve(entry.Name(), false)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(dirPath)
		}
	}
}
