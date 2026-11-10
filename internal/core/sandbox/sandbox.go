package sandbox

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

var (
	// ErrPathTraversal возвращается при попытке выхода за пределы песочницы.
	ErrPathTraversal = errors.New("попытка выхода за пределы песочницы (path traversal)")
	// ErrStorageConfig возвращается при ошибках конфигурации корневого каталога.
	ErrStorageConfig = errors.New("ошибка конфигурации хранилища")
)

var (
	dangerousFilenameChars = regexp.MustCompile(`[\x00/\\:*?"<>|]`)
	multiSeparatorRegex    = regexp.MustCompile(`(\\+|\x00)`)
)

// PathSandbox обеспечивает строгую изоляцию операций файловой системы внутри заданного корня.
type PathSandbox struct {
	root     string
	handleMu sync.Mutex
	handle   *os.Root
}

// NewPathSandbox инициализирует новую песочницу и проверяет доступность корня.
func NewPathSandbox(rootPath string) (*PathSandbox, error) {
	if strings.TrimSpace(rootPath) == "" {
		return nil, fmt.Errorf("%w: корневой каталог не может быть пустым", ErrStorageConfig)
	}

	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("%w: не удалось определить абсолютный путь: %v", ErrStorageConfig, err)
	}

	// Раскрываем симлинки для корня
	if realRoot, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = realRoot
	}

	st, err := os.Stat(absRoot)
	if err == nil && !st.IsDir() {
		return nil, fmt.Errorf("%w: путь '%s' существует, но не является директорией", ErrStorageConfig, absRoot)
	}

	sb := &PathSandbox{root: filepath.Clean(absRoot)}
	if err == nil {
		sb.handle, err = os.OpenRoot(sb.root)
		if err != nil {
			return nil, err
		}
	}
	register(sb)
	return sb, nil
}

// Root возвращает канонический абсолютный путь корня песочницы.
func (s *PathSandbox) Root() string {
	return s.root
}

// Resolve разрешает относительный путь внутри песочницы с защитой от traversal, symlink bypass и null-byte.
func (s *PathSandbox) Resolve(relativePath string, allowRoot bool) (string, error) {
	if strings.Contains(relativePath, "\x00") {
		return "", fmt.Errorf("%w: обнаружен нулевой байт", ErrPathTraversal)
	}

	// 1. Итеративное URL-декодирование (защита от многократного кодирования %252e%252e)
	cleaned := relativePath
	for i := 0; i < 10; i++ {
		if strings.Contains(cleaned, "%") {
			if decoded, err := url.QueryUnescape(cleaned); err == nil && decoded != cleaned {
				cleaned = decoded
				continue
			}
		}
		break
	}

	if strings.Contains(cleaned, "\x00") {
		return "", fmt.Errorf("%w: обнаружен нулевой байт после URL-декодирования", ErrPathTraversal)
	}

	// 2. Нормализация Unicode fullwidth слэшей и точек
	cleaned = strings.ReplaceAll(cleaned, "\uff0f", "/") // ／
	cleaned = strings.ReplaceAll(cleaned, "\uff3c", "/") // ＼
	cleaned = strings.ReplaceAll(cleaned, "\uff0e", ".") // ．

	// 3. Нормализация слэшей
	cleaned = strings.ReplaceAll(cleaned, "\\", "/")
	cleaned = strings.TrimLeft(cleaned, "/")
	cleaned = filepath.Clean(cleaned)

	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: путь '%s' выходит за пределы корня", ErrPathTraversal, relativePath)
	}

	// 4. Формирование абсолютного пути
	targetAbs := filepath.Join(s.root, cleaned)

	// 4. Проверка раскрытия симлинков, если путь или его родитель существует
	checkPath := targetAbs
	for checkPath != s.root && checkPath != "/" && checkPath != "." {
		if realPath, err := filepath.EvalSymlinks(checkPath); err == nil {
			relToRoot, err := filepath.Rel(s.root, realPath)
			if err != nil || strings.HasPrefix(relToRoot, "..") || strings.HasPrefix(relToRoot, "/") {
				return "", fmt.Errorf("%w: симлинк '%s' ведет за пределы корня ('%s')", ErrPathTraversal, checkPath, realPath)
			}
			break
		}
		checkPath = filepath.Dir(checkPath)
	}

	// 5. Строгая проверка containment
	rel, err := filepath.Rel(s.root, targetAbs)
	if err != nil || strings.HasPrefix(rel, "..") || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: путь '%s' выходит за пределы корня", ErrPathTraversal, relativePath)
	}

	// 6. Контроль доступа к самому корню
	if !allowRoot && (rel == "." || targetAbs == s.root) {
		return "", fmt.Errorf("%w: доступ к корню песочницы запрещен", ErrPathTraversal)
	}

	return targetAbs, nil
}

// ToRelative преобразует абсолютный путь внутри песочницы в относительный (с прямыми слэшами).
func (s *PathSandbox) ToRelative(absPath string) (string, error) {
	rel, err := filepath.Rel(s.root, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%w: путь '%s' не принадлежит корню '%s'", ErrPathTraversal, absPath, s.root)
	}
	return filepath.ToSlash(rel), nil
}

// TruncateUTF8Bytes усекает строку до maxBytes байт без повреждения multi-byte UTF-8 рун.
func TruncateUTF8Bytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	lastValid := 0
	for i := 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:])
		if i+size > maxBytes {
			break
		}
		i += size
		lastValid = i
	}
	return s[:lastValid]
}

// SanitizeFilenameWithBudget очищает имя файла от опасных системных символов и разделителей
// и ограничивает длину до maxBytes без повреждения UTF-8 рун.
func SanitizeFilenameWithBudget(name string, fallback string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	clean := strings.TrimSpace(name)
	if clean == "" {
		clean = strings.TrimSpace(fallback)
	}

	// Замена разделителей на ' & '
	clean = multiSeparatorRegex.ReplaceAllString(clean, " & ")
	clean = dangerousFilenameChars.ReplaceAllString(clean, "_")
	clean = strings.Trim(clean, ". ")

	if len(clean) > maxBytes {
		clean = strings.TrimRight(TruncateUTF8Bytes(clean, maxBytes), ". ")
	}

	if clean == "" {
		fallbackClean := strings.TrimSpace(fallback)
		fallbackClean = multiSeparatorRegex.ReplaceAllString(fallbackClean, " & ")
		fallbackClean = dangerousFilenameChars.ReplaceAllString(fallbackClean, "_")
		fallbackClean = strings.Trim(fallbackClean, ". ")
		if len(fallbackClean) > maxBytes {
			fallbackClean = strings.TrimRight(TruncateUTF8Bytes(fallbackClean, maxBytes), ". ")
		}
		clean = fallbackClean
	}
	return clean
}

// SanitizeFilename очищает имя файла от опасных системных символов и разделителей (до 255 байт).
func SanitizeFilename(name string, fallback string) string {
	return SanitizeFilenameWithBudget(name, fallback, 255)
}
