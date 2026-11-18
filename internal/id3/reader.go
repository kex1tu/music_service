package id3

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
)

// CoverInfo содержит бинарные данные обложки и её MIME-тип.
type CoverInfo struct {
	MimeType string
	Data     []byte
}

// ReadTags считывает метаданные из MP3-файла с защитой от поврежденных фреймов.
func ReadTags(filePath string, relPath string) (*TrackTags, error) {
	file, err := safefs.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть файл '%s': %w", filePath, err)
	}
	defer file.Close()
	return ReadTagsFromFile(file, relPath)
}

// ReadTagsFromFile reads tags from an open MP3 positioned at the start.
func ReadTagsFromFile(file *os.File, relPath string) (*TrackTags, error) {
	filePath := file.Name()
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}

	fileName := filepath.Base(filePath)
	cleanName := strings.TrimSuffix(fileName, filepath.Ext(fileName))

	tags := &TrackTags{
		Path:         relPath,
		Title:        CleanTitle(cleanName, cleanName),
		TrackNumber:  1,
		TrackTotal:   1,
		DiscNumber:   1,
		DiscTotal:    1,
		LyricsStatus: checkLyrics(filePath),
	}

	header := make([]byte, 10)
	n, err := io.ReadFull(file, header)
	if err != nil || n < 10 || string(header[0:3]) != "ID3" {
		// Нет ID3v2 заголовка — возвращаем базовые теги из имени файла
		estimateDurationAndBitrate(file, stat.Size(), 0, tags)
		return tags, nil
	}

	version := header[3]
	flags := header[5]
	tagSize := decodeSynchsafe(header[6:10])
	totalTagSize := 10 + tagSize
	if (flags & 0x10) != 0 {
		totalTagSize += 10 // ID3v2.4 footer
	}

	parseFrames(file, version, flags, tagSize, tags, nil)

	if tags.Duration <= 0 {
		estimateDurationAndBitrate(file, stat.Size(), totalTagSize, tags)
	}

	// Финальная очистка названия
	tags.Title = CleanTitle(tags.Title, cleanName)

	return tags, nil
}

// ReadCover извлекает фронтальную обложку (APIC type=3 или первую доступную).
func ReadCover(filePath string) (*CoverInfo, error) {
	file, err := safefs.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	header := make([]byte, 10)
	n, err := io.ReadFull(file, header)
	if err != nil || n < 10 || string(header[0:3]) != "ID3" {
		return nil, nil
	}

	version := header[3]
	flags := header[5]
	tagSize := decodeSynchsafe(header[6:10])

	var cover *CoverInfo
	tags := &TrackTags{}
	parseFrames(file, version, flags, tagSize, tags, func(ci *CoverInfo) {
		if cover == nil {
			cover = ci
		}
	})

	return cover, nil
}

