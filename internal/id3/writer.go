package id3

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// WriteTags записывает канонические теги стандарта ID3v2.4 в MP3-файл.
func WriteTags(filePath string, tags TrackTags, cover *CoverInfo) error {
	// Считываем существующие фреймы для сохранения нетипизированных тегов (COMM, TCOP, ISRC и др.)
	existingFrames, _ := ReadRawFrames(filePath)

	// 1. Считываем аудиопоток (пропуская существующий ID3v2 заголовок)
	file, err := os.Open(filePath)
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

	// TPE2: Исполнитель альбома
	if strings.TrimSpace(tags.AlbumArtist) != "" {
		writeTextFrame(&framesBuf, "TPE2", tags.AlbumArtist)
	}

	// TALB: Альбом
	if strings.TrimSpace(tags.Album) != "" {
		writeTextFrame(&framesBuf, "TALB", tags.Album)
	}

	// TCON: Жанр (поддержка null-separated \x00)
	if strings.TrimSpace(tags.Genre) != "" {
		writeMultiTextFrame(&framesBuf, "TCON", tags.Genre)
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

	// APIC: Фронтальная обложка
	if cover != nil && len(cover.Data) > 0 {
		writeAPICFrame(&framesBuf, cover)
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

	for _, ef := range existingFrames {
		if !managed[ef.ID] && isValidFrameID(ef.ID) && len(ef.Data) > 0 {
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

	// 4. Атомарная запись во временный файл
	dir := filepath.Dir(filePath)
	tmpFile, err := os.CreateTemp(dir, "tag_write_*.tmp")
	if err != nil {
		return fmt.Errorf("не удалось создать временный файл: %w", err)
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)

	// Заголовок
	if _, err := tmpFile.Write(id3Hdr[:]); err != nil {
		tmpFile.Close()
		return err
	}
	// Фреймы
	if _, err := tmpFile.Write(framesBytes); err != nil {
		tmpFile.Close()
		return err
	}
	// Padding (нули)
	if paddingLen > 0 {
		padding := make([]byte, paddingLen)
		if _, err := tmpFile.Write(padding); err != nil {
			tmpFile.Close()
			return err
		}
	}

	// Копируем аудиоданные
	if _, err := io.Copy(tmpFile, file); err != nil {
		tmpFile.Close()
		return fmt.Errorf("ошибка копирования аудиопотока: %w", err)
	}
	tmpFile.Close()
	file.Close()

	// Атомарная замена файла
	if err := os.Rename(tmpName, filePath); err != nil {
		return fmt.Errorf("не удалось обновить файл тегов: %w", err)
	}

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
