package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
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

var ErrInvalidURL = errors.New("untrusted or invalid target_url")

// SwitchTrackVersion выполняет привязку выбранного источника (YouTube, YT Music, SoundCloud, Bandcamp):
// скачивание потока во временный каталог, конвертацию в MP3 320k CBR, накат канонических ID3v2.4 тегов
// и обложки альбома, замену файла и фиксацию ручного пина в SQLite базе данных.
func (s *Service) SwitchTrackVersion(ctx context.Context, trackID, targetURL string) (*SwitchVersionResult, error) {
	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		return nil, fmt.Errorf("%w: target_url cannot be empty", ErrInvalidURL)
	}

	if strings.HasPrefix(targetURL, "-") {
		return nil, fmt.Errorf("%w: target_url cannot start with '-'", ErrInvalidURL)
	}

	parsedURL, pErr := url.Parse(targetURL)
	if pErr != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return nil, fmt.Errorf("%w: invalid url scheme", ErrInvalidURL)
	}

	host := strings.ToLower(parsedURL.Hostname())
	if !resolver.IsAllowedURL(targetURL) {
		return nil, fmt.Errorf("%w: domain %q is not in allowed sources whitelist", ErrInvalidURL, host)
	}

	sourceType := "direct"
	if strings.Contains(host, "youtube.com") || strings.Contains(host, "youtu.be") {
		sourceType = "youtube"
		if strings.Contains(host, "music.youtube.com") {
			sourceType = "ytmusic"
		}
	} else if strings.Contains(host, "soundcloud.com") {
		sourceType = "soundcloud"
	} else if strings.Contains(host, "bandcamp.com") {
		sourceType = "bandcamp"
	}

	var artist, title, isrc, relFilePath, absFilePath string
	var foundJob *DownloadJob
	var foundIdx int = -1

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
	var dbTrackID int64
	if foundJob == nil && s.Database() != nil {
		entity, err := s.Database().GetTrackByIDOrHash(ctx, trackID)
		if err == nil && entity != nil {
			artist = entity.Artist
			title = entity.Title
			relFilePath = entity.FilePath
			dbTrackID = entity.ID
		}
	}

	if artist == "" || title == "" {
		return nil, fmt.Errorf("track '%s' not found in active jobs or library database", trackID)
	}

	// 3. Разрешение физического пути к существующему файлу через PathSandbox
	if relFilePath != "" {
		if p, err := s.musicSandbox.Resolve(relFilePath, false); err == nil {
			if _, statErr := safefs.Stat(p); statErr == nil {
				absFilePath = p
			}
		}
		if absFilePath == "" {
			if p, err := s.tmpSandbox.Resolve(relFilePath, false); err == nil {
				if _, statErr := safefs.Stat(p); statErr == nil {
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
	rawFile, dErr := s.downloadAndValidateRawStream(ctx, targetURL, tmpOutTemplate, candDuration, nil)
	if dErr != nil {
		return nil, fmt.Errorf("failed to download audio from target URL: %w", dErr)
	}
	defer safefs.Remove(rawFile)

	// 6. Перекодирование в MP3 320 kbps CBR
	tmpMP3File := filepath.Join(s.tmpSandbox.Root(), fmt.Sprintf("switch_%d.mp3", time.Now().UnixNano()))
	if tErr := s.transcodeToMP3(ctx, rawFile, tmpMP3File); tErr != nil {
		return nil, fmt.Errorf("failed to transcode audio to MP3: %w", tErr)
	}
	defer safefs.Remove(tmpMP3File)

	// 7. Если длительность кандидата неизвестна — замеряем через ffprobe
	if candDuration <= 0 {
		if measured, ffErr := measureAudioDuration(tmpMP3File); ffErr == nil && measured > 0 {
			candDuration = measured
		} else {
			candDuration = 180
		}
	}

	// 8. Перенос канонических ID3v2.4 тегов и обложки альбома
	var tagData id3.TrackTags
	var cover *id3.CoverInfo

	if absFilePath != "" {
		oldTags, _ := id3.ReadTags(absFilePath, "")
		cover, _ = id3.ReadCover(absFilePath)
		if oldTags != nil {
			tagData = *oldTags
			tagData.Duration = float64(candDuration)
		} else {
			tagData = id3.TrackTags{Title: title, Artist: artist, Duration: float64(candDuration)}
		}
	} else if foundJob != nil && foundIdx >= 0 && foundIdx < len(foundJob.Tracks) {
		tr := foundJob.Tracks[foundIdx]
		alb := tr.Album
		if alb == "" && foundJob.AlbumTitle != nil {
			alb = *foundJob.AlbumTitle
		}
		albArt := tr.AlbumArtist
		if albArt == "" && foundJob.ArtistName != nil {
			albArt = *foundJob.ArtistName
		}
		tagData = id3.TrackTags{
			Title:       tr.Title,
			Artist:      tr.Artist,
			Album:       alb,
			AlbumArtist: albArt,
			TrackNumber: tr.TrackNumber,
			DiscNumber:  tr.DiscNumber,
			Duration:    float64(candDuration),
		}
		if tr.TrackTotal != nil {
			tagData.TrackTotal = *tr.TrackTotal
		}
		if tr.DiscTotal != nil {
			tagData.DiscTotal = *tr.DiscTotal
		}
		if tr.Year != nil {
			tagData.Year = *tr.Year
		}
		if tr.Genre != nil {
			tagData.Genre = *tr.Genre
		}

		if foundJob.CoverRelativePath != "" {
			if covAbs, cErr := s.tmpSandbox.Resolve(foundJob.CoverRelativePath, false); cErr == nil {
				cover, _ = id3.ReadCover(covAbs)
			}
		}
	} else {
		tagData = id3.TrackTags{Title: title, Artist: artist, Duration: float64(candDuration)}
	}

	_ = id3.WriteTags(tmpMP3File, tagData, cover)

	// 9. Атомарная замена или создание файла в целевой папке
	destPath := absFilePath
	if destPath == "" {
		if foundJob != nil {
			convertedDir, _ := s.tmpSandbox.Resolve(filepath.Join(foundJob.ID, "converted"), true)
			_ = safefs.MkdirAll(convertedDir, 0755)
			cleanTitle := id3.CleanTitle(title, "Track")
			trackNum := 1
			if foundIdx >= 0 && foundIdx < len(foundJob.Tracks) && foundJob.Tracks[foundIdx].TrackNumber > 0 {
				trackNum = foundJob.Tracks[foundIdx].TrackNumber
			}
			trackPrefix := fmt.Sprintf("%02d. ", trackNum)
			maxTitleBytes := 255 - len(trackPrefix) - len(".mp3")
			if maxTitleBytes <= 0 {
				maxTitleBytes = 1
			}
			safeTitle := sandbox.SanitizeFilenameWithBudget(cleanTitle, "Track", maxTitleBytes)
			destPath = filepath.Join(convertedDir, fmt.Sprintf("%s%s.mp3", trackPrefix, safeTitle))
		} else {
			cleanTitle := id3.CleanTitle(title, "Track")
			safeName := sandbox.SanitizeFilenameWithBudget(fmt.Sprintf("%s - %s.mp3", artist, cleanTitle), "Track.mp3", 255)
			destPath, _ = s.musicSandbox.Resolve(safeName, false)
		}
	}

	_ = safefs.Chmod(tmpMP3File, 0644)
	if err := safefs.Rename(tmpMP3File, destPath); err != nil {
		if cpErr := copyFile(tmpMP3File, destPath); cpErr != nil {
			return nil, fmt.Errorf("failed to replace destination file: %w", cpErr)
		}
		_ = safefs.Remove(tmpMP3File)
	}
	_ = safefs.Chmod(destPath, 0644)

	// 10. Фиксация ручного выбора в SQLite базе данных
	if s.Database() != nil {
		_ = s.Database().SavePinnedSource(ctx, db.PinnedTrackSource{
			CacheKey:   cacheKey,
			ArtistName: artist,
			TrackTitle: title,
			ISRC:       &isrc,
			PinnedURL:  targetURL,
			SourceType: sourceType,
			Duration:   candDuration,
		})
		_ = s.Database().PinSoundCloudVersion(ctx, cacheKey, artist, title, targetURL, candDuration)
		if dbTrackID > 0 {
			_ = s.Database().UpdateTrackPinnedURL(ctx, dbTrackID, targetURL)
		}
	}

	// 11. Обновление состояния задачи в памяти
	if foundJob != nil {
		relConverted, _ := s.tmpSandbox.ToRelative(destPath)
		foundJob.UpdateTrack(foundIdx, func(tr *resolver.ResolvedTrack) {
			tr.AudioSourceURL = targetURL
			tr.VersionStatus = "manual"
			tr.Duration = candDuration
			tr.ConvertedPath = &relConverted
			tr.Status = "ready"
			tr.Error = nil
		})

		// Пересчет количества готовых треков и обновление статуса задачи
		snap := foundJob.Snapshot()
		readyCount := 0
		for _, tr := range snap.Tracks {
			if tr.Status == "ready" && tr.ConvertedPath != nil && *tr.ConvertedPath != "" {
				readyCount++
			}
		}

		foundJob.mu.Lock()
		foundJob.CompletedTracks = readyCount
		if readyCount >= snap.TotalTracks && snap.TotalTracks > 0 {
			foundJob.Status = StatusReady
			foundJob.Progress = 100.0
			foundJob.CurrentStep = fmt.Sprintf("Готово к проверке тегов (%d/%d скачано)", readyCount, snap.TotalTracks)
		} else if readyCount > 0 {
			foundJob.Status = StatusPartialReady
			foundJob.CurrentStep = fmt.Sprintf("Готов частично (%d/%d готово)", readyCount, snap.TotalTracks)
		}
		foundJob.UpdatedAt = time.Now().UTC()
		foundJob.mu.Unlock()
		foundJob.notifyUpdate()
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
	in, err := safefs.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := safefs.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	out, err := safefs.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
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
