package downloader

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

var progressRegex = regexp.MustCompile(`(?:\[download\]|\bdownload:)?\s*(\d+(?:\.\d+)?)%`)

// Service orchestrates audio downloading, transcoding and ID3 tagging.
type Service struct {
	jobs         *JobManager
	resolver     *resolver.SourceResolver
	tmpSandbox   *sandbox.PathSandbox
	musicSandbox *sandbox.PathSandbox
	cookiesPath  string
	sem          chan struct{}
	client       *http.Client
}

// NewService creates a new DownloadService.
func NewService(
	jobs *JobManager,
	res *resolver.SourceResolver,
	tmpSandbox *sandbox.PathSandbox,
	musicSandbox *sandbox.PathSandbox,
	cookiesPath string,
	maxConcurrent int,
) *Service {
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	svc := &Service{
		jobs:         jobs,
		resolver:     res,
		tmpSandbox:   tmpSandbox,
		musicSandbox: musicSandbox,
		cookiesPath:  cookiesPath,
		sem:          make(chan struct{}, maxConcurrent),
		client:       &http.Client{Timeout: 30 * time.Second},
	}
	go svc.startCleaner(24 * time.Hour)
	return svc
}

// Jobs returns the underlying JobManager.
func (s *Service) Jobs() *JobManager {
	return s.jobs
}

func generateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b)
}

// CreateJob creates and queues a new download task.
func (s *Service) CreateJob(rawURL, source string) (*DownloadJob, error) {
	cleanURL := strings.TrimSpace(rawURL)
	if cleanURL == "" {
		return nil, fmt.Errorf("URL cannot be empty")
	}

	// Always enforce domain whitelist via DetectSource
	detectedSource, err := s.resolver.DetectSource(cleanURL)
	if err != nil {
		return nil, err
	}
	if source == "" || source == "auto" {
		source = detectedSource
	}

	jobID := generateUUID()
	job := &DownloadJob{
		ID:          jobID,
		Status:      StatusQueued,
		Source:      source,
		URL:         cleanURL,
		Progress:    0.0,
		CurrentStep: "Задача ожидает очереди",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:   time.Now().UTC(),
	}

	s.jobs.Add(job)
	go s.runPipeline(jobID)
	return job, nil
}

// CreateBatchJobs creates multiple download jobs.
func (s *Service) CreateBatchJobs(urls []string, source string) ([]*DownloadJob, error) {
	var created []*DownloadJob
	for _, u := range urls {
		j, err := s.CreateJob(u, source)
		if err != nil {
			return nil, err
		}
		created = append(created, j)
	}
	return created, nil
}

// RetryJob resets and restarts a failed or cancelled download task.
func (s *Service) RetryJob(jobID string) (*DownloadJob, error) {
	job, ok := s.jobs.Get(jobID)
	if !ok {
		return nil, fmt.Errorf("задача %s не найдена", jobID)
	}

	snap := job.Snapshot()
	if snap.Status == StatusResolving || snap.Status == StatusDownloading || snap.Status == StatusConverting || snap.Status == StatusTagging {
		return nil, fmt.Errorf("задача уже выполняется")
	}

	// Clean up old temporary files
	jobDir, err := s.tmpSandbox.Resolve(jobID, false)
	if err == nil {
		_ = os.RemoveAll(jobDir)
	}

	// Reset job state
	job.mu.Lock()
	job.Status = StatusQueued
	job.Progress = 0.0
	job.CurrentStep = "Задача перезапущена, ожидает очереди"
	job.Error = nil
	job.CompletedTracks = 0
	job.UpdatedAt = time.Now().UTC()
	job.mu.Unlock()
	job.notifyUpdate()

	go s.runPipeline(jobID)
	return job, nil
}

