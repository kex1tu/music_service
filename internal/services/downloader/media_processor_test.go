package downloader

import (
	"testing"
)

func TestClassifyAudioQuality(t *testing.T) {
	tests := []struct {
		name         string
		codec        string
		bitrate      int
		cutoffHz     int
		expectedTier string
	}{
		{
			name:         "Lossless FLAC",
			codec:        "flac",
			bitrate:      900000,
			cutoffHz:     22000,
			expectedTier: "lossless",
		},
		{
			name:         "Lossless ALAC",
			codec:        "alac",
			bitrate:      800000,
			cutoffHz:     22050,
			expectedTier: "lossless",
		},
		{
			name:         "Lossless full spectrum high bitrate",
			codec:        "wav",
			bitrate:      1411000,
			cutoffHz:     22000,
			expectedTier: "lossless",
		},
		{
			name:         "True 320k high cutoff",
			codec:        "mp3",
			bitrate:      320000,
			cutoffHz:     20500,
			expectedTier: "true_320k",
		},
		{
			name:         "High Quality Opus (SoundCloud HQ)",
			codec:        "opus",
			bitrate:      160000,
			cutoffHz:     18500,
			expectedTier: "hq_opus",
		},
		{
			name:         "High Quality AAC",
			codec:        "aac",
			bitrate:      256000,
			cutoffHz:     18000,
			expectedTier: "hq_opus",
		},
		{
			name:         "Upscaled 128k (YouTube standard cut at 15.5kHz)",
			codec:        "aac",
			bitrate:      128000,
			cutoffHz:     15500,
			expectedTier: "upscaled_128k",
		},
		{
			name:         "Low bitrate MP3 (128k cut at 16kHz)",
			codec:        "mp3",
			bitrate:      128000,
			cutoffHz:     16000,
			expectedTier: "upscaled_128k",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := classifyAudioQuality(tc.codec, tc.bitrate, tc.cutoffHz)
			if res == nil {
				t.Fatalf("expected non-nil telemetry")
			}
			if res.QualityTier != tc.expectedTier {
				t.Errorf("expected tier %q, got %q (cutoff: %d, bitrate: %d)", tc.expectedTier, res.QualityTier, tc.cutoffHz, tc.bitrate)
			}
			if res.CutoffHz != tc.cutoffHz {
				t.Errorf("expected cutoff %d, got %d", tc.cutoffHz, res.CutoffHz)
			}
			if res.SourceBitrate != tc.bitrate {
				t.Errorf("expected bitrate %d, got %d", tc.bitrate, res.SourceBitrate)
			}
			if res.Description == "" {
				t.Errorf("expected non-empty description")
			}
		})
	}
}
