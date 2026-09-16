package id3

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
)

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
