package organizer

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestTruncateToByteBudget(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxBytes int
		expected string
	}{
		{"empty string", "", 10, ""},
		{"zero budget", "Hello", 0, ""},
		{"negative budget", "Hello", -5, ""},
		{"ascii within budget", "Hello", 10, "Hello"},
		{"ascii exact budget", "Hello", 5, "Hello"},
		{"ascii truncated", "Hello World", 5, "Hello"},
		{"cyrillic within budget", "Привет", 12, "Привет"},
		{"cyrillic exact budget", "Привет", 12, "Привет"},
		{"cyrillic odd budget (do not split rune)", "Привет", 3, "П"},
		{"cyrillic even budget truncated", "Привет", 4, "Пр"},
		{"cyrillic budget 1 byte (cannot fit 2-byte rune)", "Привет", 1, ""},
		{"emoji 4-byte within budget", "🎧🔥", 8, "🎧🔥"},
		{"emoji 4-byte partial budget (3 bytes)", "🎧", 3, ""},
		{"emoji 4-byte exact budget (4 bytes)", "🎧", 4, "🎧"},
		{"emoji 4-byte + ascii truncated", "🎧Music", 6, "🎧Mu"},
		{"mixed ascii cyrillic emoji", "Hi Привет 🎵 Rock", 11, "Hi Прив"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateToByteBudget(tt.input, tt.maxBytes)
			if got != tt.expected {
				t.Errorf("TruncateToByteBudget(%q, %d) = %q (len %d); want %q (len %d)",
					tt.input, tt.maxBytes, got, len(got), tt.expected, len(tt.expected))
			}
			if len(got) > tt.maxBytes && tt.maxBytes > 0 {
				t.Errorf("len(got) = %d > maxBytes = %d", len(got), tt.maxBytes)
			}
			if !utf8.ValidString(got) {
				t.Errorf("result is not valid UTF-8: %x", got)
			}
		})
	}
}

func TestSanitizeComponent_UTF8ByteLimit(t *testing.T) {
	t.Run("long Cyrillic truncated to 255 bytes", func(t *testing.T) {
		input := strings.Repeat("ОченьДлинноеНазваниеАльбомаИлиТрека", 15) // 525 runes, 1050 bytes
		got := SanitizeComponent(input, "Fallback", 180)
		if len(got) > MaxComponentBytes {
			t.Errorf("len(got) = %d > %d", len(got), MaxComponentBytes)
		}
		if !utf8.ValidString(got) {
			t.Errorf("result is not valid UTF-8")
		}
		if strings.HasSuffix(got, ".") || strings.HasSuffix(got, " ") {
			t.Errorf("result has trailing dot or space: %q", got)
		}
	})

	t.Run("long Emoji truncated to 255 bytes", func(t *testing.T) {
		input := strings.Repeat("🎧🔥🎵🚀✨", 25) // 125 emojis = 500 bytes
		got := SanitizeComponent(input, "Fallback", 180)
		if len(got) > MaxComponentBytes {
			t.Errorf("len(got) = %d > %d", len(got), MaxComponentBytes)
		}
		if !utf8.ValidString(got) {
			t.Errorf("result is not valid UTF-8")
		}
	})

	t.Run("long ASCII truncated to 180 runes and 255 bytes", func(t *testing.T) {
		input := strings.Repeat("VeryLongTrackName", 20) // 340 chars
		got := SanitizeComponent(input, "Fallback", 180)
		if len(got) > 180 {
			t.Errorf("len(got) = %d > 180 runes", len(got))
		}
		if len(got) > MaxComponentBytes {
			t.Errorf("len(got) = %d > %d bytes", len(got), MaxComponentBytes)
		}
	})

	t.Run("empty input with long fallback", func(t *testing.T) {
		longFallback := strings.Repeat("ДлинныйФолбэк", 30) // 390 runes, 780 bytes
		got := SanitizeComponent("", longFallback, 180)
		if len(got) > MaxComponentBytes {
			t.Errorf("len(got) = %d > %d", len(got), MaxComponentBytes)
		}
		if !utf8.ValidString(got) {
			t.Errorf("result is not valid UTF-8")
		}
	})

	t.Run("small and boundary budgets", func(t *testing.T) {
		got0 := SanitizeComponentWithBudget("Привет Мир", "Fallback", 100, 0)
		if got0 != "" {
			t.Errorf("budget 0 should return empty string, got %q", got0)
		}

		gotNeg := SanitizeComponentWithBudget("Привет Мир", "Fallback", 100, -10)
		if gotNeg != "" {
			t.Errorf("negative budget should return empty string, got %q", gotNeg)
		}

		gotSmall := SanitizeComponentWithBudget("Привет Мир", "Fallback", 100, 5)
		if len(gotSmall) > 5 {
			t.Errorf("len(gotSmall) = %d > 5", len(gotSmall))
		}
		if !utf8.ValidString(gotSmall) {
			t.Errorf("result is not valid UTF-8: %x", gotSmall)
		}
	})

	t.Run("trailing dots and spaces trimmed after truncation", func(t *testing.T) {
		input := "Привет Мир.....   "
		got := SanitizeComponent(input, "Fallback", 180)
		if got != "Привет Мир" {
			t.Errorf("got %q; want %q", got, "Привет Мир")
		}
	})
}

