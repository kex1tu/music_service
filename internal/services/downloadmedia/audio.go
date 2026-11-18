package downloadmedia

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"music-manager/internal/core/mediaexec"
	"music-manager/internal/core/safefs"
	"music-manager/internal/services/resolver"
)

var progressRegex = regexp.MustCompile(`(?:\[download\]|\bdownload:)?\s*(\d+(?:\.\d+)?)%`)

func downloadRawStream(ctx context.Context, cookiesPath string, sourceURL, outputTemplate string, onProgress func(float64)) (string, error) {
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

	useCookies := resolver.IsValidNetscapeCookiesFile(cookiesPath)
	if useCookies {
		args = append(args, "--cookies", cookiesPath)
	}
	args = append(args, "--", sourceURL)

	cmd := mediaexec.CommandContext(ctx, "yt-dlp", args...)
	defer cmd.Close()
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
			cmdRetry := mediaexec.CommandContext(ctx, "yt-dlp", argsNoCookies...)
			defer cmdRetry.Close()
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
						return findDownloadedFile(outputTemplate)
					}
					stderr = stderrRetry
				}
			}
		}
		return "", fmt.Errorf("yt-dlp download failed: %w: %s", err, stderr.String())
	}

	return findDownloadedFile(outputTemplate)
}

// DownloadAndValidateAudio downloads audio to the supplied temporary template and rejects short previews.
func DownloadAndValidateAudio(ctx context.Context, cookiesPath string, sourceURL, outputTemplate string, expectedDurationSec int, onProgress func(float64)) (string, error) {
	rawFile, err := downloadRawStream(ctx, cookiesPath, sourceURL, outputTemplate, onProgress)
	if err != nil {
		return "", err
	}
	if vErr := validateDownloadedStreamAudio(ctx, rawFile, expectedDurationSec); vErr != nil {
		_ = safefs.Remove(rawFile)
		return "", vErr
	}
	return rawFile, nil
}

func findDownloadedFile(outputTemplate string) (string, error) {
	dir := filepath.Dir(outputTemplate)
	pattern := filepath.Base(strings.Replace(outputTemplate, "%(ext)s", "*", 1))
	matches, err := safefs.Glob(filepath.Join(dir, pattern))
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("downloaded audio file not found matching template %s", outputTemplate)
	}
	return matches[0], nil
}

func buildFfmpegArgs(inputRaw, outputMP3 string) []string {
	return []string{
		"-y",
		"-threads", "0",
		"-i", inputRaw,
		"-vn",
		"-codec:a", "libmp3lame",
		"-b:a", "320k",
		"-ar", "44100",
		"-ac", "2",
		"-map_metadata", "-1",
		outputMP3,
	}
}

// TranscodeToMP3 writes stereo MP3 at 320 kbps and 44.1 kHz.
func TranscodeToMP3(ctx context.Context, inputRaw, outputMP3 string) error {
	args := buildFfmpegArgs(inputRaw, outputMP3)

	cmd := mediaexec.CommandContext(ctx, "ffmpeg", args...)
	defer cmd.Close()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg transcoding error: %w: %s", err, stderr.String())
	}

	info, err := safefs.Stat(outputMP3)
	if err != nil || info.Size() == 0 {
		return fmt.Errorf("transcoded MP3 is empty or missing")
	}

	return nil
}

type ffprobeFormatDurationOutput struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// validateDownloadedStreamAudio проверяет длительность скачанного файла по порогам короткого превью.
func validateDownloadedStreamAudio(ctx context.Context, rawFilePath string, expectedDurationSec int) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}

	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		rawFilePath,
	}

	cmd := mediaexec.CommandContext(ctx, "ffprobe", args...)
	defer cmd.Close()
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffprobe probe failed: %w", err)
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	var data ffprobeFormatDurationOutput
	if err := json.Unmarshal(out, &data); err != nil {
		return fmt.Errorf("failed to parse ffprobe json: %w", err)
	}

	durationStr := strings.TrimSpace(data.Format.Duration)
	if durationStr == "" {
		return nil // Неизвестная/пустая длительность: не блокируем пайплайн
	}

	actualDuration, err := strconv.ParseFloat(durationStr, 64)
	if err != nil || math.IsNaN(actualDuration) || math.IsInf(actualDuration, 0) || actualDuration <= 0 {
		return nil // Нечисловое значение, NaN/Inf или некорректная длительность: не блокируем пайплайн
	}

	// Файл короче 35 секунд при ожидаемых более 60 считаем вероятным превью.
	if actualDuration < 35.0 && expectedDurationSec > 60 {
		return fmt.Errorf("detected 30s preview snippet (actual: %.1fs, expected: %ds)", actualDuration, expectedDurationSec)
	}

	return nil
}
