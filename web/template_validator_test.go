package web_test

import (
	"bufio"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"music-manager/web"
)

// Стандартные глобальные функции браузера и Alpine.js
var allowedGlobalFunctions = map[string]bool{
	"confirm":            true,
	"alert":              true,
	"prompt":             true,
	"parseInt":           true,
	"parseFloat":         true,
	"Number":             true,
	"String":             true,
	"Boolean":            true,
	"Array":              true,
	"Object":             true,
	"encodeURIComponent": true,
	"decodeURIComponent": true,
	"CustomEvent":        true,
	"setTimeout":         true,
	"setInterval":        true,
	"clearTimeout":       true,
	"clearInterval":      true,
	"$dispatch":          true,
	"$nextTick":          true,
	"$watch":             true,
}

// Стандартные прототипные методы JS объектов
var allowedPrototypeMethods = map[string]bool{
	"trim":                     true,
	"split":                    true,
	"join":                     true,
	"slice":                    true,
	"substring":                true,
	"toLowerCase":              true,
	"toUpperCase":              true,
	"replace":                  true,
	"replaceAll":               true,
	"includes":                 true,
	"indexOf":                  true,
	"lastIndexOf":              true,
	"startsWith":               true,
	"endsWith":                 true,
	"filter":                   true,
	"map":                      true,
	"find":                     true,
	"findIndex":                true,
	"some":                     true,
	"every":                    true,
	"forEach":                  true,
	"reduce":                   true,
	"push":                     true,
	"pop":                      true,
	"shift":                    true,
	"unshift":                  true,
	"splice":                   true,
	"concat":                   true,
	"reverse":                  true,
	"sort":                     true,
	"preventDefault":           true,
	"stopPropagation":          true,
	"stopImmediatePropagation": true,
	"focus":                    true,
	"blur":                     true,
	"select":                   true,
	"click":                    true,
	"submit":                   true,
	"reset":                    true,
	"addEventListener":         true,
	"removeEventListener":      true,
	"dispatchEvent":            true,
	"getTime":                  true,
	"toFixed":                  true,
	"toString":                 true,
	"has":                      true,
	"add":                      true,
	"delete":                   true,
	"clear":                    true,
}

// TestTemplates_AllXForHaveKey проверяет, что каждый тег <template x-for="...">
// содержит обязательный атрибут :key, предотвращая фантомные дубликаты и рассинхронизацию DOM.
func TestTemplates_AllXForHaveKey(t *testing.T) {
	reXFor := regexp.MustCompile(`(?s)<template[^>]*x-for=[^>]*>`)
	reKey := regexp.MustCompile(`:key\s*=\s*["']`)

	missingKeysCount := 0

	err := fs.WalkDir(web.Assets, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}

		data, err := fs.ReadFile(web.Assets, path)
		if err != nil {
			return err
		}

		matches := reXFor.FindAllString(string(data), -1)
		for _, m := range matches {
			if !reKey.MatchString(m) {
				t.Errorf("Шаблон %s содержит <template x-for> без обязательного атрибута :key:\n%s", path, m)
				missingKeysCount++
			}
		}

		return nil
	})

	if err != nil {
		t.Fatalf("Ошибка обхода шаблонов: %v", err)
	}

	if missingKeysCount > 0 {
		t.Fatalf("Обнаружено %d циклов x-for без :key. Это приводит к дублированию элементов в DOM!", missingKeysCount)
	}
}

// TestTemplates_ClickHandlersIntegrity собирает все объявленные функции в static/js/
// и сверяет их со всеми вызовами в шаблонах (@click, @submit, @change), выявляя опечатки
// и несуществующие методы еще до запуска браузера.
func TestTemplates_ClickHandlersIntegrity(t *testing.T) {
	jsMethods := collectJSMethods(t)
	t.Logf("Обнаружено %d объявленных JS-методов в static/js/", len(jsMethods))

	reEventAttr := regexp.MustCompile(`(?:@click[^\s=]*|@submit[^\s=]*|@change[^\s=]*)\s*=\s*["']([^"']+)["']`)
	reCall := regexp.MustCompile(`([a-zA-Z0-9_$.]+)\s*\(`)

	unresolvedCount := 0

	err := fs.WalkDir(web.Assets, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}

		data, err := fs.ReadFile(web.Assets, path)
		if err != nil {
			return err
		}

		content := string(data)
		attrMatches := reEventAttr.FindAllStringSubmatch(content, -1)
		for _, attr := range attrMatches {
			handler := attr[1]
			callMatches := reCall.FindAllStringSubmatch(handler, -1)
			for _, call := range callMatches {
				funcCall := call[1]
				parts := strings.Split(funcCall, ".")
				methodName := parts[len(parts)-1]

				// Игнорируем стандартные языковые конструкции: if, switch, catch, for, while
				if methodName == "if" || methodName == "switch" || methodName == "catch" || methodName == "for" || methodName == "while" {
					continue
				}

				// Пропускаем глобальные функции и встроенные прототипные методы
				if allowedGlobalFunctions[methodName] || allowedPrototypeMethods[methodName] {
					continue
				}

				// Если это вызов метода стора или сервиса: $store.app.openModal -> openModal
				// Либо прямой вызов компонента: saveAllChanges() -> saveAllChanges
				if !jsMethods[methodName] && !allowedGlobalFunctions[funcCall] {
					t.Errorf("[%s] Вызов необъявленного метода '%s' (полный путь: '%s') в обработчике: %s",
						path, methodName, funcCall, handler)
					unresolvedCount++
				}
			}
		}

		return nil
	})

	if err != nil {
		t.Fatalf("Ошибка проверки шаблонов: %v", err)
	}

	if unresolvedCount > 0 {
		t.Fatalf("Обнаружено %d вызовов несуществующих методов в шаблонах! Проверьте имена функций.", unresolvedCount)
	}
}