// parseFrames последовательно читает и разбирает фреймы ID3v2 тега.
func parseFrames(file *os.File, version byte, flags byte, tagSize int, tags *TrackTags, onCover func(*CoverInfo)) {
	bytesRead := 0

	// Пропуск extended header
	if (flags & 0x40) != 0 {
		var extHdr [4]byte
		if _, err := io.ReadFull(file, extHdr[:]); err == nil {
			bytesRead += 4
			extSize := 0
			if version == 4 {
				extSize = decodeSynchsafe(extHdr[:])
				if extSize > 4 {
					file.Seek(int64(extSize-4), io.SeekCurrent)
					bytesRead += extSize - 4
				}
			} else {
				extSize = int(binary.BigEndian.Uint32(extHdr[:]))
				if extSize > 0 {
					file.Seek(int64(extSize), io.SeekCurrent)
					bytesRead += extSize
				}
			}
		}
	}

	for bytesRead+10 <= tagSize {
		var hdr [10]byte
		if _, err := io.ReadFull(file, hdr[:]); err != nil {
			break
		}
		bytesRead += 10

		if hdr[0] == 0 {
			// Достигнут ID3 padding
			break
		}

		frameID := string(hdr[0:4])
		if !isValidFrameID(frameID) {
			break
		}

		frameSize := 0
		if version == 4 {
			frameSize = decodeSynchsafe(hdr[4:8])
			// Эвристика: если старшие биты установлены, это обычный uint32
			if hdr[4]&0x80 != 0 || hdr[5]&0x80 != 0 || hdr[6]&0x80 != 0 || hdr[7]&0x80 != 0 {
				frameSize = int(binary.BigEndian.Uint32(hdr[4:8]))
			}
		} else {
			frameSize = int(binary.BigEndian.Uint32(hdr[4:8]))
		}

		if frameSize <= 0 || frameSize > 25*1024*1024 || bytesRead+frameSize > tagSize {
			break
		}

		// Обработка APIC (обложка)
		if frameID == "APIC" {
			tags.HasCover = true
			if onCover != nil {
				data := make([]byte, frameSize)
				if _, err := io.ReadFull(file, data); err == nil {
					ci := parseAPIC(data)
					if ci != nil {
						onCover(ci)
					}
				}
			} else {
				file.Seek(int64(frameSize), io.SeekCurrent)
			}
			bytesRead += frameSize
			continue
		}

		// Текстовые фреймы
		if isTextFrame(frameID) && frameSize <= 65536 {
			data := make([]byte, frameSize)
			if _, err := io.ReadFull(file, data); err == nil {
				applyFrame(frameID, data, tags)
			}
			bytesRead += frameSize
		} else {
			file.Seek(int64(frameSize), io.SeekCurrent)
			bytesRead += frameSize
		}
	}
}

// ReadRawFrames извлекает все существующие валидные фреймы из MP3-файла.
func ReadRawFrames(filePath string) ([]RawFrame, error) {
	file, err := safefs.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	header := make([]byte, 10)
	n, err := io.ReadFull(file, header)
	if err != nil || n < 10 || string(header[0:3]) != "ID3" {
		return nil, nil // No ID3v2 tag
	}

	version := header[3]
	flags := header[5]
	tagSize := decodeSynchsafe(header[6:10])

	bytesRead := 0

	// Пропуск extended header если есть
	if (flags & 0x40) != 0 {
		var extHdr [4]byte
		if _, err := io.ReadFull(file, extHdr[:]); err == nil {
			bytesRead += 4
			extSize := 0
			if version == 4 {
				extSize = decodeSynchsafe(extHdr[:])
				if extSize > 4 {
					file.Seek(int64(extSize-4), io.SeekCurrent)
					bytesRead += extSize - 4
				}
			} else {
				extSize = int(binary.BigEndian.Uint32(extHdr[:]))
				if extSize > 0 {
					file.Seek(int64(extSize), io.SeekCurrent)
					bytesRead += extSize
				}
			}
		}
	}

	var frames []RawFrame

	for bytesRead+10 <= tagSize {
		var hdr [10]byte
		if _, err := io.ReadFull(file, hdr[:]); err != nil {
			break
		}
		bytesRead += 10

		if hdr[0] == 0 {
			// ID3 padding reached
			break
		}

		frameID := string(hdr[0:4])
		if !isValidFrameID(frameID) {
			break
		}

		frameSize := 0
		if version == 4 {
			frameSize = decodeSynchsafe(hdr[4:8])
			if hdr[4]&0x80 != 0 || hdr[5]&0x80 != 0 || hdr[6]&0x80 != 0 || hdr[7]&0x80 != 0 {
				frameSize = int(binary.BigEndian.Uint32(hdr[4:8]))
			}
		} else {
			frameSize = int(binary.BigEndian.Uint32(hdr[4:8]))
		}

		if frameSize <= 0 || frameSize > 25*1024*1024 || bytesRead+frameSize > tagSize {
			break
		}

		data := make([]byte, frameSize)
		if _, err := io.ReadFull(file, data); err != nil {
			break
		}
		bytesRead += frameSize

		frames = append(frames, RawFrame{
			ID:    frameID,
			Flags: [2]byte{hdr[8], hdr[9]},
			Data:  data,
		})
	}

	return frames, nil
}
