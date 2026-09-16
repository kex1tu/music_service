package id3

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

func decodeSynchsafe(b []byte) int {
	return int(b[0]&0x7F)<<21 | int(b[1]&0x7F)<<14 | int(b[2]&0x7F)<<7 | int(b[3]&0x7F)
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

func isID3v22TextFrame(id string) bool {
	switch id {
	case "TT2", "TP1", "TP2", "TAL", "TCO", "TRK", "TYE":
		return true
	default:
		return false
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
