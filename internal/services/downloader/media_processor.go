package downloader

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"music-manager/internal/services/resolver"
)

var progressRegex = regexp.MustCompile(`(?:\[download\]|\bdownload:)?\s*(\d+(?:\.\d+)?)%`)

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
		"--concurrent-fragments", "4",
		"--buffer-size", "64k",
		"--extractor-args", "youtube:player_client=tv_embedded,web_embedded,android,ios",
		"--add-header", "Accept-Language:ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7",
	}

	useCookies := resolver.IsValidNetscapeCookiesFile(s.cookiesPath)
	if useCookies {
		args = append(args, "--cookies", s.cookiesPath)
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
		// Fallback: если yt-dlp отклонил куки, повторяем загрузку в гостевом режиме
		if useCookies && strings.Contains(stderr.String(), "does not look like a Netscape format cookies file") {
			argsNoCookies := make([]string, 0, len(args))
			for i := 0; i < len(args); i++ {
				if args[i] == "--cookies" {
					i++
					continue
				}
				argsNoCookies = append(argsNoCookies, args[i])
			}
			cmdRetry := exec.CommandContext(ctx, "yt-dlp", argsNoCookies...)
			stdoutRetry, pErr := cmdRetry.StdoutPipe()
			if pErr == nil {
				var stderrRetry bytes.Buffer
				cmdRetry.Stderr = &stderrRetry
				if sErr := cmdRetry.Start(); sErr == nil {
					scannerRetry := bufio.NewScanner(stdoutRetry)
					for scannerRetry.Scan() {
						line := scannerRetry.Text()
						match := progressRegex.FindStringSubmatch(line)
						if len(match) >= 2 && onProgress != nil {
							if val, valErr := strconv.ParseFloat(match[1], 64); valErr == nil {
								onProgress(val)
							}
						}
					}
					if wErr := cmdRetry.Wait(); wErr == nil {
						return s.findDownloadedFile(outputTemplate)
					}
					stderr = stderrRetry
				}
			}
		}
		return "", fmt.Errorf("yt-dlp download failed: %w: %s", err, stderr.String())
	}

	return s.findDownloadedFile(outputTemplate)
}

func (s *Service) findDownloadedFile(outputTemplate string) (string, error) {
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
		"-q:a", "0",
		"-ar", "44100",
		"-ac", "2",
		"-map_metadata", "-1",
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
