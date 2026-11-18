package downloader

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sync/atomic"
	"time"

	"music-manager/internal/core/safefs"
)

// Start запускает воркер-пул, фоновую очистку и сбрасывает зависшие задачи в БД.
func (s *Service) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.ctx, s.cancel = context.WithCancel(ctx)

	// 1. Сброс прерванных задач в SQLite
	if s.repo != nil {
		if _, err := s.repo.ResetInterruptedJobs(s.ctx); err != nil {
			log.Printf("[Downloader] Warning: failed to reset interrupted jobs: %v", err)
		}
		// Загрузка сохраненных задач в память
		savedJobs, err := s.repo.GetAllJobs(s.ctx)
		if err == nil {
			s.jobsMu.Lock()
			for _, j := range savedJobs {
				st := j.Status
				curStep := j.CurrentStep
				if st == StatusQueued || st == StatusResolving || st == StatusDownloading || st == StatusConverting || st == StatusTagging {
					st = StatusError
					curStep = "Прервано перезапуском сервера. Нажмите «Повторить»"
					j.Status = st
					j.CurrentStep = curStep
				}
				s.jobs[j.ID] = j
				j.SetOnUpdate(func() {
					s.syncJobDebounced(j)
				})
			}
			s.jobsMu.Unlock()
		}
	}

	// 2. Запуск фиксированного пула воркеров
	for i := 0; i < s.workersCount; i++ {
		s.wg.Add(1)
		go s.workerLoop(s.ctx, i)
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

	s.pauseMu.Lock()
	s.isPaused = false
	s.pauseMu.Unlock()
	if s.pauseCond != nil {
		s.pauseCond.Broadcast()
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

func (s *Service) workerLoop(ctx context.Context, workerID int) {
	defer s.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case jobID, ok := <-s.workQueue:
			if !ok {
				return
			}
			if ctx.Err() != nil {
				return
			}
			if err := s.waitIfPaused(ctx); err != nil {
				return
			}
			s.executeJob(ctx, jobID)
		}
	}
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
			s.cleanOldTempDirs()
		}
	}
}

func (s *Service) cleanOldTempDirs() {
	if s.tmpSandbox == nil {
		return
	}
	root := s.tmpSandbox.Root()
	entries, err := safefs.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-s.cleanerMaxAge)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			dirPath := filepath.Join(root, entry.Name())
			_ = safefs.RemoveAll(dirPath)
		}
	}
}
