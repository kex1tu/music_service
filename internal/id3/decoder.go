package id3

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// decodeSynchsafe декодирует 28-битное synchsafe целое число ID3v2.
func decodeSynchsafe(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	return int(b[0]&0x7F)<<21 | int(b[1]&0x7F)<<14 | int(b[2]&0x7F)<<7 | int(b[3]&0x7F)
}

// isValidFrameID проверяет, состоит ли 4-байтный идентификатор фрейма из допустимых символов [A-Z0-9].
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

// isTextFrame определяет, является ли идентификатор фрейма текстовым фреймом ID3v2.
func isTextFrame(id string) bool {
	switch id {
	case "TIT2", "TPE1", "TPE2", "TALB", "TCON", "TRCK", "TPOS", "TDRC", "TYER", "TLEN", "TXXX", "TSRC":
		return true
	default:
		return false
	}
}

// decodeText декодирует байты текстового фрейма с учетом байта кодировки (0=ISO-8859-1, 1=UTF-16 BOM, 2=UTF-16BE, 3=UTF-8).
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

// decodeISO88591 декодирует сырые байты ISO-8859-1 в валидную строку UTF-8.
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

// decodeUTF16 декодирует последовательность UTF-16 байт в срез строк, разделенных нуль-терминаторами.
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