func (s *Service) runPipeline(jobID string) {
	job, ok := s.jobs.Get(jobID)
	if !ok {
		return
	}

	// Acquire concurrency slot
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	ctx, cancel := context.WithCancel(context.Background())
	job.SetCancelFunc(cancel)
	defer cancel()

	// Step 1: Resolving
	job.SetStatus(StatusResolving)
	job.UpdateProgress(5.0, "Определение источника и получение метаданных")

	meta, tracks, err := s.resolver.Resolve(ctx, job.URL, job.Source)
	if err != nil {
		s.failJob(job, fmt.Sprintf("Ошибка разрешения метаданных: %v", err))
		return
	}

	job.SetMetadata(*meta, tracks)

	// Create temporary working directories inside TMP_DIR sandbox
	rawDir, err := s.tmpSandbox.Resolve(filepath.Join(job.ID, "raw"), true)
	if err != nil {
		s.failJob(job, fmt.Sprintf("Sandbox error: %v", err))
		return
	}
	convertedDir, err := s.tmpSandbox.Resolve(filepath.Join(job.ID, "converted"), true)
	if err != nil {
		s.failJob(job, fmt.Sprintf("Sandbox error: %v", err))
		return
	}
	_ = os.MkdirAll(rawDir, 0755)
	_ = os.MkdirAll(convertedDir, 0755)

	// Download album cover if available
	var coverBytes []byte
	if meta.CoverURL != "" {
		cBytes, cErr := s.downloadCover(ctx, meta.CoverURL)
		if cErr == nil && len(cBytes) > 0 {
			coverBytes = cBytes
			coverPath := filepath.Join(rawDir, "cover.jpg")
			_ = os.WriteFile(coverPath, coverBytes, 0644)
			relCover, _ := s.tmpSandbox.ToRelative(filepath.Join(rawDir, "cover.jpg"))
			job.SetCoverPath(relCover)
		}
	}

	total := len(tracks)
	if total == 0 {
		s.failJob(job, "Релиз не содержит доступных аудиотреков")
		return
	}

	// Process each track
	var wg sync.WaitGroup
	trackSem := make(chan struct{}, 2) // Up to 2 concurrent tracks per job

	for i := range tracks {
		idx := i + 1
		t := &tracks[i]

		wg.Add(1)
		go func(trackIdx int, track *resolver.ResolvedTrack) {
			defer wg.Done()
			trackSem <- struct{}{}
			defer func() { <-trackSem }()

			select {
			case <-ctx.Done():
				return
			default:
			}

			// If audio stream URL is not yet resolved (e.g. Deezer album), match to YouTube
			if track.AudioSourceURL == "" || strings.Contains(track.AudioSourceURL, "deezer.com") {
				job.SetCurrentStep(fmt.Sprintf("Поиск аудиопотока %d/%d: %s", trackIdx, total, track.Title))

				matchedURL, mErr := s.resolver.Matcher().FindBestMatch(ctx, track.Title, track.Artist, track.Duration)
				if mErr != nil {
					errStr := mErr.Error()
					job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
						tr.Status = "error"
						tr.Error = &errStr
					})
					return
				}
				job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
					tr.AudioSourceURL = matchedURL
				})
				track.AudioSourceURL = matchedURL
			}

			// Download stream with yt-dlp
			rawOutTemplate := filepath.Join(rawDir, fmt.Sprintf("%02d.%%(ext)s", trackIdx))
			job.SetStepAndStatus(fmt.Sprintf("Загрузка трека %d/%d: %s", trackIdx, total, track.Title), StatusDownloading)

			rawAudioFile, dErr := s.downloadRawStream(ctx, track.AudioSourceURL, rawOutTemplate, func(p float64) {
				job.UpdateTrackProgress(trackIdx-1, p, fmt.Sprintf("Загрузка трека %d/%d: %s (%.0f%%)", trackIdx, total, track.Title, p))
			})
			if dErr != nil {
				errStr := dErr.Error()
				job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
					tr.Status = "error"
					tr.Error = &errStr
				})
				return
			}
			defer os.Remove(rawAudioFile)

			// Transcode to MP3 320 kbps CBR
			job.SetStepAndStatus(fmt.Sprintf("Конвертация трека %d/%d: %s в MP3 320k", trackIdx, total, track.Title), StatusConverting)

			cleanTitle := id3.CleanTitle(track.Title, fmt.Sprintf("Track_%02d", trackIdx))
			safeTitle := sandbox.SanitizeFilename(cleanTitle, fmt.Sprintf("Track_%02d", trackIdx))
			mp3Filename := fmt.Sprintf("%02d. %s.mp3", track.TrackNumber, safeTitle)
			mp3Path := filepath.Join(convertedDir, mp3Filename)

			if cErr := s.transcodeToMP3(ctx, rawAudioFile, mp3Path); cErr != nil {
				errStr := cErr.Error()
				job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
					tr.Status = "error"
					tr.Error = &errStr
				})
				return
			}

			// Write ID3v2.4 tags
			job.SetStepAndStatus(fmt.Sprintf("Запись ID3v2.4 тегов %d/%d: %s", trackIdx, total, track.Title), StatusTagging)

			tagData := id3.TrackTags{
				Path:        mp3Path,
				Title:       track.Title,
				Artist:      track.Artist,
				Album:       track.Album,
				AlbumArtist: track.AlbumArtist,
				TrackNumber: track.TrackNumber,
				DiscNumber:  track.DiscNumber,
				Duration:    float64(track.Duration),
			}
			if track.TrackTotal != nil {
				tagData.TrackTotal = *track.TrackTotal
			}
			if track.DiscTotal != nil {
				tagData.DiscTotal = *track.DiscTotal
			}
			if track.Year != nil {
				tagData.Year = *track.Year
			}
			if track.Genre != nil {
				tagData.Genre = *track.Genre
			}

			var cover *id3.CoverInfo
			if len(coverBytes) > 0 {
				mime := "image/jpeg"
				if bytes.HasPrefix(coverBytes, []byte("\x89PNG")) {
					mime = "image/png"
				}
				cover = &id3.CoverInfo{
					MimeType: mime,
					Data:     coverBytes,
				}
			}

			_ = id3.WriteTags(mp3Path, tagData, cover)

			relConverted, _ := s.tmpSandbox.ToRelative(mp3Path)
			job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
				tr.Status = "ready"
				tr.ConvertedPath = &relConverted
			})

			job.IncrementCompleted(total)

		}(idx, t)
	}

	wg.Wait()

	// Final step: READY or ERROR
	snap := job.Snapshot()
	if snap.CompletedTracks == 0 && total > 0 {
		var errDetails []string
		for _, tr := range snap.Tracks {
			if tr.Error != nil && *tr.Error != "" {
				errDetails = append(errDetails, fmt.Sprintf("%s: %s", tr.Title, *tr.Error))
			}
		}
		errMsg := "Ни один из треков релиза не был успешно загружен"
		if len(errDetails) > 0 {
			errMsg = fmt.Sprintf("%s (%s)", errMsg, strings.Join(errDetails, "; "))
		}
		s.failJob(job, errMsg)
		return
	}

	job.SetReady(snap.CompletedTracks, total)
}

