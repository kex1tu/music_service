package downloader

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

// SwitchVersionResult содержит результат ручного переключения версии трека.
type SwitchVersionResult struct {
	TrackID   string `json:"track_id"`
	TargetURL string `json:"target_url"`
	Status    string `json:"status"` // "manual"
	Duration  int    `json:"duration"`
	FilePath  string `json:"file_path"`
}

// SwitchTrackVersion выполняет горячее переключение версии трека:
// скачивание выбранного SoundCloud URL, перекодирование в MP3 320k CBR, перенос ID3v2.4 тегов,
// атомарную замену аудиофайла и сохранение ручного пина в SQLite кэше.
func (s *Service) SwitchTrackVersion(ctx context.Context, trackID, targetURL string) (*SwitchVersionResult, error) {
	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		return nil, fmt.Errorf("target_url cannot be empty")
	}

	var artist, title, isrc, relFilePath, absFilePath string
	var foundJob *DownloadJob
	var foundIdx int

	// 1. Поиск трека в активных и недавних задачах загрузчика
	for _, job := range s.jobs.GetAll() {
		snap := job.Snapshot()
		for idx, tr := range snap.Tracks {
			if tr.TrackID == trackID {
				artist = tr.Artist
				title = tr.Title
				if tr.ISRC != nil {
					isrc = *tr.ISRC
				}
				if tr.ConvertedPath != nil && *tr.ConvertedPath != "" {
					relFilePath = *tr.ConvertedPath
				}
				foundJob = job
				foundIdx = idx
				break
			}
		}
		if foundJob != nil {
			break
		}
	}

	// 2. Если трек не найден в задачах — ищем в базе данных медиатеки по ID/Hash/FilePath
	if foundJob == nil && s.Database() != nil {
		entity, err := s.Database().GetTrackByIDOrHash(ctx, trackID)
		if err == nil && entity != nil {
			artist = entity.Artist
			title = entity.Title
			relFilePath = entity.FilePath
		}
	}

	if artist == "" || title == "" {
		return nil, fmt.Errorf("track '%s' not found in active jobs or library database", trackID)
	}

	// 3. Разрешение физического пути к существующему файлу через PathSandbox
	if relFilePath != "" {
		if p, err := s.musicSandbox.Resolve(relFilePath, false); err == nil {
			if _, statErr := os.Stat(p); statErr == nil {
				absFilePath = p
			}
		}
		if absFilePath == "" {
			if p, err := s.tmpSandbox.Resolve(relFilePath, false); err == nil {
				if _, statErr := os.Stat(p); statErr == nil {
					absFilePath = p
				}
			}
		}
	}

	// 4. Определение канонического ключа и длительности кандидата
	cacheKey := resolver.BuildCacheKey(isrc, artist, title)
	candDuration := 0
	if s.Database() != nil {
		if entry, err := s.Database().GetSoundCloudCache(ctx, cacheKey, 720); err == nil && entry != nil {
			for _, c := range entry.Candidates {
				if c.URL == targetURL {
					candDuration = c.Duration
					break
				}
			}
		}
	}

	// 5. Скачивание аудиопотока во временный каталог через yt-dlp
	tmpOutTemplate := filepath.Join(s.tmpSandbox.Root(), fmt.Sprintf("switch_%d_%%(ext)s", time.Now().UnixNano()))
	rawFile, dErr := s.downloadRawStream(ctx, targetURL, tmpOutTemplate, nil)
	if dErr != nil {
		return nil, fmt.Errorf("failed to download audio from target URL: %w", dErr)
	}
	defer os.Remove(rawFile)

	// 6. Перекодирование в MP3 320 kbps CBR
	tmpMP3File := filepath.Join(s.tmpSandbox.Root(), fmt.Sprintf("switch_%d.mp3", time.Now().UnixNano()))
	if tErr := s.transcodeToMP3(ctx, rawFile, tmpMP3File); tErr != nil {
		return nil, fmt.Errorf("failed to transcode audio to MP3: %w", tErr)
	}
	defer os.Remove(tmpMP3File)

	// 7. Если длительность кандидата неизвестна — замеряем через ffprobe
	if candDuration <= 0 {
		if measured, ffErr := measureAudioDuration(tmpMP3File); ffErr == nil && measured > 0 {
			candDuration = measured
		} else {
			candDuration = 180
		}
	}

	// 8. Перенос существующих ID3v2.4 тегов и обложки
	if absFilePath != "" {
		oldTags, _ := id3.ReadTags(absFilePath, "")
		cover, _ := id3.ReadCover(absFilePath)
		if oldTags != nil {
			oldTags.Duration = float64(candDuration)
			_ = id3.WriteTags(tmpMP3File, *oldTags, cover)
		} else {
			_ = id3.WriteTags(tmpMP3File, id3.TrackTags{Title: title, Artist: artist, Duration: float64(candDuration)}, cover)
		}
	} else {
		_ = id3.WriteTags(tmpMP3File, id3.TrackTags{Title: title, Artist: artist, Duration: float64(candDuration)}, nil)
	}

	// 9. Атомарная замена файла (os.Rename с fallback на копирование при EXDEV)
	destPath := absFilePath
	if destPath == "" {
		cleanTitle := id3.CleanTitle(title, "Track")
		safeName := sandbox.SanitizeFilename(fmt.Sprintf("%s - %s.mp3", artist, cleanTitle), "Track.mp3")
		destPath, _ = s.musicSandbox.Resolve(safeName, false)
	}

	_ = os.Chmod(tmpMP3File, 0644)
	if err := os.Rename(tmpMP3File, destPath); err != nil {
		if cpErr := copyFile(tmpMP3File, destPath); cpErr != nil {
			return nil, fmt.Errorf("failed to replace destination file: %w", cpErr)
		}
		_ = os.Remove(tmpMP3File)
	}
	_ = os.Chmod(destPath, 0644)

	// 10. Фиксация ручного выбора в базе данных SQLite (защита от перезаписи)
	if s.Database() != nil {
		_ = s.Database().PinSoundCloudVersion(ctx, cacheKey, artist, title, targetURL, candDuration)
	}

	// 11. Обновление состояния задачи в памяти
	if foundJob != nil {
		foundJob.UpdateTrack(foundIdx, func(tr *resolver.ResolvedTrack) {
			tr.AudioSourceURL = targetURL
			tr.VersionStatus = "manual"
			tr.Duration = candDuration
		})
	}

	return &SwitchVersionResult{
		TrackID:   trackID,
		TargetURL: targetURL,
		Status:    "manual",
		Duration:  candDuration,
		FilePath:  destPath,
	}, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func measureAudioDuration(filePath string) (int, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", filePath)
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(out))
	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return int(val + 0.5), nil
}
