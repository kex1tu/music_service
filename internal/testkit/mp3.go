package testkit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
)

// MPEG1Layer3FrameSize размер одного кадра MP3 для 320 kbps / 44.1 kHz.
const MPEG1Layer3FrameSize = 1044

// CreateDummyMP3 создает валидный MP3-файл с кадрами MPEG-1 Layer 3 (sync word 0xFF 0xFB)
// и каноническими ID3v2.4 UTF-8 тегами внутри указанной песочницы.
// Права на созданный файл гарантированно выставляются в 0644.
func CreateDummyMP3(musicSandbox *sandbox.PathSandbox, relPath, title, artist, album string) (string, error) {
	tags := id3.TrackTags{
		Title:       title,
		Artist:      artist,
		Album:       album,
		AlbumArtist: artist,
		TrackNumber: 1,
		TrackTotal:  1,
		DiscNumber:  1,
		DiscTotal:   1,
		Year:        2026,
	}
	return CreateDummyMP3WithTags(musicSandbox, relPath, tags, nil)
}

// CreateDummyMP3WithTags создает MP3-файл с расширенными тегами и опциональной обложкой.
func CreateDummyMP3WithTags(musicSandbox *sandbox.PathSandbox, relPath string, tags id3.TrackTags, cover *id3.CoverInfo) (string, error) {
	if musicSandbox == nil {
		return "", errors.New("testkit: musicSandbox cannot be nil")
	}
	// safefs/mediaexec locate descriptor roots through weak references. Keep
	// the fixture's owning sandbox alive through the indirect ID3 operations.
	defer runtime.KeepAlive(musicSandbox)

	// 1. Разрешение пути через песочницу с запретом доступа к корню
	absPath, err := musicSandbox.Resolve(relPath, false)
	if err != nil {
		return "", fmt.Errorf("testkit: invalid relPath %q: %w", relPath, err)
	}

	// 2. Создание родительских директорий
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("testkit: failed to create directory structure for %s: %w", absPath, err)
	}

	// 3. Генерация легитимных аудиокадров MPEG-1 Layer 3 (320 kbps, 44.1 kHz, Stereo)
	audioPayload := generateMinimalMPEG1Layer3Payload(4)
	if err := os.WriteFile(absPath, audioPayload, 0644); err != nil {
		return "", fmt.Errorf("testkit: failed to write dummy audio stream: %w", err)
	}

	// 4. Запись канонических ID3v2.4 тегов через сертифицированный движок
	if err := id3.WriteTags(absPath, tags, cover); err != nil {
		return "", fmt.Errorf("testkit: failed to write ID3v2.4 tags: %w", err)
	}

	// 5. Обеспечение прав 0644 (Navidrome / DLNA совместимость)
	if err := os.Chmod(absPath, 0644); err != nil {
		return "", fmt.Errorf("testkit: failed to enforce 0644 permissions: %w", err)
	}

	return absPath, nil
}

// generateMinimalMPEG1Layer3Payload формирует n последовательных кадров MPEG-1 Layer 3.
func generateMinimalMPEG1Layer3Payload(numFrames int) []byte {
	if numFrames <= 0 {
		numFrames = 1
	}

	payload := make([]byte, numFrames*MPEG1Layer3FrameSize)
	for i := 0; i < numFrames; i++ {
		offset := i * MPEG1Layer3FrameSize
		// Байт 0: 0xFF (Sync word bits 11-4)
		payload[offset+0] = 0xFF
		// Байт 1: 0xFB (Sync word bits 3-1: 111, MPEG-1: 11, Layer 3: 01, No CRC: 1)
		payload[offset+1] = 0xFB
		// Байт 2: 0xE0 (320 kbps: 1110, 44.1 kHz: 00, Padding: 0, Private: 0)
		payload[offset+2] = 0xE0
		// Байт 3: 0x04 (Stereo: 00, Mode Ext: 00, Copyright: 0, Original: 1, Emphasis: 00)
		payload[offset+3] = 0x04
		// Байты 4..1043 заполнены нулями (тишина)
	}
	return payload
}
