package id3

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
)

// WriteTags записывает канонические теги стандарта ID3v2.4 в MP3-файл.
func WriteTags(filePath string, tags TrackTags, cover *CoverInfo) error {
	// Считываем существующие фреймы для сохранения нетипизированных тегов (COMM, TCOP, ISRC и др.)
	existingFrames, _ := ReadRawFrames(filePath)
	return writeTags(filePath, tags, cover, existingFrames, tags.SourceURL != "")
}

// ReplaceAudioTags keeps the original file's metadata frames on newly downloaded audio.
func ReplaceAudioTags(downloaded, original string, tags TrackTags) error {
	existingFrames, err := ReadRawFrames(original)
	if err != nil {
		return err
	}
	return writeTags(downloaded, tags, nil, existingFrames, true)
}

func writeTags(filePath string, tags TrackTags, cover *CoverInfo, existingFrames []RawFrame, replaceSource bool) error {
	// 1. Считываем аудиопоток (пропуская существующий ID3v2 заголовок)
	file, err := safefs.Open(filePath)
	if err != nil {
		return fmt.Errorf("не удалось открыть исходный файл: %w", err)
	}
	defer file.Close()

	audioOffset := int64(0)
	header := make([]byte, 10)
	if n, err := io.ReadFull(file, header); err == nil && n == 10 && string(header[0:3]) == "ID3" {
		tagSize := decodeSynchsafe(header[6:10])
		audioOffset = int64(10 + tagSize)
		if (header[5] & 0x10) != 0 {
			audioOffset += 10 // ID3v2.4 footer
		}
		info, err := file.Stat()
		if err != nil {
			return fmt.Errorf("не удалось определить размер исходного файла: %w", err)
		}
		if audioOffset > info.Size() {
			return fmt.Errorf("тег ID3 выходит за пределы исходного файла: %w", io.ErrUnexpectedEOF)
		}
	}

	if _, err := file.Seek(audioOffset, io.SeekStart); err != nil {
		return fmt.Errorf("не удалось спозиционировать аудиопоток: %w", err)
	}

	// 2. Генерируем ID3v2.4 фреймы
	var framesBuf bytes.Buffer

	// TIT2: Название
	if strings.TrimSpace(tags.Title) != "" {
		writeTextFrame(&framesBuf, "TIT2", tags.Title)
	}

	// TPE1: Исполнитель (поддержка null-separated \x00 для нескольких исполнителей)
	if strings.TrimSpace(tags.Artist) != "" {
		writeMultiTextFrame(&framesBuf, "TPE1", tags.Artist)
	}

	// TPE2: Исполнитель альбома (поддержка null-separated \x00 для нескольких исполнителей стандарта ID3v2.4)
	if strings.TrimSpace(tags.AlbumArtist) != "" {
		writeMultiTextFrame(&framesBuf, "TPE2", tags.AlbumArtist)
	}

	// TALB: Альбом
	if strings.TrimSpace(tags.Album) != "" {
		writeTextFrame(&framesBuf, "TALB", tags.Album)
	}

	// TCON: Жанр (поддержка null-separated \x00)
	if strings.TrimSpace(tags.Genre) != "" {
		writeGenreFrame(&framesBuf, tags.Genre)
	}

	// TRCK: Номер дорожки
	if tags.TrackNumber > 0 {
		trck := fmt.Sprintf("%d", tags.TrackNumber)
		if tags.TrackTotal > 0 {
			trck = fmt.Sprintf("%d/%d", tags.TrackNumber, tags.TrackTotal)
		}
		writeTextFrame(&framesBuf, "TRCK", trck)
	}

	// TPOS: Номер диска
	if tags.DiscNumber > 0 {
		tpos := fmt.Sprintf("%d", tags.DiscNumber)
		if tags.DiscTotal > 0 {
			tpos = fmt.Sprintf("%d/%d", tags.DiscNumber, tags.DiscTotal)
		}
		writeTextFrame(&framesBuf, "TPOS", tpos)
	}

	// TDRC: Год выпуска
	if tags.Year > 0 {
		writeTextFrame(&framesBuf, "TDRC", fmt.Sprintf("%d", tags.Year))
	}

	// APIC: Фронтальная обложка (оптимизируется в памяти)
	if cover != nil && len(cover.Data) > 0 {
		optData, optMime := OptimizeCoverForID3(cover.Data, 1000, 85)
		if len(optData) > 0 {
			optCover := &CoverInfo{
				MimeType: optMime,
				Data:     optData,
			}
			writeAPICFrame(&framesBuf, optCover)
		}
	}

	// Сохраняем сторонние валидные фреймы (COMM, TCOP, ISRC, TBPM и др.)
	managed := map[string]bool{
		"TIT2": true, "TPE1": true, "TPE2": true, "TALB": true,
		"TCON": true, "TRCK": true, "TPOS": true, "TDRC": true, "TYER": true,
		// Инвариант: тексты песен не вшиваются в MP3
		"USLT": true, "SYLT": true,
	}
	if cover != nil && len(cover.Data) > 0 {
		managed["APIC"] = true
	}
	if tags.ISRC != "" {
		managed["TSRC"] = true
		writeTextFrame(&framesBuf, "TSRC", tags.ISRC)
	}
	if tags.SourceURL != "" {
		writeTextFrame(&framesBuf, "TXXX", sourceURLDescription+"\x00"+tags.SourceURL)
	}

	for _, ef := range existingFrames {
		if replaceSource && ef.ID == "TXXX" && strings.EqualFold(ExtractTXXXDesc(ef.Data), sourceURLDescription) {
			continue
		}
		if !managed[ef.ID] && isValidFrameID(ef.ID) && len(ef.Data) > 0 && !isArtistOrConflictingTXXX(ef) {
			writeRawFrame(&framesBuf, ef)
		}
	}

	// 3. Формируем ID3v2.4 заголовок
	framesBytes := framesBuf.Bytes()
	totalTagLen := len(framesBytes) + 1024 // 1 КБ padding для быстрых мелких правок
	paddingLen := 1024

	var id3Hdr [10]byte
	copy(id3Hdr[0:3], "ID3")
	id3Hdr[3] = 4 // ID3v2.4
	id3Hdr[4] = 0 // revision 0
	id3Hdr[5] = 0 // flags

	// Кодируем synchsafe размер тега
	encodeSynchsafe(totalTagLen, id3Hdr[6:10])

	// 4. Атомарная буферизованная запись во временный файл (буфер 128 КБ)
	dir := filepath.Dir(filePath)
	tmpFile, err := safefs.CreateTemp(dir, "tag_write_*.tmp")
	if err != nil {
		return fmt.Errorf("не удалось создать временный файл: %w", err)
	}
	tmpName := tmpFile.Name()
	defer safefs.Remove(tmpName)

	bufWriter := bufio.NewWriterSize(tmpFile, 128*1024)

	// Заголовок
	if _, err := bufWriter.Write(id3Hdr[:]); err != nil {
		tmpFile.Close()
		return err
	}
	// Фреймы
	if _, err := bufWriter.Write(framesBytes); err != nil {
		tmpFile.Close()
		return err
	}
	// Padding (нули)
	if paddingLen > 0 {
		padding := make([]byte, paddingLen)
		if _, err := bufWriter.Write(padding); err != nil {
			tmpFile.Close()
			return err
		}
	}

	// Копируем аудиоданные через 128 КБ буфер
	bufReader := bufio.NewReaderSize(file, 128*1024)
	if _, err := io.Copy(bufWriter, bufReader); err != nil {
		tmpFile.Close()
		return fmt.Errorf("ошибка копирования аудиопотока: %w", err)
	}

	if err := bufWriter.Flush(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("ошибка сброса буфера записи: %w", err)
	}

	tmpFile.Close()
	file.Close()

	// Гарантируем права 0644 для доступности сервисам (Navidrome, DLNA)
	_ = safefs.Chmod(tmpName, 0644)

	// Атомарная замена файла
	if err := safefs.Rename(tmpName, filePath); err != nil {
		return fmt.Errorf("не удалось обновить файл тегов: %w", err)
	}
	_ = safefs.Chmod(filePath, 0644)

	return nil
}

