package scanner

import (
	"sync"

	"music-scanner/id3"
)

// StartWorkerPool запускает пул горутин для параллельного парсинга MP3-файлов.
// После завершения всех воркеров канал out закрывается.
func StartWorkerPool(rootDir string, workers int, in <-chan string, out chan<- id3.TrackMeta) {
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range in {
				if meta, err := id3.ParseTrackFast(path, rootDir); err == nil {
					out <- meta
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(out)
	}()
}