func TestComputeAlbumDir_LongPartsAndBudget(t *testing.T) {
	t.Run("catalog of two long parts (both album and artist long)", func(t *testing.T) {
		longAlbum := strings.Repeat("ДлинныйАльбомНомерОдин", 15) // 330 runes, 660 bytes
		longArtist := strings.Repeat("ОченьДлинныйИсполнитель", 15) // 345 runes, 690 bytes

		dir := ComputeAlbumDir(longArtist, longAlbum)

		if len(dir) > MaxComponentBytes {
			t.Errorf("len(dir) = %d > MaxComponentBytes = %d", len(dir), MaxComponentBytes)
		}
		if !utf8.ValidString(dir) {
			t.Errorf("dir is not valid UTF-8")
		}
		if !strings.Contains(dir, " - ") {
			t.Errorf("dir missing separator ' - ': %q", dir)
		}
		if strings.HasSuffix(dir, ".") || strings.HasSuffix(dir, " ") {
			t.Errorf("dir has trailing dot or space: %q", dir)
		}

		// Verify both parts are represented
		parts := strings.Split(dir, " - ")
		if len(parts) < 2 {
			t.Fatalf("expected 2 parts split by ' - ', got %d: %q", len(parts), dir)
		}
		if !strings.HasPrefix(parts[0], "ДлинныйАльбом") {
			t.Errorf("album part should start with 'ДлинныйАльбом', got %q", parts[0])
		}
		if !strings.HasPrefix(parts[1], "ОченьДлинный") {
			t.Errorf("artist part should start with 'ОченьДлинный', got %q", parts[1])
		}
	})

	t.Run("catalog of two long parts with emoji", func(t *testing.T) {
		longAlbum := strings.Repeat("🎧Album🔥", 20) // 160 runes, 480 bytes
		longArtist := strings.Repeat("🎵Artist🚀", 20) // 180 runes, 520 bytes

		dir := ComputeAlbumDir(longArtist, longAlbum)
		if len(dir) > MaxComponentBytes {
			t.Errorf("len(dir) = %d > MaxComponentBytes = %d", len(dir), MaxComponentBytes)
		}
		if !utf8.ValidString(dir) {
			t.Errorf("dir is not valid UTF-8")
		}
		if !strings.Contains(dir, " - ") {
			t.Errorf("dir missing separator ' - ': %q", dir)
		}
	})

	t.Run("short album and artist remain unchanged", func(t *testing.T) {
		dir := ComputeAlbumDir("The Beatles", "Abbey Road")
		want := "Abbey Road - The Beatles"
		if dir != want {
			t.Errorf("ComputeAlbumDir() = %q; want %q", dir, want)
		}
	})

	t.Run("one long part and one short part", func(t *testing.T) {
		shortArtist := "Кино"
		longAlbum := strings.Repeat("ЗвездаПоИмениСолнце", 20) // 400 runes, 800 bytes

		dir := ComputeAlbumDir(shortArtist, longAlbum)
		if len(dir) > MaxComponentBytes {
			t.Errorf("len(dir) = %d > MaxComponentBytes = %d", len(dir), MaxComponentBytes)
		}
		if !strings.HasSuffix(dir, " - Кино") {
			t.Errorf("short artist should be preserved in full: %q", dir)
		}
	})

	t.Run("boundary budgets for ComputeAlbumDirWithBudget", func(t *testing.T) {
		dir0 := ComputeAlbumDirWithBudget("Artist", "Album", 0)
		if dir0 != "" {
			t.Errorf("budget 0 should return empty, got %q", dir0)
		}

		dirSmall := ComputeAlbumDirWithBudget("Artist", "Album", 4)
		if len(dirSmall) > 4 {
			t.Errorf("len(dirSmall) = %d > 4", len(dirSmall))
		}
		if !utf8.ValidString(dirSmall) {
			t.Errorf("dirSmall is not valid UTF-8")
		}

		dir15 := ComputeAlbumDirWithBudget("Artist", "Album", 15)
		if len(dir15) > 15 {
			t.Errorf("len(dir15) = %d > 15", len(dir15))
		}
		if !utf8.ValidString(dir15) {
			t.Errorf("dir15 is not valid UTF-8")
		}
	})
}

