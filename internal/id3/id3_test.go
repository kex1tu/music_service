package id3

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanTitle(t *testing.T) {
	tests := []struct {
		title    string
		fallback string
		expected string
	}{
		// 1. Устранение каскада префиксов
		{"05. 05. 05. Похрюкай", "05. 05. 05. Похрюкай.mp3", "Похрюкай"},
		{"01. Track Title", "01. Track Title.mp3", "Track Title"},
		{"12 - Song", "12 - Song.mp3", "Song"},

		// 2. Устранение бага с утечкой TALB
		{"ПоTALB & _ & _", "05. 05. 05. Похрюкай.mp3", "Похрюкай"},
		{"Track TIT2 leaked", "02. Real Name.mp3", "Real Name"},

		// 3. Нормальные заголовки
		{"Clean Title", "Clean Title.mp3", "Clean Title"},
		{"", "07. Fallback.mp3", "Fallback"},

		// 4. Названия с цифрами в начале
		{"21 Guns", "01. 21 Guns.mp3", "21 Guns"},
		{"1979", "05. 1979.mp3", "1979"},
		{"505", "12 - 505.mp3", "505"},
		{"7 rings", "01. 7 rings.mp3", "7 rings"},
		{"1984", "04. 1984.mp3", "1984"},
		{"24K Magic", "01. 24K Magic.mp3", "24K Magic"},
		{"30 Seconds To Mars", "01. 30 Seconds To Mars.mp3", "30 Seconds To Mars"},
		{"01. 21 Guns", "01. 21 Guns.mp3", "21 Guns"},
		{"02 - 505", "02 - 505.mp3", "505"},
		{"01. 01. 21 Guns", "01. 01. 21 Guns.mp3", "21 Guns"},

		// 5. Названия и исполнители с точками после цифр без пробела (1.Kla$, 1.Царь, 3.14)
		{"1.Kla$", "01. 1.Kla$.mp3", "1.Kla$"},
		{"1.Kla$", "1.Kla$.mp3", "1.Kla$"},
		{"01. 1.Kla$", "01. 1.Kla$.mp3", "1.Kla$"},
		{"01. 01. 1.Kla$", "01. 01. 1.Kla$.mp3", "1.Kla$"},
		{"1. 1.Kla$", "01. 1.Kla$.mp3", "1.Kla$"},
		{"1.Царь", "01. 1.Царь.mp3", "1.Царь"},
		{"01. 1.Царь", "01. 1.Царь.mp3", "1.Царь"},
		{"3.14", "01. 3.14.mp3", "3.14"},
		{"01. 3.14", "01. 3.14.mp3", "3.14"},
	}

	for _, tc := range tests {
		got := CleanTitle(tc.title, tc.fallback)
		if got != tc.expected {
			t.Errorf("CleanTitle('%s', '%s'): expected '%s', got '%s'", tc.title, tc.fallback, tc.expected, got)
		}
	}
}

func TestParseGenresAndMultiValue(t *testing.T) {
	genres := ParseGenres("Alternative\\\\Rock\\\\Indie Rock\\\\Pop-Rock")
	expected := []string{"Alternative", "Rock", "Indie Rock", "Pop-Rock"}
	if len(genres) != len(expected) {
		t.Fatalf("expected %d genres, got %d: %v", len(expected), len(genres), genres)
	}
	for i, exp := range expected {
		if genres[i] != exp {
			t.Errorf("expected genre[%d] = %q, got %q", i, exp, genres[i])
		}
	}

	// Slashes, semicolons, commas, single backslashes
	g2 := ParseGenres("Alternative / Rock; Indie Rock, Pop-Rock\\Metal\x00Punk")
	exp2 := []string{"Alternative", "Rock", "Indie Rock", "Pop-Rock", "Metal", "Punk"}
	if len(g2) != len(exp2) {
		t.Fatalf("expected %d genres, got %d: %v", len(exp2), len(g2), g2)
	}
	for i, exp := range exp2 {
		if g2[i] != exp {
			t.Errorf("expected g2[%d] = %q, got %q", i, exp, g2[i])
		}
	}

	// Artist splitting
	artists := ParseMultiValue("1.Kla$\\\\1.Царь\x00Czar; Shock")
	expArtists := []string{"1.Kla$", "1.Царь", "Czar", "Shock"}
	if len(artists) != len(expArtists) {
		t.Fatalf("expected %d artists, got %d: %v", len(expArtists), len(artists), artists)
	}
	for i, exp := range expArtists {
		if artists[i] != exp {
			t.Errorf("expected artist[%d] = %q, got %q", i, exp, artists[i])
		}
	}
}

func TestWriteAndReadTags_Roundtrip(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "id3_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	filePath := filepath.Join(tempDir, "test_track.mp3")

	// Создаем фейковый MP3 файл с аудиофреймами
	fakeAudio := make([]byte, 10000)
	for i := range fakeAudio {
		fakeAudio[i] = 0x55
	}
	if err := os.WriteFile(filePath, fakeAudio, 0644); err != nil {
		t.Fatal(err)
	}

	// Записываем ID3v2.4 теги
	writeTags := TrackTags{
		Title:       "Похрюкай",
		Artist:      "МЭЙБИ БЭЙБИ\\\\Дора",
		Album:       "МЭЙБИЛЭНД",
		AlbumArtist: "МЭЙБИ БЭЙБИ",
		Year:        2022,
		Genre:       "Pop\\\\Rock",
		TrackNumber: 5,
		TrackTotal:  16,
		DiscNumber:  1,
		DiscTotal:   1,
	}

	cover := &CoverInfo{
		MimeType: "image/jpeg",
		Data:     []byte("\xFF\xD8\xFF\xE0fake_jpeg_data"),
	}

	if err := WriteTags(filePath, writeTags, cover); err != nil {
		t.Fatalf("WriteTags failed: %v", err)
	}

	// Считываем теги обратно
	readTags, err := ReadTags(filePath, "test_track.mp3")
	if err != nil {
		t.Fatalf("ReadTags failed: %v", err)
	}

	if readTags.Title != "Похрюкай" {
		t.Errorf("expected Title 'Похрюкай', got '%s'", readTags.Title)
	}
	if readTags.Artist != "МЭЙБИ БЭЙБИ\\\\Дора" {
		t.Errorf("expected Artist 'МЭЙБИ БЭЙБИ\\\\Дора', got '%s'", readTags.Artist)
	}
	if readTags.Album != "МЭЙБИЛЭНД" {
		t.Errorf("expected Album 'МЭЙБИЛЭНД', got '%s'", readTags.Album)
	}
	if readTags.TrackNumber != 5 || readTags.TrackTotal != 16 {
		t.Errorf("expected Track 5/16, got %d/%d", readTags.TrackNumber, readTags.TrackTotal)
	}
	if readTags.Year != 2022 {
		t.Errorf("expected Year 2022, got %d", readTags.Year)
	}
	if !readTags.HasCover {
		t.Errorf("expected HasCover=true")
	}

	// Считываем обложку
	readCover, err := ReadCover(filePath)
	if err != nil {
		t.Fatalf("ReadCover failed: %v", err)
	}
	if readCover == nil || string(readCover.Data) != string(cover.Data) {
		t.Errorf("cover data mismatch")
	}
}
