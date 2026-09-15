package scanner

import (
	"path/filepath"
	"strings"
	"sync"

	"music-scanner/catalog"
	"music-scanner/db"
	"music-scanner/id3"
	"music-scanner/validator"
)

// StartAuditWorkerPool запускает пул горутин для параллельного парсинга, валидации путей и жанров.
func StartAuditWorkerPool(
	rootDir string,
	workers int,
	autoFixGenres bool,
	in <-chan string,
	out chan<- db.TrackAuditRecord,
) {
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range in {
				meta, err := id3.ParseTrackFast(path, rootDir)
				if err != nil {
					continue
				}

				// 1. Валидация канонического пути
				isPathValid, suggestedPath, reason := validator.ValidateTrackPath(
					meta.RelPath, meta.Album, meta.TrackNumber, meta.Title,
				)

				// 2. Валидация и нормализация жанра
				genreCheck := validator.AnalyzeAndNormalizeGenre(meta.Genre)
				genreAutoFixed := false

				if (genreCheck.HasCyrillic || genreCheck.IsNonCanonical) && genreCheck.IsAutoFixable && autoFixGenres {
					if err := id3.RewriteGenre(path, genreCheck.CanonicalGenre); err == nil {
						meta.Genre = genreCheck.CanonicalGenre
						genreAutoFixed = true
					}
				}

				// 3. Проверка обложки в папке альбома, если нет вшитой
				coverPath := ""
				hasCover := meta.HasCover
				if !hasCover {
					dir := filepath.Dir(path)
					for _, name := range []string{"cover.jpg", "cover.png", "folder.jpg", "folder.png", "front.jpg", "front.png"} {
						cp := filepath.Join(dir, name)
						if relCover, err := filepath.Rel(rootDir, cp); err == nil {
							coverPath = filepath.ToSlash(relCover)
							hasCover = true
							break
						}
					}
				}

				record := db.TrackAuditRecord{
					RelPath:             meta.RelPath,
					Title:               meta.Title,
					CleanTitle:          catalog.CleanString(meta.Title),
					Artist:              meta.Artist,
					CleanArtist:         catalog.CleanString(meta.Artist),
					Album:               meta.Album,
					CleanAlbum:          catalog.CleanString(meta.Album),
					AlbumArtist:         meta.AlbumArtist,
					Year:                meta.Year,
					TrackNumber:         meta.TrackNumber,
					TrackTotal:          meta.TrackTotal,
					DiscNumber:          meta.DiscNumber,
					DiscTotal:           meta.DiscTotal,
					Duration:            meta.Duration,
					Bitrate:             meta.Bitrate,
					FileSize:            meta.Size,
					Mtime:               meta.Mtime,
					HasCover:            hasCover,
					CoverPath:           coverPath,
					LyricsStatus:        meta.LyricsStatus,
					LyricsPath:          meta.LyricsPath,
					IsPathValid:         isPathValid,
					PathViolationReason: reason,
					SuggestedPath:       suggestedPath,
					Genre:               strings.TrimSpace(meta.Genre),
					GenreIsNonEn:        (genreCheck.HasCyrillic || genreCheck.IsNonCanonical) && !genreAutoFixed,
					GenreCanonical:      genreCheck.CanonicalGenre,
					GenreIsAutoFixed:    genreAutoFixed,
				}

				out <- record
			}
		}()
	}

	go func() {
		wg.Wait()
		close(out)
	}()
}
