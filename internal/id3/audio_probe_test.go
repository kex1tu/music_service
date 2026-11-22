package id3_test

import (
	"os/exec"
	"path/filepath"
	"testing"

	"music-manager/internal/id3"
)

func TestReadTagsMeasuresImportedMP3(t *testing.T) {
	for _, binary := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s unavailable", binary)
		}
	}
	for _, tc := range []struct {
		name     string
		args     []string
		min, max int
	}{
		{"128k", []string{"-b:a", "128k"}, 128, 128},
		{"320k", []string{"-b:a", "320k"}, 320, 320},
		{"VBR", []string{"-q:a", "4"}, 16, 300},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.mp3")
			args := []string{"-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=10", "-c:a", "libmp3lame"}
			args = append(args, tc.args...)
			args = append(args, path)
			if out, err := exec.CommandContext(t.Context(), "ffmpeg", args...).CombinedOutput(); err != nil {
				t.Fatalf("generate MP3: %v %s", err, out)
			}
			tags, err := id3.ReadTags(path, "test.mp3")
			if err != nil {
				t.Fatal(err)
			}
			if tags.Duration < 10 || tags.Duration > 10.1 || tags.Bitrate < tc.min || tags.Bitrate > tc.max {
				t.Fatalf("duration=%v bitrate=%v", tags.Duration, tags.Bitrate)
			}
		})
	}
}
