package sandbox

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	root string
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

	return &PathSandbox{root: filepath.Clean(absRoot)}, nil
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

// SanitizeFilename очищает имя файла от опасных системных символов и разделителей.
func SanitizeFilename(name string, fallback string) string {
	clean := strings.TrimSpace(name)
	if clean == "" {
		return fallback
	}

	// Замена разделителей на ' & '
	clean = multiSeparatorRegex.ReplaceAllString(clean, " & ")
	clean = dangerousFilenameChars.ReplaceAllString(clean, "_")
	clean = strings.Trim(clean, ". ")

	if clean == "" {
		return fallback
	}
	return clean
}
