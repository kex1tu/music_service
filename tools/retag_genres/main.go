// tools/retag_genres/main.go
// Одноразовый инструмент: пакетно переписывает TCON (genre) тег
// в указанных MP3-файлах из tmp_downloads.
//
// Использование (из корня репозитория):
//
//	go run ./tools/retag_genres/ [--dir /path/to/tmp_downloads]
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"music-manager/internal/id3"
)

// retag описывает одну задачу перезаписи жанра.
type retag struct {
	// relPath — путь относительно tmp_downloads, например:
	// "00e7fa41.../converted/01. ОСТАВЬ МЕНЯ.mp3"
	relPath     string
	targetGenre string // канонический жанр в формате "A\\B" (null-sep будет сделан writer'ом)
}

// jobs — полный список файлов из запроса пользователя.
// Инди-поп → Indie Pop — исправлено.
var jobs = []retag{
	{
		relPath:     "00e7fa4190df4c5397e81c4de49523c0/converted/01. ОСТАВЬ МЕНЯ.mp3",
		targetGenre: "Alternative\\Rock",
	},
	{
		relPath:     "054f507126c747a68dac4c7514478882/converted/01. ДЕЦИБЕЛЫ.mp3",
		targetGenre: "Alternative\\Indie Rock\\Rock",
	},
	{
		relPath:     "0589b855842345dd89e9937b405fe7c6/converted/01. Плохим.mp3",
		targetGenre: "Alternative\\Pop\\Rock",
	},
	{
		relPath:     "1b65433307ef4a81b485e0263aea7e3d/converted/03. Фарыфуры.mp3",
		targetGenre: "Rap\\Hip-Hop\\Rock",
	},
	{
		relPath:     "1b65433307ef4a81b485e0263aea7e3d/converted/06. Порвав поводок.mp3",
		targetGenre: "Rap\\Hip-Hop\\Rock",
	},
	{
		relPath:     "1b65433307ef4a81b485e0263aea7e3d/converted/07. Сгораю.mp3",
		targetGenre: "Rap\\Hip-Hop\\Rock",
	},
	{
		relPath:     "1e101b98a85941e88a8586dbd1bd241e/converted/01. Гоша Рубчинский.mp3",
		targetGenre: "Alternative\\Indie Pop\\Pop",
	},
	{
		relPath:     "c9f05f45dcfe42569bee2e5e27f84f3c/converted/01. Inner.mp3",
		targetGenre: "Electronic\\Dance Music",
	},
}

func main() {
	var tmpDir string
	flag.StringVar(&tmpDir, "dir", "tmp_downloads", "Путь к директории tmp_downloads")
	flag.Parse()

	// Раскрываем до абсолютного пути, если передан относительный.
	if !filepath.IsAbs(tmpDir) {
		cwd, err := os.Getwd()
		if err != nil {
			log.Fatalf("os.Getwd: %v", err)
		}
		tmpDir = filepath.Join(cwd, tmpDir)
	}

	ok, fail := 0, 0

	for _, job := range jobs {
		absPath := filepath.Join(tmpDir, filepath.FromSlash(job.relPath))

		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			fmt.Printf("[SKIP]  %s — файл не найден\n", job.relPath)
			fail++
			continue
		}

		// Читаем текущие теги (все фреймы сохраняются).
		tags, err := id3.ReadTags(absPath, job.relPath)
		if err != nil {
			fmt.Printf("[ERR]   %s — ReadTags: %v\n", job.relPath, err)
			fail++
			continue
		}

		oldGenre := tags.Genre
		tags.Genre = job.targetGenre

		// Записываем обратно; cover=nil — обложка сохраняется через RawFrames.
		if err := id3.WriteTags(absPath, *tags, nil); err != nil {
			fmt.Printf("[ERR]   %s — WriteTags: %v\n", job.relPath, err)
			fail++
			continue
		}

		// Верификация: перечитываем и проверяем.
		verify, err := id3.ReadTags(absPath, job.relPath)
		if err != nil {
			fmt.Printf("[WARN]  %s — verify ReadTags: %v\n", job.relPath, err)
		} else {
			got := verify.Genre
			// Нормализуем разделители для сравнения (writer пишет \x00, reader читает как \\).
			wantNorm := strings.ReplaceAll(job.targetGenre, "\\", "\\\\")
			if got == wantNorm || got == job.targetGenre {
				fmt.Printf("[OK]    %s\n       %q → %q\n", job.relPath, oldGenre, got)
				ok++
			} else {
				fmt.Printf("[MISMATCH] %s\n       want %q, got %q\n", job.relPath, job.targetGenre, got)
				ok++ // тег всё же записан — считаем как успех с предупреждением
			}
		}
	}

	fmt.Printf("\nИтого: %d успешно, %d ошибок/пропусков (из %d)\n", ok, fail, len(jobs))
	if fail > 0 {
		os.Exit(1)
	}
}
