package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func (s *Service) analyzeAudioQuality(ctx context.Context, inputRaw string) *resolver.AudioQualityTelemetry {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := mediaexec.CommandContext(probeCtx, "ffprobe",
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=codec_name,bit_rate:format=bit_rate",
		"-of", "json",
		inputRaw,
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run()

	codec := "audio"
	bitrate := 0
	var out struct {
		Streams []struct {
			CodecName string `json:"codec_name"`
			BitRate   string `json:"bit_rate"`
		} `json:"streams"`
		Format struct {
			BitRate string `json:"bit_rate"`
		} `json:"format"`
	}
	if json.Unmarshal(stdout.Bytes(), &out) == nil {
		if len(out.Streams) > 0 {
			codec = strings.ToLower(out.Streams[0].CodecName)
			if b, err := strconv.Atoi(out.Streams[0].BitRate); err == nil {
				bitrate = b
			}
		}
		if bitrate == 0 && out.Format.BitRate != "" {
			if b, err := strconv.Atoi(out.Format.BitRate); err == nil {
				bitrate = b
			}
		}
	}

	cutoffHz := 18500
	if strings.EqualFold(codec, "opus") || bitrate >= 160000 {
		cutoffHz = 19500
	} else if bitrate < 140000 && bitrate > 0 {
		cutoffHz = 15500
	}

	telemetry := &resolver.AudioQualityTelemetry{
		SourceCodec:   codec,
		SourceBitrate: bitrate,
		CutoffHz:      cutoffHz,
	}

	if cutoffHz >= 19500 {
		telemetry.QualityTier = "true_320k"
		telemetry.Description = fmt.Sprintf("Честные 320k (%s) • Срез ~%.1f kHz", strings.ToUpper(codec), float64(cutoffHz)/1000.0)
	} else {
		telemetry.QualityTier = "hq_opus"
		telemetry.Description = fmt.Sprintf("Высокое качество (%s) • Срез ~%.1f kHz", strings.ToUpper(codec), float64(cutoffHz)/1000.0)
	}

	return telemetry
}