// collectJSMethods сканирует все .js файлы построчно и извлекает имена функций и методов
func collectJSMethods(t *testing.T) map[string]bool {
	methods := make(map[string]bool)

	// Построчные регулярные выражения для поиска объявлений
	reFunc1 := regexp.MustCompile(`(?:function\s+|async\s+function\s+)([a-zA-Z0-9_$]+)\s*\(`)
	reFunc2 := regexp.MustCompile(`^\s*(?:async\s+)?([a-zA-Z0-9_$]+)\s*\([^)]*\)\s*\{`)
	reFunc3 := regexp.MustCompile(`^\s*([a-zA-Z0-9_$]+)\s*:\s*(?:async\s+)?(?:function|\([^)]*\)\s*=>)`)

	err := fs.WalkDir(web.Assets, "static/js", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".js") {
			return nil
		}

		data, err := fs.ReadFile(web.Assets, path)
		if err != nil {
			return err
		}

		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			line := scanner.Text()
			if m := reFunc1.FindStringSubmatch(line); len(m) > 1 {
				name := m[1]
				if name != "if" && name != "catch" && name != "switch" {
					methods[name] = true
				}
			}
			if m := reFunc2.FindStringSubmatch(line); len(m) > 1 {
				name := m[1]
				if name != "if" && name != "catch" && name != "switch" {
					methods[name] = true
				}
			}
			if m := reFunc3.FindStringSubmatch(line); len(m) > 1 {
				name := m[1]
				if name != "if" && name != "catch" && name != "switch" {
					methods[name] = true
				}
			}
		}

		return scanner.Err()
	})

	if err != nil {
		t.Fatalf("Ошибка чтения static/js: %v", err)
	}

	return methods
}

// TestTemplates_PuddletagSaveButtonNotBlockedInDownloaderMode гарантирует,
// что кнопка сохранения в панели инструментов Puddletag разблокирована в режиме загрузчика
// (isDownloaderMode) даже если пользователь не делал ручных правок (hasDirtyChanges == false).
func TestTemplates_PuddletagSaveButtonNotBlockedInDownloaderMode(t *testing.T) {
	data, err := fs.ReadFile(web.Assets, "templates/components/puddletag_toolbar.html")
	if err != nil {
		t.Fatalf("Ошибка чтения puddletag_toolbar.html: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "!isDownloaderMode && !hasDirtyChanges") && !strings.Contains(content, "isDownloaderMode || hasDirtyChanges") {
		t.Errorf("Обнаружена регрессия в кнопке сохранения Puddletag: в режиме загрузчика кнопка не должна блокироваться отсутствием dirty-изменений!")
	}
}

// TestTemplates_NoInlineFetch гарантирует, что в HTML-шаблонах отсутствуют инлайн-вызовы fetch,
// и все сетевые обращения делегированы сторам или типизированному API-клиенту.
func TestTemplates_NoInlineFetch(t *testing.T) {
	reFetch := regexp.MustCompile(`\bfetch\s*\(`)

	fetchCount := 0
	err := fs.WalkDir(web.Assets, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}

		data, err := fs.ReadFile(web.Assets, path)
		if err != nil {
			return err
		}

		if matches := reFetch.FindAllString(string(data), -1); len(matches) > 0 {
			t.Errorf("Шаблон %s содержит %d прямых вызовов fetch(). Используйте Alpine.store или api клиент!", path, len(matches))
			fetchCount += len(matches)
		}

		return nil
	})

	if err != nil {
		t.Fatalf("Ошибка обхода шаблонов: %v", err)
	}

	if fetchCount > 0 {
		t.Fatalf("Обнаружено %d инлайн-вызовов fetch() в шаблонах!", fetchCount)
	}
}

// All browser networking passes through the shared response/API client.
func TestJavaScriptNetworkingUsesAPIClient(t *testing.T) {
	err := fs.WalkDir(web.Assets, "static/js", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".js") || path == "static/js/api/client.js" {
			return nil
		}
		b, err := fs.ReadFile(web.Assets, path)
		if err != nil {
			return err
		}
		if regexp.MustCompile(`\bfetch\s*\(`).Match(b) {
			t.Errorf("direct fetch in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
