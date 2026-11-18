package downloader

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"music-manager/internal/core/mediaexec"
	"music-manager/internal/services/downloadmedia"
	"music-manager/internal/services/resolver"
)

func (s *Service) downloadAndValidateRawStream(ctx context.Context, sourceURL, outputTemplate string, expectedDurationSec int, onProgress func(float64)) (string, error) {
	return downloadmedia.DownloadAndValidateAudio(ctx, s.cookiesPath, sourceURL, outputTemplate, expectedDurationSec, onProgress)
}

func (s *Service) transcodeToMP3(ctx context.Context, inputRaw, outputMP3 string) error {
	return downloadmedia.TranscodeToMP3(ctx, inputRaw, outputMP3)
}

func (s *Service) downloadCover(ctx context.Context, coverURL string) ([]byte, error) {
	return downloadmedia.DownloadCover(ctx, s.client, coverURL)
}

var rolloffRegex = regexp.MustCompile(`lavfi\.aspectralstats(?:\.\d+)?\.rolloff=([0-9.]+)`)

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
