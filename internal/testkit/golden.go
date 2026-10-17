package testkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AssertGoldenJSON сравнивает actual JSON с содержимым эталонного goldenPath.
// Если goldenPath отсутствует или установлена переменная окружения UPDATE_GOLDEN=true / 1,
// файл автоматически создается или перезаписывается каноническим форматированным JSON.
func AssertGoldenJSON(t *testing.T, actual []byte, goldenPath string) {
	t.Helper()

	if len(bytes.TrimSpace(actual)) == 0 {
		t.Fatalf("testkit: actual JSON payload is empty for golden path: %s", goldenPath)
	}

	// 1. Нормализация и форматирование actual JSON с отступами в 2 пробела
	prettyActual, err := normalizeJSON(actual)
	if err != nil {
		t.Fatalf("testkit: invalid actual JSON for golden comparison (%s): %v\nActual payload:\n%s",
			goldenPath, err, string(actual))
	}

	update := os.Getenv("UPDATE_GOLDEN") == "true" || os.Getenv("UPDATE_GOLDEN") == "1"

	// 2. Чтение или создание эталонного файла
	expected, readErr := os.ReadFile(goldenPath)
	if os.IsNotExist(readErr) {
		isCI := os.Getenv("CI") == "true" || os.Getenv("CI") == "1"
		if isCI && !update {
			t.Fatalf("testkit: golden file %q does not exist in CI environment (run with UPDATE_GOLDEN=true locally and commit)", goldenPath)
		}

		if err := os.MkdirAll(filepath.Dir(goldenPath), 0755); err != nil {
			t.Fatalf("testkit: failed to create directory for golden file %s: %v", goldenPath, err)
		}
		if err := os.WriteFile(goldenPath, prettyActual, 0644); err != nil {
			t.Fatalf("testkit: failed to write initial golden file %s: %v", goldenPath, err)
		}
		t.Logf("testkit: created initial golden file: %s", goldenPath)
		return
	} else if readErr != nil {
		t.Fatalf("testkit: failed to read golden file %s: %v", goldenPath, readErr)
	}

	// 3. Автоматическое обновление эталона при наличии флага UPDATE_GOLDEN
	if update {
		if err := os.WriteFile(goldenPath, prettyActual, 0644); err != nil {
			t.Fatalf("testkit: failed to update golden file %s: %v", goldenPath, err)
		}
		t.Logf("testkit: updated golden file: %s", goldenPath)
		return
	}

	// 4. Нормализация эталонного файла для исключения различий в пробелах/форматировании
	prettyExpected, err := normalizeJSON(expected)
	if err != nil {
		t.Fatalf("testkit: golden file %s contains invalid JSON: %v\nFile content:\n%s",
			goldenPath, err, string(expected))
	}

	// 5. Побайтовое сравнение
	if bytes.Equal(prettyActual, prettyExpected) {
		return
	}

	// 6. Формирование наглядного Unified Diff при расхождении
	diffMsg := formatJSONDiff(goldenPath, prettyExpected, prettyActual)
	t.Fatalf("testkit: golden file mismatch (%s):\nRun with UPDATE_GOLDEN=true to accept changes.\n\n%s",
		goldenPath, diffMsg)
}

// normalizeJSON парсит сырой JSON и сериализует его с отступами в 2 пробела и сортированными ключами.
func normalizeJSON(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var val any
	if err := dec.Decode(&val); err != nil {
		return nil, err
	}

	pretty, err := json.MarshalIndent(val, "", "  ")
	if err != nil {
		return nil, err
	}

	// Гарантируем завершающий символ новой строки стандарта POSIX
	pretty = append(pretty, '\n')
	return pretty, nil
}

// formatJSONDiff генерирует компактный unified-дифф между ожидаемым и фактическим JSON.
func formatJSONDiff(goldenPath string, wantBytes, gotBytes []byte) string {
	wantLines := strings.Split(strings.TrimRight(string(wantBytes), "\n"), "\n")
	gotLines := strings.Split(strings.TrimRight(string(gotBytes), "\n"), "\n")

	maxLines := len(wantLines)
	if len(gotLines) > maxLines {
		maxLines = len(gotLines)
	}

	firstDiff := -1
	for i := 0; i < maxLines; i++ {
		var wLine, gLine string
		if i < len(wantLines) {
			wLine = wantLines[i]
		}
		if i < len(gotLines) {
			gLine = gotLines[i]
		}
		if wLine != gLine {
			firstDiff = i
			break
		}
	}

	if firstDiff == -1 {
		return "no line-by-line differences detected (check whitespace or encodings)"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- WANT (%s)\n", goldenPath))
	sb.WriteString("+++ GOT (actual)\n")

	start := firstDiff - 2
	if start < 0 {
		start = 0
	}
	end := firstDiff + 6
	if end > maxLines {
		end = maxLines
	}

	sb.WriteString(fmt.Sprintf("@@ line %d @@\n", firstDiff+1))
	for i := start; i < firstDiff; i++ {
		if i < len(wantLines) {
			sb.WriteString("  " + wantLines[i] + "\n")
		}
	}

	for i := firstDiff; i < end; i++ {
		hasW := i < len(wantLines)
		hasG := i < len(gotLines)
		var wLine, gLine string
		if hasW {
			wLine = wantLines[i]
		}
		if hasG {
			gLine = gotLines[i]
		}

		if hasW && hasG && wLine == gLine {
			sb.WriteString("  " + wLine + "\n")
		} else {
			if hasW {
				sb.WriteString("- " + wLine + "\n")
			}
			if hasG {
				sb.WriteString("+ " + gLine + "\n")
			}
		}
	}

	if end < maxLines {
		sb.WriteString("  ...\n")
	}

	return sb.String()
}