func (s *Service) failJob(job *DownloadJob, errMsg string) {
	job.SetError(errMsg)
	log.Printf("[Downloader] Job %s failed: %s", job.ID, errMsg)
}

func (s *Service) downloadRawStream(ctx context.Context, sourceURL, outputTemplate string, onProgress func(float64)) (string, error) {
	args := []string{
		"--ignore-config",
		"--no-download-archive",
		"-f", "ba/b",
		"-o", outputTemplate,
		"--no-playlist",
		"--newline",
		"--no-colors",
		"--progress",
		"--progress-template", "download:%(progress._percent_str)s",
		"--progress-delta", "0.2",
		"--no-warnings",
		"--add-header", "Accept-Language:ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7",
	}

	if s.cookiesPath != "" {
		if _, err := os.Stat(s.cookiesPath); err == nil {
			args = append(args, "--cookies", s.cookiesPath)
		} else {
			args = append(args, "--extractor-args", "youtube:player_client=ios,visionos")
		}
	} else {
		args = append(args, "--extractor-args", "youtube:player_client=ios,visionos")
	}
	args = append(args, sourceURL)

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start yt-dlp: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		match := progressRegex.FindStringSubmatch(line)
		if len(match) >= 2 && onProgress != nil {
			if val, pErr := strconv.ParseFloat(match[1], 64); pErr == nil {
				onProgress(val)
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("yt-dlp download failed: %w: %s", err, stderr.String())
	}

	// Locate downloaded file
	dir := filepath.Dir(outputTemplate)
	pattern := filepath.Base(strings.Replace(outputTemplate, "%(ext)s", "*", 1))
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("downloaded audio file not found matching template %s", outputTemplate)
	}

	return matches[0], nil
}

func (s *Service) transcodeToMP3(ctx context.Context, inputRaw, outputMP3 string) error {
	args := []string{
		"-y",
		"-threads", "0",
		"-i", inputRaw,
		"-vn",
		"-codec:a", "libmp3lame",
		"-b:a", "320k",
		"-compression_level", "2",
		"-ar", "44100",
		"-ac", "2",
		outputMP3,
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg transcoding error: %w: %s", err, stderr.String())
	}

	info, err := os.Stat(outputMP3)
	if err != nil || info.Size() == 0 {
		return fmt.Errorf("transcoded MP3 is empty or missing")
	}

	return nil
}

func (s *Service) downloadCover(ctx context.Context, coverURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download cover: status %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

// DeleteJob deletes a job and removes its temporary directory.
func (s *Service) DeleteJob(id string) error {
	s.jobs.Delete(id)
	jobDir, err := s.tmpSandbox.Resolve(id, false)
	if err == nil {
		_ = os.RemoveAll(jobDir)
	}
	return nil
}

func (s *Service) startCleaner(maxAge time.Duration) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		s.cleanOldTempDirs(maxAge)
	}
}

func (s *Service) cleanOldTempDirs(maxAge time.Duration) {
	root := s.tmpSandbox.Root()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirPath, err := s.tmpSandbox.Resolve(entry.Name(), false)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(dirPath)
		}
	}
}