func TestComputeTargetPath_UTF8Budget(t *testing.T) {
	t.Run("very long Cyrillic title", func(t *testing.T) {
		longTitle := strings.Repeat("ОченьДлинноеНазваниеТрекаНаКириллице", 15) // 540 runes, 1080 bytes
		albumDir, trackRel := ComputeTargetPath("Исполнитель", "Альбом", 1, longTitle, "1.mp3")

		filename := filepath.Base(trackRel)
		if len(filename) > MaxComponentBytes {
			t.Errorf("len(filename) = %d > MaxComponentBytes = %d", len(filename), MaxComponentBytes)
		}
		if len(albumDir) > MaxComponentBytes {
			t.Errorf("len(albumDir) = %d > MaxComponentBytes = %d", len(albumDir), MaxComponentBytes)
		}
		if !strings.HasSuffix(filename, ".mp3") {
			t.Errorf("filename should end with .mp3, got %q", filename)
		}
		if !strings.HasPrefix(filename, "01. ") {
			t.Errorf("filename should start with '01. ', got %q", filename)
		}
		if !utf8.ValidString(filename) {
			t.Errorf("filename is not valid UTF-8: %x", filename)
		}
	})

	t.Run("very long Emoji title", func(t *testing.T) {
		longEmojiTitle := strings.Repeat("🎧🔥🎵🚀💥", 30) // 150 emojis, 600 bytes
		albumDir, trackRel := ComputeTargetPath("Artist", "Album", 5, longEmojiTitle, "5.mp3")

		filename := filepath.Base(trackRel)
		if len(filename) > MaxComponentBytes {
			t.Errorf("len(filename) = %d > MaxComponentBytes = %d", len(filename), MaxComponentBytes)
		}
		if len(albumDir) > MaxComponentBytes {
			t.Errorf("len(albumDir) = %d > MaxComponentBytes = %d", len(albumDir), MaxComponentBytes)
		}
		if !strings.HasSuffix(filename, ".mp3") {
			t.Errorf("filename should end with .mp3, got %q", filename)
		}
		if !utf8.ValidString(filename) {
			t.Errorf("filename is not valid UTF-8")
		}
	})

	t.Run("title ending in .mp3 does not create duplicate extension", func(t *testing.T) {
		_, trackRel := ComputeTargetPath("Artist", "Album", 1, "Song Title.mp3", "1.mp3")
		filename := filepath.Base(trackRel)
		if filename != "01. Song Title.mp3" {
			t.Errorf("filename = %q; want '01. Song Title.mp3'", filename)
		}
	})

	t.Run("title ending in uppercase .MP3", func(t *testing.T) {
		_, trackRel := ComputeTargetPath("Artist", "Album", 2, "Song Title.MP3", "2.mp3")
		filename := filepath.Base(trackRel)
		if filename != "02. Song Title.mp3" {
			t.Errorf("filename = %q; want '02. Song Title.mp3'", filename)
		}
	})

	t.Run("both long album, long artist, and long title", func(t *testing.T) {
		longArtist := strings.Repeat("Артист", 30)
		longAlbum := strings.Repeat("Альбом", 30)
		longTitle := strings.Repeat("Песня", 40)

		albumDir, trackRel := ComputeTargetPath(longArtist, longAlbum, 10, longTitle, "10.mp3")
		filename := filepath.Base(trackRel)

		if len(albumDir) > MaxComponentBytes {
			t.Errorf("albumDir len = %d > 255", len(albumDir))
		}
		if len(filename) > MaxComponentBytes {
			t.Errorf("filename len = %d > 255", len(filename))
		}
		if !utf8.ValidString(albumDir) || !utf8.ValidString(filename) {
			t.Errorf("path components must be valid UTF-8")
		}
	})

	t.Run("boundary budgets in ComputeTargetPathWithBudget", func(t *testing.T) {
		_, rel0 := ComputeTargetPathWithBudget("Artist", "Album", 1, "Title", "1.mp3", 0)
		if rel0 != "" {
			t.Errorf("expected empty relPath for budget 0, got %q", rel0)
		}

		_, relSmall := ComputeTargetPathWithBudget("Artist", "Album", 1, "Title", "1.mp3", 10)
		filenameSmall := filepath.Base(relSmall)
		if len(filenameSmall) > 10 {
			t.Errorf("len(filenameSmall) = %d > 10", len(filenameSmall))
		}
	})
}
