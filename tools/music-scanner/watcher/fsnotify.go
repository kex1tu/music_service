package watcher

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type FSWatcher struct {
	watcher     *fsnotify.Watcher
	debounce    time.Duration
	timers      map[string]*time.Timer
	timersMu    sync.Mutex
	triggerChan chan string
}

func NewFSWatcher(debounce time.Duration, triggerChan chan string) (*FSWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	return &FSWatcher{
		watcher:     w,
		debounce:    debounce,
		timers:      make(map[string]*time.Timer),
		triggerChan: triggerChan,
	}, nil
}

// AddRecursive добавляет директорию и все ее поддиректории в наблюдение.
func (fw *FSWatcher) AddRecursive(rootDir string) error {
	return filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Пропускаем ошибки доступа к отдельным папкам
		}
		if d.IsDir() {
			if err := fw.watcher.Add(path); err != nil {
				return err
			}
		}
		return nil
	})
}

// Start запускает цикл прослушивания файловых событий с дебаунсингом.
func (fw *FSWatcher) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			_ = fw.watcher.Close()
			return

		case event, ok := <-fw.watcher.Events:
			if !ok {
				return
			}

			// Если создана новая директория — добавляем ее в наблюдение
			if event.Has(fsnotify.Create) {
				if fi, err := os.Stat(event.Name); err == nil && fi.IsDir() {
					_ = fw.AddRecursive(event.Name)
					continue
				}
			}

			// Фильтруем интересующие расширения (музыка, тексты, обложки)
			ext := strings.ToLower(filepath.Ext(event.Name))
			if ext != ".mp3" && ext != ".lrc" && ext != ".txt" && ext != ".jpg" && ext != ".png" {
				continue
			}

			fw.debounceEvent(event.Name)

		case err, ok := <-fw.watcher.Errors:
			if !ok {
				return
			}
			_ = err // Ошибки файловой системы логируются или игнорируются
		}
	}
}

func (fw *FSWatcher) debounceEvent(filePath string) {
	fw.timersMu.Lock()
	defer fw.timersMu.Unlock()

	if t, exists := fw.timers[filePath]; exists {
		t.Stop()
	}

	fw.timers[filePath] = time.AfterFunc(fw.debounce, func() {
		fw.timersMu.Lock()
		delete(fw.timers, filePath)
		fw.timersMu.Unlock()

		// Передаем путь изменившегося файла
		select {
		case fw.triggerChan <- filePath:
		default:
		}
	})
}
