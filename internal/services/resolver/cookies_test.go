package resolver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsValidNetscapeCookiesFile(t *testing.T) {
	// 1. Пустой путь
	if IsValidNetscapeCookiesFile("") {
		t.Errorf("expected false for empty path")
	}

	// 2. Несуществующий файл
	if IsValidNetscapeCookiesFile("/path/does/not/exist/cookies.txt") {
		t.Errorf("expected false for non-existent path")
	}

	tempDir := t.TempDir()

	// 3. Директория вместо файла
	if IsValidNetscapeCookiesFile(tempDir) {
		t.Errorf("expected false for directory")
	}

	// 4. Пустой файл (0 байт) - типичный кейс `touch cookies.txt`
	emptyFile := filepath.Join(tempDir, "empty.txt")
	if err := os.WriteFile(emptyFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if IsValidNetscapeCookiesFile(emptyFile) {
		t.Errorf("expected false for empty 0-byte file")
	}

	// 5. Файл с мусором / некорректным форматом
	junkFile := filepath.Join(tempDir, "junk.txt")
	if err := os.WriteFile(junkFile, []byte("some random cookies data\nwithout netscape header"), 0644); err != nil {
		t.Fatal(err)
	}
	if IsValidNetscapeCookiesFile(junkFile) {
		t.Errorf("expected false for junk file")
	}

	// 6. Валидный файл Netscape
	validFile := filepath.Join(tempDir, "valid.txt")
	validContent := "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t1800000000\tSID\t12345\n"
	if err := os.WriteFile(validFile, []byte(validContent), 0644); err != nil {
		t.Fatal(err)
	}
	if !IsValidNetscapeCookiesFile(validFile) {
		t.Errorf("expected true for valid netscape cookies file")
	}

	// 7. Валидный файл с UTF-8 BOM
	bomFile := filepath.Join(tempDir, "bom.txt")
	bomContent := "\xef\xbb\xbf# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t1800000000\tSID\t12345\n"
	if err := os.WriteFile(bomFile, []byte(bomContent), 0644); err != nil {
		t.Fatal(err)
	}
	if !IsValidNetscapeCookiesFile(bomFile) {
		t.Errorf("expected true for valid netscape cookies file with BOM")
	}

	// 8. Валидный файл с альтернативным заголовком "# HTTP Cookie File"
	altFile := filepath.Join(tempDir, "alt.txt")
	altContent := "# HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t1800000000\tSID\t12345\n"
	if err := os.WriteFile(altFile, []byte(altContent), 0644); err != nil {
		t.Fatal(err)
	}
	if !IsValidNetscapeCookiesFile(altFile) {
		t.Errorf("expected true for valid alt header cookies file")
	}

	// 9. Валидный файл с пустыми строками в начале
	leadingNewlinesFile := filepath.Join(tempDir, "leading.txt")
	leadingContent := "\n\n# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t1800000000\tSID\t12345\n"
	if err := os.WriteFile(leadingNewlinesFile, []byte(leadingContent), 0644); err != nil {
		t.Fatal(err)
	}
	if !IsValidNetscapeCookiesFile(leadingNewlinesFile) {
		t.Errorf("expected true for valid cookies file with leading newlines")
	}
}

func TestCleanYtDlpStderr(t *testing.T) {
	raw := "Deprecated Feature: Support for Python version 3.10 has been deprecated. Please update to Python 3.11 or above\nERROR: Video unavailable"
	cleaned := CleanYtDlpStderr(raw)
	expected := "ERROR: Video unavailable"
	if cleaned != expected {
		t.Errorf("expected %q, got %q", expected, cleaned)
	}
}
