package id3

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"strconv"
	"time"

	"music-manager/internal/core/mediaexec"
)

// Probe the already opened file. A path lookup here could measure a different
// file from the one whose tags we just read.
func probeMP3(ctx context.Context, file *os.File, tags *TrackTags) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := mediaexec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-protocol_whitelist", "file", "-f", "mp3", "-select_streams", "a:0",
		"-show_entries", "stream=duration,bit_rate", "-of", "json", "file:/proc/self/fd/3")
	cmd.ExtraFiles = []*os.File{file}
	out, err := cmd.Output()
	if err != nil {
		return
	}

	var result struct {
		Streams []struct {
			Duration string `json:"duration"`
			Bitrate  string `json:"bit_rate"`
		} `json:"streams"`
	}
	if json.Unmarshal(out, &result) != nil || len(result.Streams) != 1 {
		return
	}
	stream := result.Streams[0]
	if seconds, err := strconv.ParseFloat(stream.Duration, 64); err == nil && seconds > 0 && !math.IsInf(seconds, 0) {
		tags.Duration = seconds
	}
	if bits, err := strconv.Atoi(stream.Bitrate); err == nil && bits > 0 {
		tags.Bitrate = int(math.Round(float64(bits) / 1000))
	}
}
