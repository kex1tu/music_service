package downloader

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"music-manager/internal/services/downloadstaging"
)

// Start запускает воркер-пул, фоновую очистку и сбрасывает зависшие задачи в БД.
func (s *Service) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.ctx, s.cancel = context.WithCancel(ctx)

	if s.repo != nil {
		if _, err := s.repo.ResetInterruptedJobs(s.ctx); err != nil {
			s.cancel()
			return fmt.Errorf("reset interrupted downloads: %w", err)
		}
		savedJobs, err := s.repo.GetAllJobs(s.ctx)
		if err != nil {
			s.cancel()
			return fmt.Errorf("load download jobs: %w", err)
		}
		for _, job := range savedJobs {
			if job.CleanupPending != "" {
				continue
			}
			recovered, err := s.recoverLegacyStaging(job)
			if err != nil {
				log.Printf("[Downloader] recover staged files for %s: %v", job.ID, err)
				job.Tracks = nil
				job.SetError("Не удалось прочитать готовые файлы старой задачи")
				recovered = true
			}
			if !recovered {
				continue
			}
			if job.legacySelection {
				err = s.repo.UpdateJobStatus(s.ctx, job.ID, job.Status, job.CurrentStep, job.Error)
			} else {
				err = s.repo.SaveJob(s.ctx, job)
			}
			if err != nil {
				s.cancel()
				return fmt.Errorf("save recovered download %s: %w", job.ID, err)
			}
		}
		s.jobsMu.Lock()
		for _, job := range savedJobs {
			s.registerJobLocked(job)
		}
		s.jobsMu.Unlock()
		s.retryPendingCleanups(s.ctx)
	}

	// 2. Запуск фиксированного пула воркеров
	for i := 0; i < s.workersCount; i++ {
		s.wg.Add(1)
		go s.workerLoop(s.ctx)
	}

	// 3. Запуск демона очистки старых временных директорий
	s.wg.Add(1)
	go s.cleanerLoop(s.ctx)

	return nil
}

// Stop выполняет graceful shutdown: отменяет контекст, пробуждает воркеры и ожидает их завершения.
func (s *Service) Stop(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&s.stopped, 0, 1) {
		return nil
	}

	if s.cancel != nil {
		s.cancel()
	}

	// Ожидаем завершения всех горутин пула
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		return fmt.Errorf("downloader: stop timed out: %w", ctx.Err())
	}

	// Останавливаем все висящие таймеры дебаунса и сбрасываем состояние
	s.timersMu.Lock()
	for id, tm := range s.syncTimers {
		tm.Stop()
		delete(s.syncTimers, id)
	}
	s.timersMu.Unlock()

	return nil
}

// Close реализует io.Closer для совместимости с defer.
func (s *Service) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.Stop(ctx)
}

func (s *Service) workerLoop(ctx context.Context) {
	defer s.wg.Done()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()

	for ctx.Err() == nil {
		job, jobCtx, cancel, err := s.claimNextJob(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("[Downloader] failed to claim queued job: %v", err)
		}
		if job != nil {
			s.wakeWorker()
			if !job.Snapshot().selectionCorrupt {
				s.executeJob(jobCtx, job)
			}
			cancel()
			s.finishJobExecution(ctx, job)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		case <-s.wake:
		}
	}
}

func (s *Service) wakeWorker() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) claimNextJob(ctx context.Context) (*Job, context.Context, context.CancelFunc, error) {
	s.pauseMu.RLock()
	defer s.pauseMu.RUnlock()
	if s.isPaused {
		return nil, nil, nil, nil
	}

	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()

	var job *Job
	if s.repo != nil {
		claimed, err := s.repo.ClaimNextJob(ctx)
		if err != nil || claimed == nil {
			return nil, nil, nil, err
		}
		job = s.jobs[claimed.ID]
		if job == nil {
			job = claimed
			s.registerJobLocked(job)
		} else {
			job.applyQueueSnapshot(claimed)
		}
	} else {
		for _, candidate := range s.jobs {
			draft := candidate.Snapshot()
			if draft.Status == StatusQueued {
				draft.Status = StatusResolving
				candidate.applyQueueSnapshot(draft)
				job = candidate
				break
			}
		}
		if job == nil {
			return nil, nil, nil, nil
		}
	}

	jobCtx, cancel := context.WithCancel(ctx)
	job.SetCancelFunc(cancel)
	s.runningJobs[job.ID] = true
	return job, jobCtx, cancel, nil
}

func (s *Service) cleanerLoop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(s.cleanerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.retryPendingCleanups(ctx)
			s.cleanOldTempDirs()
		}
	}
}

func (s *Service) cleanOldTempDirs() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	downloadstaging.CleanOldDirs(s.tmpSandbox, s.cleanerMaxAge, func(id string) bool {
		s.jobsMu.RLock()
		job := s.jobs[id]
		s.jobsMu.RUnlock()
		if job != nil && job.Snapshot().Status != StatusCancelled {
			return false
		}
		if s.repo == nil {
			return true
		}
		stored, err := s.repo.GetJob(ctx, id)
		return err == nil && (stored == nil || stored.Status == StatusCancelled)
	})
}
