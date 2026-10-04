package resolver

import (
	"bufio"
	"os"
	"strings"
)

// IsValidNetscapeCookiesFile проверяет, существует ли файл куков, не пустой ли он,
// и содержит ли он стандартный заголовок Netscape / Mozilla cookie file.
//
// yt-dlp использует Python http.cookiejar.MozillaCookieJar, который аварийно завершает
// работу с кодом 1, если файл пустой (0 байт) или в его начале отсутствует сигнатура
// "# Netscape HTTP Cookie File" или "# HTTP Cookie File".
func IsValidNetscapeCookiesFile(path string) bool {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return false
	}

	info, err := os.Stat(cleanPath)
	if err != nil || info.IsDir() || info.Size() < 10 {
		return false
	}

	f, err := os.Open(cleanPath)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	linesChecked := 0
	for scanner.Scan() && linesChecked < 10 {
		line := strings.TrimSpace(scanner.Text())
		linesChecked++
		if line == "" {
			continue
		}
		// Устраняем возможный UTF-8 BOM (\xef\xbb\xbf)
		line = strings.TrimPrefix(line, "\xef\xbb\xbf")
		lineLower := strings.ToLower(line)
		if strings.HasPrefix(lineLower, "# netscape http cookie file") ||
			strings.HasPrefix(lineLower, "#: netscape http cookie file") ||
			strings.HasPrefix(lineLower, "# http cookie file") ||
			strings.HasPrefix(lineLower, "#: http cookie file") {
			return true
		}
		// Если первая непустая строка не содержит сигнатуру заголовка Netscape,
		// Python MozillaCookieJar гарантированно выбросит исключение LoadError.
		break
	}

	return false
}

// CleanYtDlpStderr очищает вывод stderr yt-dlp от нефатальных deprecation-предупреждений Python
// и прочих вспомогательных строк, оставляя содержательное сообщение об ошибке.
func CleanYtDlpStderr(errStr string) string {
	lines := strings.Split(errStr, "\n")
	var clean []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "Deprecated Feature:") {
			continue
		}
		clean = append(clean, trimmed)
	}
	return strings.Join(clean, " ")
}

