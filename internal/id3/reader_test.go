package id3

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

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

func TestWriteAndReadTags_MultiGenre_NullSeparated(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "id3_genre_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	filePath := filepath.Join(tempDir, "genre_test.mp3")
	fakeAudio := make([]byte, 5000)
	if err := os.WriteFile(filePath, fakeAudio, 0644); err != nil {
		t.Fatal(err)
	}

	tags := TrackTags{
		Title:  "Test Multi-Genre",
		Artist: "1.Kla$\\\\1.Царь",
		Genre:  "Alternative\\\\Rock\\\\Indie Rock\\\\Pop-Rock",
	}

	if err := WriteTags(filePath, tags, nil); err != nil {
		t.Fatalf("WriteTags failed: %v", err)
	}

	// 1. Проверяем сырые фреймы: TCON должен содержать null-байты \x00
	rawFrames, err := ReadRawFrames(filePath)
	if err != nil {
		t.Fatalf("ReadRawFrames failed: %v", err)
	}

	var tconData []byte
	var tpe1Data []byte
	for _, f := range rawFrames {
		if f.ID == "TCON" {
			tconData = f.Data
		}
		if f.ID == "TPE1" {
			tpe1Data = f.Data
		}
	}

	if len(tconData) == 0 {
		t.Fatalf("TCON frame not found")
	}
	if tconData[0] != 3 {
		t.Errorf("expected TCON encoding 3 (UTF-8), got %d", tconData[0])
	}

	// Ожидаемый payload: UTF-8 encoding (3) + "Alternative\x00Rock\x00Indie Rock\x00Pop-Rock"
	expectedGenrePayload := append([]byte{3}, []byte("Alternative\x00Rock\x00Indie Rock\x00Pop-Rock")...)
	if !bytes.Equal(tconData, expectedGenrePayload) {
		t.Errorf("TCON payload mismatch:\nwant: %q (%v)\ngot:  %q (%v)",
			string(expectedGenrePayload), expectedGenrePayload, string(tconData), tconData)
	}

	// Проверяем TPE1 (Artists): "1.Kla$\x001.Царь"
	expectedArtistPayload := append([]byte{3}, []byte("1.Kla$\x001.Царь")...)
	if !bytes.Equal(tpe1Data, expectedArtistPayload) {
		t.Errorf("TPE1 payload mismatch:\nwant: %q (%v)\ngot:  %q (%v)",
			string(expectedArtistPayload), expectedArtistPayload, string(tpe1Data), tpe1Data)
	}

	// 2. Проверяем обратное считывание через ReadTags
	readBack, err := ReadTags(filePath, "genre_test.mp3")
	if err != nil {
		t.Fatalf("ReadTags failed: %v", err)
	}

	if readBack.Genre != "Alternative\\\\Rock\\\\Indie Rock\\\\Pop-Rock" {
		t.Errorf("expected Genre %q, got %q", "Alternative\\\\Rock\\\\Indie Rock\\\\Pop-Rock", readBack.Genre)
	}
	if readBack.Artist != "1.Kla$\\\\1.Царь" {
		t.Errorf("expected Artist %q, got %q", "1.Kla$\\\\1.Царь", readBack.Artist)
	}
}

func TestWriteTagsPreservesAudioAtTagBoundary(t *testing.T) {
	audio := make([]byte, 417)
	copy(audio, []byte{0xff, 0xfb, 0x90, 0x64})
	copy(audio[4:], "original audio")
	for _, tc := range []struct {
		name    string
		tagSize int
		wantErr bool
	}{
		{name: "complete tag", tagSize: 16},
		{name: "truncated tag", tagSize: 1024, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "track.mp3")
			header := []byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 0}
			encodeSynchsafe(tc.tagSize, header[6:10])
			before := append(append(header, make([]byte, 16)...), audio...)
			if err := os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}

			writeErr := WriteTags(path, TrackTags{Title: "Updated"}, nil)
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantErr {
				if writeErr == nil || !bytes.Equal(before, after) {
					t.Fatalf("truncated tag: error=%v, original unchanged=%t", writeErr, bytes.Equal(before, after))
				}
				return
			}
			if writeErr != nil {
				t.Fatal(writeErr)
			}
			offset := 10 + decodeSynchsafe(after[6:10])
			if offset > len(after) || !bytes.Equal(after[offset:], audio) {
				t.Fatal("retagging changed the audio payload")
			}
			tags, err := ReadTags(path, "track.mp3")
			if err != nil || tags.Title != "Updated" {
				t.Fatalf("updated tags = %+v, error = %v", tags, err)
			}
		})
	}
}
