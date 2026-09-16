package organizer

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeComponent(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		fallback string
		maxLen   int
		expected string
	}{
		{
			name:     "clean normal name",
			input:    "Greatest Hits",
			fallback: "Unknown",
			maxLen:   180,
			expected: "Greatest Hits",
		},
		{
			name:     "replace forbidden characters",
			input:    `AC/DC: What? * "Special" <Live> | Track`,
			fallback: "Unknown",
			maxLen:   180,
			expected: "AC_DC_ What_ _ _Special_ _Live_ _ Track",
		},
		{
			name:     "replace backslash and null byte with ampersand",
			input:    "Artist A\\Artist B\x00Artist C",
			fallback: "Unknown",
			maxLen:   180,
			expected: "Artist A & Artist B & Artist C",
		},
		{
			name:     "trim trailing dots and spaces",
			input:    "  Album Name...   ",
			fallback: "Unknown",
			maxLen:   180,
			expected: "Album Name",
		},
		{
			name:     "empty input returns fallback",
			input:    "   ",
			fallback: "Unknown Album",
			maxLen:   180,
			expected: "Unknown Album",
		},
		{
			name:     "truncate long title safely without breaking unicode",
			input:    strings.Repeat("ПриветМир", 30),
			fallback: "Unknown",
			maxLen:   20,
			expected: "ПриветМирПриветМирПр",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeComponent(tt.input, tt.fallback, tt.maxLen)
			if got != tt.expected {
				t.Errorf("SanitizeComponent(%q) = %q; want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestComputeTargetPath(t *testing.T) {
	tests := []struct {
		name         string
		artist       string
		album        string
		trackNum     int
		title        string
		origFilename string
		wantDir      string
		wantRelPath  string
	}{
		{
			name:         "normal album and track",
			artist:       "The Beatles",
			album:        "Abbey Road",
			trackNum:     1,
			title:        "Come Together",
			origFilename: "1.mp3",
			wantDir:      "Abbey Road - The Beatles",
			wantRelPath:  filepath.Join("Abbey Road - The Beatles", "01. Come Together.mp3"),
		},
		{
			name:         "single with empty album",
			artist:       "The Weeknd",
			album:        "",
			trackNum:     1,
			title:        "Blinding Lights",
			origFilename: "track.mp3",
			wantDir:      "Blinding Lights - The Weeknd",
			wantRelPath:  filepath.Join("Blinding Lights - The Weeknd", "01. Blinding Lights.mp3"),
		},
		{
			name:         "single with album named Single",
			artist:       "The Weeknd",
			album:        "Single",
			trackNum:     1,
			title:        "Starboy",
			origFilename: "01.mp3",
			wantDir:      "Starboy - The Weeknd",
			wantRelPath:  filepath.Join("Starboy - The Weeknd", "01. Starboy.mp3"),
		},
		{
			name:         "single with album named Singles (case-insensitive)",
			artist:       "Phil Collins",
			album:        "singles",
			trackNum:     0, // non-positive should default to 1
			title:        "01. 01. In the Air Tonight",
			origFilename: "track.mp3",
			wantDir:      "In the Air Tonight - Phil Collins",
			wantRelPath:  filepath.Join("In the Air Tonight - Phil Collins", "01. In the Air Tonight.mp3"),
		},
		{
			name:         "dangerous characters in album and title",
			artist:       "AC/DC",
			album:        "AC/DC Live?",
			trackNum:     5,
			title:        "Hells Bells: Special Edition",
			origFilename: "5.mp3",
			wantDir:      "AC_DC Live_ - AC_DC",
			wantRelPath:  filepath.Join("AC_DC Live_ - AC_DC", "05. Hells Bells_ Special Edition.mp3"),
		},
		{
			name:         "track title starting with numbers (21 Guns)",
			artist:       "Green Day",
			album:        "21st Century Breakdown",
			trackNum:     21,
			title:        "21 Guns",
			origFilename: "21. 21 Guns.mp3",
			wantDir:      "21st Century Breakdown - Green Day",
			wantRelPath:  filepath.Join("21st Century Breakdown - Green Day", "21. 21 Guns.mp3"),
		},
		{
			name:         "track title starting with year (1979)",
			artist:       "The Smashing Pumpkins",
			album:        "Mellon Collie",
			trackNum:     5,
			title:        "1979",
			origFilename: "05. 1979.mp3",
			wantDir:      "Mellon Collie - The Smashing Pumpkins",
			wantRelPath:  filepath.Join("Mellon Collie - The Smashing Pumpkins", "05. 1979.mp3"),
		},
		{
			name:         "track title with number prefix and digits title (01. 505)",
			artist:       "Arctic Monkeys",
			album:        "Favourite Worst Nightmare",
			trackNum:     12,
			title:        "12. 505",
			origFilename: "12. 505.mp3",
			wantDir:      "Favourite Worst Nightmare - Arctic Monkeys",
			wantRelPath:  filepath.Join("Favourite Worst Nightmare - Arctic Monkeys", "12. 505.mp3"),
		},
		{
			name:         "artist and track title starting with digit and dot (1.Kla$)",
			artist:       "1.Kla$",
			album:        "1.Kla$",
			trackNum:     1,
			title:        "01. 1.Kla$",
			origFilename: "01. 1.Kla$.mp3",
			wantDir:      "1.Kla$ - 1.Kla$",
			wantRelPath:  filepath.Join("1.Kla$ - 1.Kla$", "01. 1.Kla$.mp3"),
		},
		{
			name:         "single with 1.Kla$ and empty album",
			artist:       "1.Kla$",
			album:        "",
			trackNum:     1,
			title:        "1.Kla$",
			origFilename: "1.Kla$.mp3",
			wantDir:      "1.Kla$ - 1.Kla$",
			wantRelPath:  filepath.Join("1.Kla$ - 1.Kla$", "01. 1.Kla$.mp3"),
		},
		{
			name:         "artist and track title starting with 1.Царь",
			artist:       "1.Царь",
			album:        "Мой стиль",
			trackNum:     1,
			title:        "01. 1.Царь",
			origFilename: "01. 1.Царь.mp3",
			wantDir:      "Мой стиль - 1.Царь",
			wantRelPath:  filepath.Join("Мой стиль - 1.Царь", "01. 1.Царь.mp3"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			albumDir, trackRel := ComputeTargetPath(tt.artist, tt.album, tt.trackNum, tt.title, tt.origFilename)
			if albumDir != tt.wantDir {
				t.Errorf("ComputeTargetPath albumDir = %q; want %q", albumDir, tt.wantDir)
			}
			if trackRel != tt.wantRelPath {
				t.Errorf("ComputeTargetPath trackRel = %q; want %q", trackRel, tt.wantRelPath)
			}
		})
	}
}