func encodeSynchsafe(val int, b []byte) {
	b[0] = byte((val >> 21) & 0x7F)
	b[1] = byte((val >> 14) & 0x7F)
	b[2] = byte((val >> 7) & 0x7F)
	b[3] = byte(val & 0x7F)
}

func writeTextFrame(w io.Writer, frameID string, text string) {
	// ID3v2.4 encoding: 3 = UTF-8
	payload := append([]byte{3}, []byte(text)...)
	writeFrameHeader(w, frameID, len(payload))
	w.Write(payload)
}

func writeMultiTextFrame(w io.Writer, frameID string, multiVal string) {
	// Преобразуем '\\' или ';' в \x00
	items := ParseMultiValue(multiVal)
	if len(items) == 0 {
		writeTextFrame(w, frameID, multiVal)
		return
	}

	var payload []byte
	payload = append(payload, 3) // UTF-8
	for i, item := range items {
		if i > 0 {
			payload = append(payload, 0) // \x00 разделитель стандарта ID3v2.4
		}
		payload = append(payload, []byte(item)...)
	}

	writeFrameHeader(w, frameID, len(payload))
	w.Write(payload)
}

func writeGenreFrame(w io.Writer, genreStr string) {
	items := ParseGenres(genreStr)
	if len(items) == 0 {
		trimmed := strings.TrimSpace(genreStr)
		if trimmed != "" {
			writeTextFrame(w, "TCON", trimmed)
		}
		return
	}

	var payload []byte
	payload = append(payload, 3) // UTF-8
	for i, item := range items {
		if i > 0 {
			payload = append(payload, 0) // \x00 разделитель стандарта ID3v2.4
		}
		payload = append(payload, []byte(item)...)
	}

	writeFrameHeader(w, "TCON", len(payload))
	w.Write(payload)
}

