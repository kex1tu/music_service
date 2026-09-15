package id3

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

var yearRegex = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)

// ParseTrackFast открывает MP3-файл, считывает только ID3-заголовок и метаданные аудиопотока,
// не вычитывая сам аудиопоток целиком.
func ParseTrackFast(fullPath string, rootDir string) (TrackMeta, error) {
	file, err := os.Open(fullPath)
	if err != nil {
		return TrackMeta{}, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return TrackMeta{}, err
	}

	relPath, err := filepath.Rel(rootDir, fullPath)
	if err != nil {
		relPath = fullPath
	}
	relPath = filepath.ToSlash(relPath)

	size := stat.Size()
	mtime := float64(stat.ModTime().UnixNano()) / 1e9

	// Проверяем наличие внешних текстов (.lrc / .txt)
	lyricsStatus := checkLyricsStatus(fullPath)

	meta := TrackMeta{
		RelPath:      relPath,
		Title:        strings.TrimSuffix(filepath.Base(fullPath), filepath.Ext(fullPath)),
		TrackNumber:  1,
		DiscNumber:   1,
		LyricsStatus: lyricsStatus,
		Size:         size,
		Mtime:        mtime,
	}

	header := make([]byte, 10)
	n, err := io.ReadFull(file, header)
	if err != nil || n < 10 {
		return meta, nil
	}

	totalTagSize := 0
	if string(header[0:3]) == "ID3" {
		version := header[3]
		flags := header[5]
		tagSize := decodeSynchsafe(header[6:10])
		totalTagSize = 10 + tagSize
		if (flags & 0x10) != 0 { // ID3v2.4 footer
			totalTagSize += 10
		}

		parseID3FramesStream(file, version, flags, tagSize, &meta)
	}

	// Определение длительности
	if meta.Duration <= 0 {
		meta.Duration = estimateDuration(file, size, totalTagSize)
	}

	return meta, nil
}

func checkLyricsStatus(fullPath string) string {
	ext := filepath.Ext(fullPath)
	base := strings.TrimSuffix(fullPath, ext)

	if st, err := os.Stat(base + ".lrc"); err == nil && !st.IsDir() {
		return "lrc"
	}
	if st, err := os.Stat(base + ".txt"); err == nil && !st.IsDir() {
		return "txt"
	}
	return "none"
}

func decodeSynchsafe(b []byte) int {
	return int(b[0]&0x7F)<<21 | int(b[1]&0x7F)<<14 | int(b[2]&0x7F)<<7 | int(b[3]&0x7F)
}

func parseID3FramesStream(file *os.File, version byte, flags byte, tagSize int, meta *TrackMeta) {
	bytesRead := 0

	// Пропуск extended header при наличии
	if (flags & 0x40) != 0 {
		var extHdr [4]byte
		if _, err := io.ReadFull(file, extHdr[:]); err != nil {
			return
		}
		bytesRead += 4
		var extSize int
		if version == 3 {
			extSize = int(binary.BigEndian.Uint32(extHdr[:]))
			if extSize > 0 {
				if _, err := file.Seek(int64(extSize), io.SeekCurrent); err != nil {
					return
				}
				bytesRead += extSize
			}
		} else if version == 4 {
			extSize = decodeSynchsafe(extHdr[:])
			if extSize > 4 {
				if _, err := file.Seek(int64(extSize-4), io.SeekCurrent); err != nil {
					return
				}
				bytesRead += extSize - 4
			}
		}
	}

	if version == 2 {
		parseID3v22FramesStream(file, tagSize, bytesRead, meta)
		return
	}

	// ID3v2.3 и ID3v2.4
	for bytesRead+10 <= tagSize {
		var hdr [10]byte
		if _, err := io.ReadFull(file, hdr[:]); err != nil {
			break
		}
		bytesRead += 10

		if hdr[0] == 0 {
			// Наткнулись на ID3 padding (нули)
			break
		}

		frameID := string(hdr[0:4])
		if !isValidFrameID(frameID) {
			break
		}

		var frameSize int
		if version == 4 {
			frameSize = decodeSynchsafe(hdr[4:8])
		} else {
			frameSize = int(binary.BigEndian.Uint32(hdr[4:8]))
		}

		if frameSize <= 0 || bytesRead+frameSize > tagSize {
			break
		}

		if frameID == "APIC" {
			meta.HasCover = true
			// Не читаем мегабайты обложки из диска — мгновенный seek вперед!
			if _, err := file.Seek(int64(frameSize), io.SeekCurrent); err != nil {
				break
			}
			bytesRead += frameSize
			continue
		}

		if isTextFrame(frameID) && frameSize <= 65536 {
			frameData := make([]byte, frameSize)
			if _, err := io.ReadFull(file, frameData); err != nil {
				break
			}
			bytesRead += frameSize
			applyFrame(frameID, frameData, meta)
		} else {
			// Пропускаем неиспользуемые или слишком большие фреймы
			if _, err := file.Seek(int64(frameSize), io.SeekCurrent); err != nil {
				break
			}
			bytesRead += frameSize
		}
	}
}

