package downloader

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"

	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"music-manager/internal/core/mediaexec"
	"music-manager/internal/core/safefs"
	"music-manager/internal/id3"
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

func (s *Service) downloadAndValidateRawStream(ctx context.Context, sourceURL, outputTemplate string, expectedDurationSec int, onProgress func(float64)) (string, error) {
	rawFile, err := s.downloadRawStream(ctx, sourceURL, outputTemplate, onProgress)
	if err != nil {
		return "", err
	}
	if vErr := validateDownloadedStreamAudio(ctx, rawFile, expectedDurationSec); vErr != nil {
		_ = safefs.Remove(rawFile)
		return "", vErr
	}
	return rawFile, nil
}

func (s *Service) findDownloadedFile(outputTemplate string) (string, error) {
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

func (s *Service) transcodeToMP3(ctx context.Context, inputRaw, outputMP3 string) error {
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

func (s *Service) downloadCover(ctx context.Context, coverURL string) ([]byte, error) {
	if strings.TrimSpace(coverURL) == "" {
		return nil, errors.New("empty cover URL")
	}

	origURL, fallbackURL := resolver.YandexCoverCandidateURLs(coverURL)

	data, err := s.fetchCoverData(ctx, origURL)
	if err == nil && len(data) > 0 {
		return data, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if fallbackURL != "" && fallbackURL != origURL {
		fallbackData, fallbackErr := s.fetchCoverData(ctx, fallbackURL)
		if fallbackErr == nil && len(fallbackData) > 0 {
			return fallbackData, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("failed to download original (%v) and fallback 1000x1000 (%w)", err, fallbackErr)
	}

	return nil, err
}

func (s *Service) fetchCoverData(ctx context.Context, urlStr string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Del("Authorization")

	client := s.client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download cover: status %d", resp.StatusCode)
	}

	if resp.ContentLength > int64(id3.MaxCoverDownloadBytes) {
		return nil, fmt.Errorf("cover Content-Length %d exceeds maximum allowed %d bytes", resp.ContentLength, id3.MaxCoverDownloadBytes)
	}

	limitReader := io.LimitReader(resp.Body, int64(id3.MaxCoverDownloadBytes)+1)
	data, err := io.ReadAll(limitReader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > int64(id3.MaxCoverDownloadBytes) {
		return nil, fmt.Errorf("cover size exceeds maximum allowed %d bytes", id3.MaxCoverDownloadBytes)
	}
	if len(data) == 0 {
		return nil, errors.New("empty cover response")
	}

	return data, nil
}

var rolloffRegex = regexp.MustCompile(`lavfi\.aspectralstats(?:\.\d+)?\.rolloff=([0-9.]+)`)

type ffprobeFormatDurationOutput struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// validateDownloadedStreamAudio проверяет, что скачанный файл не является урезанным превью (сниппетом из-за гео-блокировки).
func validateDownloadedStreamAudio(ctx context.Context, rawFilePath string, expectedDurationSec int) error {
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

	// Если файл длится менее 35 секунд, а ожидаемый трек длиннее 60 секунд — это 30-секундный гео-сниппет
	if actualDuration < 35.0 && expectedDurationSec > 60 {
		return fmt.Errorf("detected 30s preview snippet (actual: %.1fs, expected: %ds)", actualDuration, expectedDurationSec)
	}

	return nil
}

type ffprobeAudioOutput struct {
	Streams []struct {
		CodecName  string `json:"codec_name"`
		BitRate    string `json:"bit_rate"`
		SampleRate string `json:"sample_rate"`
	} `json:"streams"`
	Format struct {
		BitRate string `json:"bit_rate"`
	} `json:"format"`
}

// probeAudioStream извлекает кодек, битрейт и частоту дискретизации исходного файла через ffprobe.
func probeAudioStream(ctx context.Context, inputRaw string) (codec string, bitrate int, sampleRate int, err error) {
	args := []string{
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=codec_name,bit_rate,sample_rate:format=bit_rate",
		"-of", "json",
		inputRaw,
	}

	cmd := mediaexec.CommandContext(ctx, "ffprobe", args...)
	defer cmd.Close()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if runErr := cmd.Run(); runErr != nil {
		return "", 0, 0, fmt.Errorf("ffprobe failed: %w: %s", runErr, stderr.String())
	}

	var out ffprobeAudioOutput
	if jsonErr := json.Unmarshal(stdout.Bytes(), &out); jsonErr != nil {
		return "", 0, 0, fmt.Errorf("failed to parse ffprobe json: %w", jsonErr)
	}

	if len(out.Streams) > 0 {
		st := out.Streams[0]
		codec = strings.ToLower(strings.TrimSpace(st.CodecName))
		if br, brErr := strconv.Atoi(st.BitRate); brErr == nil && br > 0 {
			bitrate = br
		}
		if sr, srErr := strconv.Atoi(st.SampleRate); srErr == nil && sr > 0 {
			sampleRate = sr
		}
	}

	if bitrate == 0 && out.Format.BitRate != "" {
		if br, brErr := strconv.Atoi(out.Format.BitRate); brErr == nil && br > 0 {
			bitrate = br
		}
	}

	if codec == "" {
		codec = "unknown"
	}

	return codec, bitrate, sampleRate, nil
}

// measureSpectralCutoff замеряет спектральный срез частот (rolloff) в герцах через ffmpeg aspectralstats.
func measureSpectralCutoff(ctx context.Context, inputRaw string) (int, error) {
	// Сначала пробуем сделать 5-секундный срез из середины аудио (-ss 15 -t 5)
	args := []string{
		"-v", "error",
		"-ss", "15",
		"-t", "5",
		"-i", inputRaw,
		"-vn",
		"-af", "aspectralstats=measure=rolloff,ametadata=print:key=lavfi.aspectralstats.1.rolloff:file=-",
		"-f", "null",
		"-",
	}

	cmd := mediaexec.CommandContext(ctx, "ffmpeg", args...)
	defer cmd.Close()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if runErr := cmd.Run(); runErr != nil || stdout.Len() == 0 {
		// Фоллбэк: если трек короче 15 секунд, замеряем с начала (-ss 0 -t 5)
		stdout.Reset()
		stderr.Reset()
		argsRetry := []string{
			"-v", "error",
			"-ss", "0",
			"-t", "5",
			"-i", inputRaw,
			"-vn",
			"-af", "aspectralstats=measure=rolloff,ametadata=print:key=lavfi.aspectralstats.1.rolloff:file=-",
			"-f", "null",
			"-",
		}
		cmdRetry := mediaexec.CommandContext(ctx, "ffmpeg", argsRetry...)
		defer cmdRetry.Close()
		cmdRetry.Stdout = &stdout
		cmdRetry.Stderr = &stderr
		if rErr := cmdRetry.Run(); rErr != nil {
			return 0, fmt.Errorf("ffmpeg aspectralstats failed: %w: %s", rErr, stderr.String())
		}
	}

	var values []float64
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		line := scanner.Text()
		matches := rolloffRegex.FindStringSubmatch(line)
		if len(matches) >= 2 {
			if val, pErr := strconv.ParseFloat(matches[1], 64); pErr == nil && val > 0 && val <= 24000 {
				values = append(values, val)
			}
		}
	}

	if len(values) == 0 {
		return 0, fmt.Errorf("no spectral rolloff values collected")
	}

	// Сортируем и берем 90-й перцентиль собранных значений (отсекая затухания и тихие паузы)
	sort.Float64s(values)
	p90Idx := int(float64(len(values)) * 0.90)
	if p90Idx >= len(values) {
		p90Idx = len(values) - 1
	}

	return int(values[p90Idx]), nil
}

// classifyAudioQuality классифицирует качество исходного потока и формирует человекочитаемое описание.
func classifyAudioQuality(codec string, bitrate int, cutoffHz int) *resolver.AudioQualityTelemetry {
	normCodec := strings.ToLower(strings.TrimSpace(codec))
	if normCodec == "" {
		normCodec = "audio"
	}

	displayCodec := strings.ToUpper(normCodec)
	brKbps := bitrate / 1000

	telemetry := &resolver.AudioQualityTelemetry{
		SourceCodec:   normCodec,
		SourceBitrate: bitrate,
		CutoffHz:      cutoffHz,
	}

	cutoffKhz := float64(cutoffHz) / 1000.0

	// 1. Lossless / Flac
	if normCodec == "flac" || normCodec == "alac" || (cutoffHz >= 21000 && brKbps >= 500) {
		telemetry.QualityTier = "lossless"
		telemetry.Description = fmt.Sprintf("Lossless %s • Полный спектр (~%.1f kHz)", displayCodec, cutoffKhz)
		return telemetry
	}

	// 2. True 320k / Hi-Fi
	if cutoffHz >= 19500 && (brKbps >= 256 || brKbps == 0) {
		telemetry.QualityTier = "true_320k"
		if brKbps > 0 {
			telemetry.Description = fmt.Sprintf("Честные 320k (%s %dk) • Срез ~%.1f kHz", displayCodec, brKbps, cutoffKhz)
		} else {
			telemetry.Description = fmt.Sprintf("Честные 320k (%s) • Срез ~%.1f kHz", displayCodec, cutoffKhz)
		}
		return telemetry
	}

	// 3. High Quality Opus / AAC (SoundCloud HQ, etc.)
	if cutoffHz >= 17000 || normCodec == "opus" {
		telemetry.QualityTier = "hq_opus"
		if brKbps > 0 {
			telemetry.Description = fmt.Sprintf("Высокое качество (%s %dk) • Срез ~%.1f kHz", displayCodec, brKbps, cutoffKhz)
		} else {
			telemetry.Description = fmt.Sprintf("Высокое качество (%s) • Срез ~%.1f kHz", displayCodec, cutoffKhz)
		}
		return telemetry
	}

	// 4. Low Bitrate / Upscaled (YouTube 128k, etc.)
	telemetry.QualityTier = "upscaled_128k"
	if brKbps > 0 {
		telemetry.Description = fmt.Sprintf("Апскейл до 320k (исходник %s %dk, срез ~%.1f kHz)", displayCodec, brKbps, cutoffKhz)
	} else {
		telemetry.Description = fmt.Sprintf("Апскейл до 320k (исходник %s, срез ~%.1f kHz)", displayCodec, cutoffKhz)
	}

	return telemetry
}

// analyzeAudioQuality оркестрирует спектральный и кодековый анализ аудиопотока.
func (s *Service) analyzeAudioQuality(ctx context.Context, inputRaw string) *resolver.AudioQualityTelemetry {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	codec, bitrate, _, pErr := probeAudioStream(probeCtx, inputRaw)
	if pErr != nil {
		codec = "audio"
	}

	cutoffHz, cErr := measureSpectralCutoff(probeCtx, inputRaw)
	if cErr != nil {
		// Если замер спектра завершился с ошибкой, формируем базовую оценку по кодеку и битрейту
		if bitrate >= 256000 {
			cutoffHz = 20000
		} else if strings.EqualFold(codec, "opus") || bitrate >= 160000 {
			cutoffHz = 18500
		} else {
			cutoffHz = 15500
		}
	}

	return classifyAudioQuality(codec, bitrate, cutoffHz)
}
