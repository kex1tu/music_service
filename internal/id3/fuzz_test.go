package id3

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzParseAPIC(f *testing.F) {
	// Seed corpus with valid and edge case APIC data
	f.Add([]byte{0x00, 'i', 'm', 'a', 'g', 'e', '/', 'j', 'p', 'e', 'g', 0x00, 0x03, 'C', 'o', 'v', 'e', 'r', 0x00, 0xFF, 0xD8, 0xFF, 0xE0})
	f.Add([]byte{0x03, 'i', 'm', 'a', 'g', 'e', '/', 'p', 'n', 'g', 0x00, 0x03, 0x00, 0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A})
	f.Add([]byte{0x01, 0x00, 0x03, 0x00, 0x00, 0xFF, 0xD8})
	f.Add([]byte{0x00})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		// parseAPIC should never panic
		_ = parseAPIC(data)
	})
}

func FuzzDecodeText(f *testing.F) {
	f.Add([]byte{0x03, 'A', 'r', 't', 'i', 's', 't', 0x00, 'F', 'e', 'a', 't'})
	f.Add([]byte{0x00, 'T', 'e', 's', 't'})
	f.Add([]byte{0x01, 0xFF, 0xFE, 'T', 0x00, 'e', 0x00, 's', 0x00, 't', 0x00})
	f.Add([]byte{0x02, 0x00, 'T', 0x00, 'e', 0x00, 's', 0x00, 't'})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		// decodeText should never panic
		_ = decodeText(data)
	})
}

func FuzzReadTags(f *testing.F) {
	// Seed with valid ID3v2.4 header
	validHeader := []byte{
		'I', 'D', '3', 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x20,
		'T', 'I', 'T', '2', 0x00, 0x00, 0x00, 0x06, 0x00, 0x00, 0x03, 'S', 'o', 'n', 'g', 0x00,
	}
	f.Add(validHeader)
	f.Add([]byte("not an id3 file at all"))
	f.Add([]byte{'I', 'D', '3', 0x03, 0x00, 0x00, 0x7F, 0x7F, 0x7F, 0x7F})

	f.Fuzz(func(t *testing.T, data []byte) {
		tmpDir := t.TempDir()
		tmpFile := filepath.Join(tmpDir, "fuzz_track.mp3")
		if err := os.WriteFile(tmpFile, data, 0644); err != nil {
			return
		}

		// ReadTags and ReadCover should never panic regardless of file content
		_, _ = ReadTags(tmpFile, "fuzz_track.mp3")
		_, _ = ReadCover(tmpFile)
		_, _ = ReadRawFrames(tmpFile)
	})
}
