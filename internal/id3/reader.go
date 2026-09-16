package id3

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// CoverInfo содержит бинарные данные обложки и её MIME-тип.
type CoverInfo struct {
	MimeType string
	Data     []byte
}

// ReadTags считывает метаданные из MP3-файла с защитой от поврежденных фреймов.
func ReadTags(filePath string, relPath string) (*TrackTags, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть файл '%s': %w", filePath, err)
	}
	defer file.Close()

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
	file, err := os.Open(filePath)
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

func decodeSynchsafe(b []byte) int {
	return int(b[0]&0x7F)<<21 | int(b[1]&0x7F)<<14 | int(b[2]&0x7F)<<7 | int(b[3]&0x7F)
}

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

		if frameSize <= 0 || bytesRead+frameSize > tagSize {
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

func isValidFrameID(id string) bool {
	if len(id) != 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		c := id[i]
		if !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func isTextFrame(id string) bool {
	switch id {
	case "TIT2", "TPE1", "TPE2", "TALB", "TCON", "TRCK", "TPOS", "TDRC", "TYER", "TLEN":
		return true
	default:
		return false
	}
}

func applyFrame(id string, data []byte, tags *TrackTags) {
	val := decodeText(data)
	if val == "" {
		return
	}

	switch id {
	case "TIT2":
		tags.Title = val
	case "TPE1":
		tags.Artist = val
	case "TPE2":
		tags.AlbumArtist = val
	case "TALB":
		tags.Album = val
	case "TCON":
		tags.Genre = val
	case "TRCK":
		parseTrackNum(val, &tags.TrackNumber, &tags.TrackTotal)
	case "TPOS":
		parseTrackNum(val, &tags.DiscNumber, &tags.DiscTotal)
	case "TDRC", "TYER":
		if len(val) >= 4 {
			if yr, err := strconv.Atoi(val[:4]); err == nil && yr > 1900 && yr < 2100 {
				tags.Year = yr
			}
		}
	case "TLEN":
		if ms, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil && ms > 0 {
			tags.Duration = ms / 1000.0
		}
	}
}

func decodeText(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	encoding := data[0]
	payload := data[1:]

	var parts []string
	switch encoding {
	case 1: // UTF-16 with BOM
		parts = decodeUTF16(payload, true)
	case 2: // UTF-16BE without BOM
		parts = decodeUTF16(payload, false)
	case 3: // UTF-8
		raw := strings.ToValidUTF8(string(payload), "")
		for _, s := range strings.Split(raw, "\x00") {
			t := strings.TrimSpace(s)
			if t != "" {
				parts = append(parts, t)
			}
		}
	default: // ISO-8859-1 / CP1251
		raw := decodeISO88591(payload)
		for _, s := range strings.Split(raw, "\x00") {
			t := strings.TrimSpace(s)
			if t != "" {
				parts = append(parts, t)
			}
		}
	}

	return strings.Join(parts, "\\\\")
}

func decodeISO88591(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	runes := make([]rune, len(data))
	for i, b := range data {
		runes[i] = rune(b)
	}
	return string(runes)
}

func decodeUTF16(data []byte, withBOM bool) []string {
	if len(data) < 2 {
		return nil
	}
	isLE := false
	offset := 0
	if withBOM {
		if data[0] == 0xFF && data[1] == 0xFE {
			isLE = true
			offset = 2
		} else if data[0] == 0xFE && data[1] == 0xFF {
			isLE = false
			offset = 2
		}
	}

	var u16s []uint16
	for i := offset; i+1 < len(data); i += 2 {
		var val uint16
		if isLE {
			val = uint16(data[i]) | (uint16(data[i+1]) << 8)
		} else {
			val = (uint16(data[i]) << 8) | uint16(data[i+1])
		}
		u16s = append(u16s, val)
	}

	runes := utf16.Decode(u16s)
	raw := string(runes)

	var parts []string
	for _, s := range strings.Split(raw, "\x00") {
		t := strings.TrimSpace(s)
		if t != "" {
			parts = append(parts, t)
		}
	}
	return parts
}

func parseTrackNum(val string, num *int, total *int) {
	parts := strings.Split(val, "/")
	if len(parts) > 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil && n > 0 {
			*num = n
		}
	}
	if len(parts) > 1 {
		if t, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && t > 0 {
			*total = t
		}
	}
}

func parseAPIC(data []byte) *CoverInfo {
	if len(data) < 5 {
		return nil
	}
	// encoding: data[0]
	// MIME type: string terminated by \x00
	idx := 1
	for idx < len(data) && data[idx] != 0 {
		idx++
	}
	if idx >= len(data) {
		return nil
	}
	mime := string(data[1:idx])
	idx++ // пропуск \x00

	if idx >= len(data) {
		return nil
	}
	// picture type: data[idx] (3 = Cover Front)
	idx++

	// description: terminated by \x00 (or \x00\x00 in utf16)
	for idx < len(data) && data[idx] != 0 {
		idx++
	}
	for idx < len(data) && data[idx] == 0 {
		idx++
	}

	if idx >= len(data) {
		return nil
	}

	imgData := data[idx:]
	if len(imgData) == 0 {
		return nil
	}

	if mime == "" {
		if len(imgData) >= 2 && imgData[0] == 0xFF && imgData[1] == 0xD8 {
			mime = "image/jpeg"
		} else if len(imgData) >= 8 && string(imgData[1:4]) == "PNG" {
			mime = "image/png"
		} else {
			mime = "image/jpeg"
		}
	}

	return &CoverInfo{
		MimeType: mime,
		Data:     imgData,
	}
}

func checkLyrics(filePath string) string {
	ext := filepath.Ext(filePath)
	base := strings.TrimSuffix(filePath, ext)
	if st, err := os.Stat(base + ".lrc"); err == nil && !st.IsDir() {
		return "lrc"
	}
	if st, err := os.Stat(base + ".txt"); err == nil && !st.IsDir() {
		return "txt"
	}
	return "none"
}

func estimateDurationAndBitrate(file *os.File, fileSize int64, tagSize int, tags *TrackTags) {
	audioSize := fileSize - int64(tagSize)
	if audioSize <= 0 {
		return
	}
	// По умолчанию стандартный 320 kbps MP3
	tags.Bitrate = 320
	// 320 kbps = 40 000 байт/сек
	tags.Duration = float64(audioSize) / 40000.0
}

// ReadRawFrames extracts all existing valid frames from an MP3 file.
func ReadRawFrames(filePath string) ([]RawFrame, error) {
	file, err := os.Open(filePath)
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

	// Skip extended header if present
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

		if frameSize <= 0 || bytesRead+frameSize > tagSize {
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
