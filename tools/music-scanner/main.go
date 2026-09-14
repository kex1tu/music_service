package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"

	"music-scanner/id3"
	"music-scanner/scanner"
)

func main() {
	rootDir := flag.String("root", "", "Path to MUSIC_DIR root directory")
	workers := flag.Int("workers", runtime.NumCPU()*2, "Number of concurrent workers")
	flag.Parse()

	if *rootDir == "" {
		fmt.Fprintln(os.Stderr, "Error: --root argument is required")
		os.Exit(1)
	}

	stat, err := os.Stat(*rootDir)
	if err != nil || !stat.IsDir() {
		fmt.Fprintf(os.Stderr, "Error: root directory does not exist or is not a directory: %s\n", *rootDir)
		os.Exit(1)
	}

	numWorkers := *workers
	if numWorkers <= 0 {
		numWorkers = 1
	}

	pathsChan := make(chan string, 10000)
	resultsChan := make(chan id3.TrackMeta, 5000)

	// Запуск пула воркеров
	scanner.StartWorkerPool(*rootDir, numWorkers, pathsChan, resultsChan)

	// Запуск обхода директории
	go func() {
		_ = scanner.WalkDirectory(*rootDir, pathsChan)
	}()

	// Потоковый вывод результатов в stdout в формате NDJSON
	writer := bufio.NewWriterSize(os.Stdout, 64*1024)
	encoder := json.NewEncoder(writer)

	for meta := range resultsChan {
		if err := encoder.Encode(meta); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding track meta: %v\n", err)
		}
	}

	if err := writer.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "Error flushing stdout: %v\n", err)
	}
}
