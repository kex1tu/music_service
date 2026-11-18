package downloader

import (
	"context"
	"time"

	"music-manager/internal/services/downloadstaging"
)

func (s *Service) startCleaner(ctx context.Context, maxAge time.Duration) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanOldTempDirs(maxAge)
		}
	}
}

func (s *Service) cleanOldTempDirs(maxAge time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	downloadstaging.CleanOldDirs(s.tmpSandbox, maxAge, func(id string) bool {
		if s.jobs == nil {
			return true
		}
		if job, exists := s.jobs.Get(id); exists && job.Snapshot().Status != StatusCancelled {
			return false
		}
		if database := s.jobs.Database(); database != nil {
			job, err := database.GetJob(ctx, id)
			return err == nil && (job == nil || job.Status == string(StatusCancelled))
		}
		return true
	})
}
