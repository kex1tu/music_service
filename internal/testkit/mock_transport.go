package testkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// FixtureEntry представляет зарегистрированную фикстуру ответа.
type FixtureEntry struct {
	Pattern     string
	StatusCode  int
	ContentType string
	Body        []byte
	Handler     func(req *http.Request) (*http.Response, error)
}

// MockTransport реализует http.RoundTripper для перехвата внешних HTTP-запросов.
type MockTransport struct {
	mu                   sync.RWMutex
	fixtures             []FixtureEntry
	passthroughTransport http.RoundTripper
}

// NewMockTransport создает новый экземпляр мок-транспорта.
func NewMockTransport(passthrough ...http.RoundTripper) *MockTransport {
	var pt http.RoundTripper = &http.Transport{}
	if len(passthrough) > 0 && passthrough[0] != nil {
		pt = passthrough[0]
	}

	return &MockTransport{
		fixtures:             make([]FixtureEntry, 0),
		passthroughTransport: pt,
	}
}

// RegisterFixture регистрирует фикстуру ответа для заданного шаблона URL.
func (m *MockTransport) RegisterFixture(urlPattern string, statusCode int, contentType string, body []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if contentType == "" {
		contentType = "application/json"
	}

	// Удаляем существующие фикстуры с точно таким же шаблоном
	filtered := make([]FixtureEntry, 0, len(m.fixtures))
	for _, f := range m.fixtures {
		if f.Pattern != urlPattern {
			filtered = append(filtered, f)
		}
	}

	filtered = append(filtered, FixtureEntry{
		Pattern:     urlPattern,
		StatusCode:  statusCode,
		ContentType: contentType,
		Body:        body,
	})

	// Сортируем: более длинные шаблоны имеют приоритет над короткими префиксами
	sort.SliceStable(filtered, func(i, j int) bool {
		return len(filtered[i].Pattern) > len(filtered[j].Pattern)
	})

	m.fixtures = filtered
}

// RegisterJSON регистрирует JSON-фикстуру с автоматической сериализацией.
func (m *MockTransport) RegisterJSON(urlPattern string, statusCode int, data any) {
	bytesData, err := json.Marshal(data)
	if err != nil {
		bytesData = []byte(fmt.Sprintf(`{"error":"failed to marshal mock json: %v"}`, err))
	}
	m.RegisterFixture(urlPattern, statusCode, "application/json", bytesData)
}

// RegisterHandler регистрирует динамический обработчик запроса.
func (m *MockTransport) RegisterHandler(urlPattern string, fn func(req *http.Request) (*http.Response, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry := FixtureEntry{
		Pattern: urlPattern,
		Handler: fn,
	}
	m.fixtures = append([]FixtureEntry{entry}, m.fixtures...)
}

// Reset очищает все зарегистрированные фикстуры.
func (m *MockTransport) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fixtures = make([]FixtureEntry, 0)
}

// RoundTrip перехватывает HTTP-запросы и возвращает фикстуру или fallback.
func (m *MockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("mock transport: nil request or request URL")
	}

	urlStr := req.URL.String()
	host := req.URL.Host
	if host == "" {
		host = req.Host
	}
	path := req.URL.Path

	// 1. Поиск в пользовательских зарегистрированных фикстурах под блокировкой чтения (RLock)
	var (
		matchedHandler     func(req *http.Request) (*http.Response, error)
		matchedStatusCode  int
		matchedContentType string
		matchedBody        []byte
		foundFixture       bool
	)

	m.mu.RLock()
	for _, f := range m.fixtures {
		if matchesPattern(req, urlStr, host, path, f.Pattern) {
			foundFixture = true
			if f.Handler != nil {
				matchedHandler = f.Handler
			} else {
				matchedStatusCode = f.StatusCode
				matchedContentType = f.ContentType
				matchedBody = f.Body
			}
			break
		}
	}
	pt := m.passthroughTransport
	m.mu.RUnlock()

	// Исполнение кастомного обработчика или генерация ответа ВНЕ критической секции.
	// Предотвращает deadlock при вызове RegisterFixture/Reset изнутри обработчика.
	if foundFixture {
		if matchedHandler != nil {
			return matchedHandler(req)
		}
		return makeResponse(req, matchedStatusCode, matchedContentType, matchedBody), nil
	}

	// 2. Локальный трафик (127.0.0.0/8, localhost, ::1) перенаправляем на реальный loopback транспорт
	if isLoopbackHost(host) {
		if pt != nil {
			return pt.RoundTrip(req)
		}
		return http.DefaultTransport.RoundTrip(req)
	}

	// 3. Fallback-ответы для известных внешних каталогов и 501-блокировка немоканных запросов
	return m.defaultFallbackResponse(req)
}

