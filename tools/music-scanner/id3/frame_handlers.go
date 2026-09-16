package id3

import (
	"encoding/binary"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var yearRegex = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)

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
