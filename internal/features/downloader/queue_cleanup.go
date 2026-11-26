package downloader

import (
	"context"
	"errors"
	"fmt"
	"log"

	"music-manager/internal/core/safefs"
)

// Durable internal statuses use the existing status column. Clients see ERROR
// with cleanup_pending and an actionable message, using the existing queue UI.
const (
	cleanupDelete JobStatus = "CLEANUP_DELETE"
	cleanupRetry  JobStatus = "CLEANUP_RETRY"
)

var errCleanupPending = errors.New("очистка временных файлов не завершена; сервер повторит её автоматически")

// All cleanup transitions are serialized with claims and debounced writes by jobsMu.
// Write the intent before touching files, including for memory-only compatibility jobs.
func (s *Service) beginCleanupLocked(ctx context.Context, job *Job, action JobStatus) error {
	draft := job.Snapshot()
	draft.CleanupPending = action
	draft.Status = StatusError
	message := errCleanupPending.Error()
	draft.Error, draft.CurrentStep = &message, message
	if s.repo != nil {
		err := s.repo.UpdateJobStatus(ctx, job.ID, action, message, &message)
		if errors.Is(err, ErrJobNotFound) {
			err = s.repo.CreateJob(ctx, draft)
		}
		if err != nil {
			return fmt.Errorf("%w: %w", errQueueStorage, err)
		}
	}
	job.applyQueueSnapshot(draft)
	if s.jobs[job.ID] != job {
		s.registerJobLocked(job)
	}
	job.Cancel()
	return nil
}

func (s *Service) completeCleanupLocked(ctx context.Context, job *Job) error {
	draft := job.Snapshot()
	if draft.CleanupPending != cleanupDelete && draft.CleanupPending != cleanupRetry {
		return errors.New("unknown cleanup action")
	}
	if draft.CleanupPending == cleanupRetry && draft.selectionCorrupt {
		return ErrCorruptJobSelection
	}
	if s.runningJobs[job.ID] {
		return errCleanupPending
	}
	if s.tmpSandbox == nil {
		return fmt.Errorf("%w: staging sandbox not configured", errCleanupPending)
	}
	dir, err := s.tmpSandbox.Resolve(job.ID, false)
	if err == nil {
		err = safefs.RemoveAll(dir)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", errCleanupPending, err)
	}
	switch draft.CleanupPending {
	case cleanupDelete:
		// Keep the durable intent until both filesystem and database work succeed.
		return s.deleteJobLocked(ctx, job.ID, false)
	case cleanupRetry:
		draft.CleanupPending = ""
		draft.resetForQueue("Задача перезапущена, ожидает очереди")
		if s.repo != nil {
			if err := s.repo.updateJobWithSelection(ctx, draft, cleanupRetry); err != nil {
				return fmt.Errorf("%w: %w", errQueueStorage, err)
			}
		}
		job.applyQueueSnapshot(draft)
		s.wakeWorker()
		return nil
	default:
		return errors.New("unknown cleanup action")
	}
}

func (s *Service) retryPendingCleanups(ctx context.Context) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	for _, job := range s.jobs {
		if job.Snapshot().CleanupPending != "" {
			if err := s.completeCleanupLocked(ctx, job); err != nil {
				log.Printf("[Downloader] cleanup %s: %v", job.ID, err)
			}
		}
	}
}

func (s *Service) finishJobExecution(ctx context.Context, job *Job) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	delete(s.runningJobs, job.ID)
	if job.Snapshot().CleanupPending != "" {
		if err := s.completeCleanupLocked(ctx, job); err != nil {
			log.Printf("[Downloader] cleanup after worker %s: %v", job.ID, err)
		}
	}
}