func matchesPattern(req *http.Request, urlStr, host, path, pattern string) bool {
	if pattern == "" {
		return false
	}
	// Точное совпадение URL или Host или Path
	if urlStr == pattern || host == pattern || path == pattern {
		return true
	}
	// Префиксное совпадение
	if strings.HasPrefix(urlStr, pattern) || strings.HasPrefix(path, pattern) {
		return true
	}
	// Подстрочное совпадение
	if strings.Contains(urlStr, pattern) {
		return true
	}
	return false
}

func isLoopbackHost(hostPort string) bool {
	hostname := strings.TrimSpace(hostPort)
	if hostname == "" {
		return false
	}

	// 1. Отделяем порт, если он указан (например, "localhost:8080", "[::1]:9090", "127.0.0.1:3000")
	if h, _, err := net.SplitHostPort(hostname); err == nil {
		hostname = h
	}

	// 2. Убираем квадратные скобки IPv6 (например, "[::1]") и приводим к нижнему регистру (RFC 7230 §2.7.3)
	hostname = strings.ToLower(strings.Trim(hostname, "[]"))

	// 3. Удаляем IPv6 zone ID / scope (%eth0, %1), если он присутствует
	if idx := strings.IndexByte(hostname, '%'); idx != -1 {
		hostname = hostname[:idx]
	}

	// 4. Удаляем замыкающую точку FQDN (например, "localhost.")
	hostname = strings.TrimSuffix(hostname, ".")

	// 5. Проверяем символическое имя localhost (без учета регистра)
	if hostname == "localhost" {
		return true
	}

	// 6. Проверяем IP-адрес на принадлежность loopback-диапазону (127.0.0.0/8 или ::1)
	if ip := net.ParseIP(hostname); ip != nil && ip.IsLoopback() {
		return true
	}

	return false
}

func (m *MockTransport) defaultFallbackResponse(req *http.Request) (*http.Response, error) {
	host := req.URL.Host
	if host == "" {
		host = req.Host
	}
	host = strings.ToLower(host)
	path := req.URL.Path

	// Deezer API
	if strings.Contains(host, "deezer.com") || strings.Contains(host, "verymad.net") {
		if strings.Contains(path, "/search") || strings.Contains(path, "/chart") {
			return makeResponse(req, http.StatusOK, "application/json", []byte(`{"data":[],"total":0}`)), nil
		}
		return makeResponse(req, http.StatusOK, "application/json", []byte(`{"data":[]}`)), nil
	}

	// Apple / iTunes Search API
	if strings.Contains(host, "apple.com") {
		return makeResponse(req, http.StatusOK, "application/json", []byte(`{"resultCount":0,"results":[]}`)), nil
	}

	// SoundCloud API
	if strings.Contains(host, "soundcloud.com") {
		return makeResponse(req, http.StatusOK, "application/json", []byte(`{"collection":[],"total_results":0}`)), nil
	}

	// MusicBrainz API
	if strings.Contains(host, "musicbrainz.org") {
		return makeResponse(req, http.StatusOK, "application/json", []byte(`{"releases":[],"count":0,"release-groups":[],"recordings":[]}`)), nil
	}

	// Cover Art Archive
	if strings.Contains(host, "coverartarchive.org") {
		return makeResponse(req, http.StatusNotFound, "application/json", []byte(`{"error":"Cover art not found"}`)), nil
	}

	// LRCLIB API
	if strings.Contains(host, "lrclib.net") {
		if strings.Contains(path, "/api/get") {
			return makeResponse(req, http.StatusNotFound, "application/json", []byte(`{"error":"not_found"}`)), nil
		}
		return makeResponse(req, http.StatusOK, "application/json", []byte(`[]`)), nil
	}

	// Любой сторонний внешний ресурс блокируется (защита AGENTS.md от live сетевых запросов)
	errPayload := fmt.Sprintf(`{"error":"unmocked external request in testkit","host":"%s","path":"%s"}`, host, path)
	return makeResponse(req, http.StatusNotImplemented, "application/json", []byte(errPayload)), nil
}

func makeResponse(req *http.Request, statusCode int, contentType string, body []byte) *http.Response {
	if contentType == "" {
		contentType = "application/json"
	}
	header := make(http.Header)
	header.Set("Content-Type", contentType)
	header.Set("Content-Length", strconv.Itoa(len(body)))

	return &http.Response{
		Status:        fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		StatusCode:    statusCode,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}