func writeAPICFrame(w io.Writer, cover *CoverInfo) {
	mime := cover.MimeType
	if mime == "" {
		mime = "image/jpeg"
	}

	var payload bytes.Buffer
	payload.WriteByte(0) // ISO-8859-1 для MIME типа
	payload.WriteString(mime)
	payload.WriteByte(0) // Терминатор MIME типа
	payload.WriteByte(3) // Picture type: 3 = Cover (front)
	payload.WriteByte(0) // Пустое описание (terminated by \x00)
	payload.Write(cover.Data)

	data := payload.Bytes()
	writeFrameHeader(w, "APIC", len(data))
	w.Write(data)
}

func writeFrameHeader(w io.Writer, frameID string, size int) {
	var hdr [10]byte
	copy(hdr[0:4], frameID)
	encodeSynchsafe(size, hdr[4:8])
	hdr[8] = 0 // flags
	hdr[9] = 0
	w.Write(hdr[:])
}

func writeRawFrame(w io.Writer, f RawFrame) {
	var hdr [10]byte
	copy(hdr[0:4], f.ID)
	encodeSynchsafe(len(f.Data), hdr[4:8])
	hdr[8] = f.Flags[0]
	hdr[9] = f.Flags[1]
	w.Write(hdr[:])
	w.Write(f.Data)
}

// isArtistOrConflictingTXXX определяет, является ли пользовательский TXXX фрейм конфликтующим тегом артистов
// (TXXX:ARTISTS, TXXX:ALBUMARTISTS и др.), который перебивает чистые ID3v2.4 фреймы TPE1/TPE2 в Navidrome/TagLib.
func isArtistOrConflictingTXXX(f RawFrame) bool {
	if f.ID != "TXXX" || len(f.Data) < 2 {
		return false
	}
	desc := ExtractTXXXDesc(f.Data)
	descLower := strings.ToLower(strings.TrimSpace(desc))
	switch descLower {
	case "artists", "albumartists", "album artists", "artist", "album artist", "album_artist":
		return true
	default:
		return false
	}
}

// ExtractTXXXDesc извлекает текстовое описание фрейма TXXX с учетом кодировки байта [0].
func ExtractTXXXDesc(data []byte) string {
	return extractTXXXDescription(data)
}

func extractTXXXDescription(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	enc := data[0]
	payload := data[1:]

	switch enc {
	case 1: // UTF-16 with BOM
		for i := 0; i+1 < len(payload); i += 2 {
			if payload[i] == 0 && payload[i+1] == 0 {
				parts := decodeUTF16(payload[:i], true)
				return strings.Join(parts, " ")
			}
		}
		parts := decodeUTF16(payload, true)
		return strings.Join(parts, " ")
	case 2: // UTF-16BE without BOM
		for i := 0; i+1 < len(payload); i += 2 {
			if payload[i] == 0 && payload[i+1] == 0 {
				parts := decodeUTF16(payload[:i], false)
				return strings.Join(parts, " ")
			}
		}
		parts := decodeUTF16(payload, false)
		return strings.Join(parts, " ")
	case 3: // UTF-8
		idx := bytes.IndexByte(payload, 0)
		if idx != -1 {
			return string(payload[:idx])
		}
		return string(payload)
	default: // ISO-8859-1
		idx := bytes.IndexByte(payload, 0)
		if idx != -1 {
			return decodeISO88591(payload[:idx])
		}
		return decodeISO88591(payload)
	}
}