func isTextFrame(id string) bool {
	switch id {
	case "TIT2", "TPE1", "TPE2", "TALB", "TCON", "TRCK", "TPOS", "TDRC", "TYER", "TLEN":
		return true
	default:
		return false
	}
}

func isValidFrameID(id string) bool {
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func parseID3v22FramesStream(file *os.File, tagSize int, bytesRead int, meta *TrackMeta) {
	for bytesRead+6 <= tagSize {
		var hdr [6]byte
		if _, err := io.ReadFull(file, hdr[:]); err != nil {
			break
		}
		bytesRead += 6

		if hdr[0] == 0 {
			break
		}
		frameID := string(hdr[0:3])
		frameSize := int(hdr[3])<<16 | int(hdr[4])<<8 | int(hdr[5])
		if frameSize <= 0 || bytesRead+frameSize > tagSize {
			break
		}

		if frameID == "PIC" {
			meta.HasCover = true
			if _, err := file.Seek(int64(frameSize), io.SeekCurrent); err != nil {
				break
			}
			bytesRead += frameSize
			continue
		}

		if isID3v22TextFrame(frameID) && frameSize <= 65536 {
			frameData := make([]byte, frameSize)
			if _, err := io.ReadFull(file, frameData); err != nil {
				break
			}
			bytesRead += frameSize

			switch frameID {
			case "TT2":
				meta.Title = decodeTextFrame(frameData)
			case "TP1":
				meta.Artist = decodeTextFrame(frameData)
			case "TP2":
				meta.AlbumArtist = decodeTextFrame(frameData)
			case "TAL":
				meta.Album = decodeTextFrame(frameData)
			case "TCO":
				meta.Genre = decodeTextFrame(frameData)
			case "TRK":
				parseTrackNumber(decodeTextFrame(frameData), meta)
			case "TYE":
				parseYear(decodeTextFrame(frameData), meta)
			}
		} else {
			if _, err := file.Seek(int64(frameSize), io.SeekCurrent); err != nil {
				break
			}
			bytesRead += frameSize
		}
	}
}

func isID3v22TextFrame(id string) bool {
	switch id {
	case "TT2", "TP1", "TP2", "TAL", "TCO", "TRK", "TYE":
		return true
	default:
		return false
	}
}

func applyFrame(frameID string, data []byte, meta *TrackMeta) {
	if len(data) == 0 {
		return
	}

	switch frameID {
	case "TIT2":
		meta.Title = decodeTextFrame(data)
	case "TPE1":
		meta.Artist = decodeTextFrame(data)
	case "TPE2":
		meta.AlbumArtist = decodeTextFrame(data)
	case "TALB":
		meta.Album = decodeTextFrame(data)
	case "TCON":
		meta.Genre = decodeTextFrame(data)
	case "TRCK":
		parseTrackNumber(decodeTextFrame(data), meta)
	case "TPOS":
		parseDiscNumber(decodeTextFrame(data), meta)
	case "TDRC", "TYER":
		parseYear(decodeTextFrame(data), meta)
	case "TLEN":
		text := strings.TrimSpace(decodeTextFrame(data))
		if ms, err := strconv.ParseFloat(text, 64); err == nil && ms > 0 {
			meta.Duration = ms / 1000.0
		}
	case "APIC":
		meta.HasCover = true
	}
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

func decodeTextFrame(data []byte) string {
	if len(data) == 0 {
		return ""
	}

	encoding := data[0]
	payload := data[1:]

	var parts []string

	switch encoding {
	case 0: // ISO-8859-1 / ASCII / Windows-1251
		raw := decodeISO88591(payload)
		for _, s := range strings.Split(raw, "\x00") {
			trimmed := strings.ToValidUTF8(strings.TrimSpace(s), "")
			if trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
	case 1: // UTF-16 with BOM
		for _, p := range decodeUTF16(payload, true) {
			trimmed := strings.ToValidUTF8(strings.TrimSpace(p), "")
			if trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
	case 2: // UTF-16BE without BOM
		for _, p := range decodeUTF16(payload, false) {
			trimmed := strings.ToValidUTF8(strings.TrimSpace(p), "")
			if trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
	case 3: // UTF-8
		raw := strings.ToValidUTF8(string(payload), "")
		for _, s := range strings.Split(raw, "\x00") {
			trimmed := strings.TrimSpace(s)
			if trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
	default:
		raw := decodeISO88591(payload)
		for _, s := range strings.Split(raw, "\x00") {
			trimmed := strings.ToValidUTF8(strings.TrimSpace(s), "")
			if trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
	}

	return strings.Join(parts, "\\\\")
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
		trimmed := strings.TrimSpace(s)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts
}

func parseTrackNumber(val string, meta *TrackMeta) {
	// Поддержка "7/14" или "7"
	parts := strings.Split(val, "/")
	if len(parts) > 0 {
		if num, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil && num > 0 {
			meta.TrackNumber = num
		}
	}
	if len(parts) > 1 {
		if total, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && total > 0 {
			meta.TrackTotal = total
		}
	}
}

func parseDiscNumber(val string, meta *TrackMeta) {
	// Поддержка "1/2" или "1"
	parts := strings.Split(val, "/")
	if len(parts) > 0 {
		if num, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil && num > 0 {
			meta.DiscNumber = num
		}
	}
	if len(parts) > 1 {
		if total, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && total > 0 {
			meta.DiscTotal = total
		}
	}
}

func parseYear(val string, meta *TrackMeta) {
	matches := yearRegex.FindStringSubmatch(val)
	if len(matches) > 1 {
		if y, err := strconv.Atoi(matches[1]); err == nil && y >= 1000 && y <= 2999 {
			meta.Year = y
		}
	}
}

// estimateDuration вычисляет приблизительную или точную длительность MP3 аудиопотока.
func estimateDuration(file *os.File, fileSize int64, id3TagSize int) float64 {
	audioDataSize := fileSize - int64(id3TagSize)
	if audioDataSize <= 0 {
		return 0
	}

	_, err := file.Seek(int64(id3TagSize), io.SeekStart)
	if err != nil {
		return 0
	}

	buf := make([]byte, 4096)
	n, err := io.ReadFull(file, buf)
	if err != nil && n < 4 {
		return 0
	}
	buf = buf[:n]

	// Поиск sync word MPEG (11 единиц подряд: 0xFF и (0xFn или 0xEn))
	for i := 0; i+4 <= len(buf); i++ {
		if buf[i] == 0xFF && (buf[i+1]&0xE0) == 0xE0 {
			versionBits := (buf[i+1] >> 3) & 0x03
			layerBits := (buf[i+1] >> 1) & 0x03
			bitrateIdx := (buf[i+2] >> 4) & 0x0F
			sampleRateIdx := (buf[i+2] >> 2) & 0x03
			channelMode := (buf[i+3] >> 6) & 0x03

			if versionBits == 1 || layerBits == 0 || bitrateIdx == 0 || bitrateIdx == 15 || sampleRateIdx == 3 {
				continue
			}

			sampleRate := getSampleRate(versionBits, sampleRateIdx)
			if sampleRate <= 0 {
				continue
			}

			// Проверка наличия заголовка Xing / Info
			xingOffset := i + 4
			if versionBits == 3 { // MPEG 1
				if channelMode == 3 { // Mono
					xingOffset += 17
				} else {
					xingOffset += 32
				}
			} else { // MPEG 2 / 2.5
				if channelMode == 3 {
					xingOffset += 9
				} else {
					xingOffset += 17
				}
			}

			if xingOffset+8 <= len(buf) {
				tag := string(buf[xingOffset : xingOffset+4])
				if tag == "Xing" || tag == "Info" {
					flags := binary.BigEndian.Uint32(buf[xingOffset+4 : xingOffset+8])
					if (flags&0x01) != 0 && xingOffset+12 <= len(buf) {
						frameCount := binary.BigEndian.Uint32(buf[xingOffset+8 : xingOffset+12])
						samplesPerFrame := 1152
						if layerBits == 3 { // Layer I
							samplesPerFrame = 384
						} else if versionBits != 3 { // MPEG 2 / 2.5 Layer III
							samplesPerFrame = 576
						}
						return float64(frameCount) * float64(samplesPerFrame) / float64(sampleRate)
					}
				}
			}

			// Если Xing не найден, вычисляем по CBR битрейту
			bitrate := getBitrate(versionBits, layerBits, bitrateIdx)
			if bitrate > 0 {
				bitrateBps := float64(bitrate * 1000)
				return float64(audioDataSize*8) / bitrateBps
			}
		}
	}

	// По умолчанию предполагаем 320 kbps для проекта
	return float64(audioDataSize*8) / (320.0 * 1000.0)
}

func getSampleRate(versionBits, sampleRateIdx byte) int {
	// MPEG 1
	if versionBits == 3 {
		rates := [3]int{44100, 48000, 32000}
		return rates[sampleRateIdx]
	}
	// MPEG 2
	if versionBits == 2 {
		rates := [3]int{22050, 24000, 16000}
		return rates[sampleRateIdx]
	}
	// MPEG 2.5
	if versionBits == 0 {
		rates := [3]int{11025, 12000, 8000}
		return rates[sampleRateIdx]
	}
	return 44100
}

func getBitrate(versionBits, layerBits, bitrateIdx byte) int {
	if versionBits == 3 && layerBits == 1 { // MPEG 1 Layer III
		rates := [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
		return rates[bitrateIdx]
	}
	if versionBits != 3 && layerBits == 1 { // MPEG 2/2.5 Layer III
		rates := [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
		return rates[bitrateIdx]
	}
	return 320
}
