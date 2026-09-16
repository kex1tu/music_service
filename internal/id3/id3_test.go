package id3

import (
	"bytes"
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
	}

	for _, tc := range tests {
		got := CleanTitle(tc.title, tc.fallback)
		if got != tc.expected {
			t.Errorf("CleanTitle('%s', '%s'): expected '%s', got '%s'", tc.title, tc.fallback, tc.expected, got)
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

func TestWriteTags_PreservesUnknownFrames(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "id3_preserve_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	filePath := filepath.Join(tempDir, "test_preserve.mp3")
	fakeAudio := make([]byte, 5000)
	for i := range fakeAudio {
		fakeAudio[i] = 0xAA
	}
	if err := os.WriteFile(filePath, fakeAudio, 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Записываем первичные теги
	initialTags := TrackTags{
		Title:       "Initial Song",
		Artist:      "Original Artist",
		Album:       "Some Album",
		TrackNumber: 1,
	}
	if err := WriteTags(filePath, initialTags, nil); err != nil {
		t.Fatalf("initial WriteTags failed: %v", err)
	}

	// 2. Внедряем кастомный фрейм TCOP (Copyright) и USLT (текст)
	frames, err := ReadRawFrames(filePath)
	if err != nil {
		t.Fatalf("ReadRawFrames failed: %v", err)
	}

	frames = append(frames, RawFrame{
		ID:    "TCOP",
		Flags: [2]byte{0, 0},
		Data:  append([]byte{3}, []byte("2026 Record Label Corp.")...),
	})
	frames = append(frames, RawFrame{
		ID:    "USLT",
		Flags: [2]byte{0, 0},
		Data:  append([]byte{3}, []byte("eng\x00\x00Embedded lyrics should be stripped")...),
	})

	var framesBuf bytes.Buffer
	for _, f := range frames {
		writeRawFrame(&framesBuf, f)
	}
	framesBytes := framesBuf.Bytes()
	totalTagLen := len(framesBytes) + 512
	var id3Hdr [10]byte
	copy(id3Hdr[0:3], "ID3")
	id3Hdr[3] = 4
	encodeSynchsafe(totalTagLen, id3Hdr[6:10])

	fileData, _ := os.ReadFile(filePath)
	audioOffset := 0
	if string(fileData[:3]) == "ID3" {
		tagSize := decodeSynchsafe(fileData[6:10])
		audioOffset = 10 + tagSize
	}
	var newContent []byte
	newContent = append(newContent, id3Hdr[:]...)
	newContent = append(newContent, framesBytes...)
	newContent = append(newContent, make([]byte, 512)...)
	newContent = append(newContent, fileData[audioOffset:]...)
	if err := os.WriteFile(filePath, newContent, 0644); err != nil {
		t.Fatal(err)
	}

	// 3. Вызываем WriteTags с обновленным названием трека
	updatedTags := TrackTags{
		Title:       "Updated Song Title",
		Artist:      "Updated Artist",
		Album:       "Some Album",
		TrackNumber: 1,
	}
	if err := WriteTags(filePath, updatedTags, nil); err != nil {
		t.Fatalf("second WriteTags failed: %v", err)
	}

	// 4. Считываем сырые фреймы
	newFrames, err := ReadRawFrames(filePath)
	if err != nil {
		t.Fatalf("ReadRawFrames failed: %v", err)
	}

	hasTCOP := false
	hasUSLT := false
	for _, f := range newFrames {
		if f.ID == "TCOP" {
			hasTCOP = true
			if string(f.Data[1:]) != "2026 Record Label Corp." {
				t.Errorf("TCOP content corrupted: %s", string(f.Data[1:]))
			}
		}
		if f.ID == "USLT" {
			hasUSLT = true
		}
	}

	if !hasTCOP {
		t.Errorf("expected TCOP frame to be preserved upon rewrite")
	}
	if hasUSLT {
		t.Errorf("expected USLT frame to be stripped according to rule #5")
	}

	// Проверяем, что обновленные теги считались корректно
	read, err := ReadTags(filePath, "test_preserve.mp3")
	if err != nil {
		t.Fatalf("ReadTags failed: %v", err)
	}
	if read.Title != "Updated Song Title" {
		t.Errorf("expected Title 'Updated Song Title', got '%s'", read.Title)
	}
}
