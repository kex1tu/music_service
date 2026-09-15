package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"music-scanner/catalog"
	"music-scanner/db"
	"music-scanner/scanner"
	"music-scanner/watcher"
)

func main() {
	log.SetOutput(os.Stderr)

	rootDir := flag.String("root", "", "Path to MUSIC_DIR root directory")
	dbPath := flag.String("db", "data/library.db", "Path to SQLite database file")
	workers := flag.Int("workers", runtime.NumCPU()*2, "Number of concurrent workers")
	daemon := flag.Bool("daemon", false, "Run in background daemon mode with live fsnotify watcher")
	autoFixGenres := flag.Bool("auto-fix-genres", true, "Automatically replace Russian genres with English canonical counterparts")
	checkMissing := flag.Bool("check-missing", true, "Check Deezer catalog for missing tracks in incomplete albums")
	flag.Parse()

	if *rootDir == "" {
		fmt.Fprintln(os.Stderr, "Error: --root argument is required")
		os.Exit(1)
	}

	absRoot, err := filepath.Abs(*rootDir)
	if err != nil {
		log.Fatalf("Invalid root path: %v", err)
	}

	stat, err := os.Stat(absRoot)
	if err != nil || !stat.IsDir() {
		log.Fatalf("Root directory does not exist or is not a directory: %s", absRoot)
	}

	absDB, err := filepath.Abs(*dbPath)
	if err != nil {
		log.Fatalf("Invalid db path: %v", err)
	}

	numWorkers := *workers
	if numWorkers <= 0 {
		numWorkers = 1
	}

	database, err := db.OpenDB(absDB)
	if err != nil {
		log.Fatalf("Failed to open SQLite database: %v", err)
	}
	defer database.Close()

	log.Printf("[music-auditor] Initialized with root: %s, db: %s, workers: %d", absRoot, absDB, numWorkers)

	// Запуск первого сканирования
	runScanCycle(database, absRoot, numWorkers, *autoFixGenres, *checkMissing)

	if !*daemon {
		log.Println("[music-auditor] Scan complete. Exiting (--daemon is false).")
		return
	}

	log.Println("[music-auditor] Starting live fsnotify watcher daemon...")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	triggerChan := make(chan string, 100)
	fsWatcher, err := watcher.NewFSWatcher(500*time.Millisecond, triggerChan)
	if err != nil {
		log.Fatalf("Failed to initialize fsnotify watcher: %v", err)
	}

	if err := fsWatcher.AddRecursive(absRoot); err != nil {
		log.Printf("[music-auditor] Warning: error watching root: %v", err)
	}

	go fsWatcher.Start(ctx)

	for {
		select {
		case sig := <-sigChan:
			log.Printf("[music-auditor] Received signal %v, shutting down gracefully...", sig)
			cancel()
			return

		case changedPath := <-triggerChan:
			log.Printf("[music-auditor] Change detected on disk: %s. Running incremental scan...", changedPath)
			runScanCycle(database, absRoot, numWorkers, *autoFixGenres, *checkMissing)
		}
	}
}

func runScanCycle(
	database *db.DB,
	rootDir string,
	workers int,
	autoFixGenres bool,
	checkMissing bool,
) {
	startTime := time.Now()

	existingMtimes, err := database.GetExistingMtimes()
	if err != nil {
		log.Printf("[music-auditor] Error reading existing mtimes: %v", err)
		existingMtimes = make(map[string]db.FileMtimeEntry)
	}

	pathsChan := make(chan string, 10000)
	recordsChan := make(chan db.TrackAuditRecord, 5000)
	existingRelPaths := make(map[string]bool)

	// Запуск пула воркеров аудита
	scanner.StartAuditWorkerPool(rootDir, workers, autoFixGenres, pathsChan, recordsChan)

	// Запуск инкрементального обхода файлов
	go func() {
		_ = scanner.WalkDirectoryIncremental(rootDir, existingMtimes, pathsChan, existingRelPaths)
	}()

	// Сбор, вывод в stdout (NDJSON) и пакетная запись в SQLite
	stdoutWriter := bufio.NewWriterSize(os.Stdout, 64*1024)
	stdoutEncoder := json.NewEncoder(stdoutWriter)

	batch := make([]db.TrackAuditRecord, 0, 100)
	totalProcessed := 0

	for record := range recordsChan {
		_ = stdoutEncoder.Encode(record)
		batch = append(batch, record)
		totalProcessed++

		if len(batch) >= 100 {
			_ = stdoutWriter.Flush()
			if err := database.SaveTrackBatch(batch); err != nil {
				log.Printf("[music-auditor] Error saving track batch: %v", err)
			}
			batch = batch[:0]
		}
	}

	_ = stdoutWriter.Flush()

	if len(batch) > 0 {
		if err := database.SaveTrackBatch(batch); err != nil {
			log.Printf("[music-auditor] Error saving final track batch: %v", err)
		}
	}

	// Удаление несуществующих файлов
	deletedCount, err := database.DeleteMissingFiles(existingRelPaths)
	if err != nil {
		log.Printf("[music-auditor] Error cleaning deleted files: %v", err)
	}

	duration := time.Since(startTime)
	log.Printf("[music-auditor] Incremental scan completed in %v. Processed: %d, Deleted: %d, Total tracked: %d",
		duration, totalProcessed, deletedCount, len(existingRelPaths))

	// Сверка неполных альбомов с каталогом Deezer
	if checkMissing {
		incompleteAlbums, err := database.GetIncompleteAlbums()
		if err == nil && len(incompleteAlbums) > 0 {
			log.Printf("[music-auditor] Checking catalog for %d incomplete albums...", len(incompleteAlbums))
			for _, album := range incompleteAlbums {
				remoteAlbum, err := catalog.FetchDeezerAlbumTracks(album.Artist, album.Title)
				if err != nil {
					continue
				}

				missing := catalog.FindMissingTracks(album.ID, nil, remoteAlbum)
				if len(missing) > 0 {
					_ = database.SaveMissingTracks(album.ID, missing)
					log.Printf("[music-auditor] Album '%s - %s': found %d missing tracks",
						album.Artist, album.Title, len(missing))
				}
			}
		}
	}
}
