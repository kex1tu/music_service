package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"music-manager/internal/core/auth"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/id3"
)

var (
	defaultTransportMu sync.Mutex
)

// TestEnv предоставляет изолированное, легковесное тестовое окружение.
type TestEnv struct {
	T            *testing.T
	DB           *db.DB
	MusicSandbox *sandbox.PathSandbox
	TmpSandbox   *sandbox.PathSandbox
	MockHTTP     *MockTransport
	Client       *http.Client
	Server       *httptest.Server
	Handler      http.Handler
	TempDir      string
	MusicDir     string
	TmpDir       string
	SecretKey    string

	authCookie *http.Cookie
}

// NewEnv инициализирует изолированное тестовое окружение:
// - временная директория с music/ и tmp/ песочницами
// - SQLite база данных с накатом актуальной schema.sql
// - MockTransport RoundTripper для блокировки внешних запросов
// - авто-очистка через t.Cleanup.
func NewEnv(t *testing.T) *TestEnv {
	t.Helper()

	// Гарантируем изоляцию подпроцессов (yt-dlp, ffmpeg) во всех тестовых сценариях
	t.Setenv("MUSIC_MANAGER_TEST_MODE", "1")

	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music")
	tmpDir := filepath.Join(tempDir, "tmp")

	if err := os.MkdirAll(musicDir, 0755); err != nil {
		t.Fatalf("testkit: failed to create music dir: %v", err)
	}
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		t.Fatalf("testkit: failed to create tmp dir: %v", err)
	}

	musicSb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatalf("testkit: failed to initialize music sandbox: %v", err)
	}

	tmpSb, err := sandbox.NewPathSandbox(tmpDir)
	if err != nil {
		t.Fatalf("testkit: failed to initialize tmp sandbox: %v", err)
	}

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("testkit: failed to open test sqlite db: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
		// Absolute-path fixture consumers resolve these roots through weak references.
		runtime.KeepAlive(musicSb)
		runtime.KeepAlive(tmpSb)
	})

	mockTransport := NewMockTransport(nil)

	// Защищаем подмену DefaultTransport через мьютекс и восстанавливаем через t.Cleanup
	defaultTransportMu.Lock()
	savedTransport := http.DefaultTransport
	http.DefaultTransport = mockTransport
	t.Cleanup(func() {
		http.DefaultTransport = savedTransport
		defaultTransportMu.Unlock()
	})

	client := &http.Client{
		Transport: mockTransport,
	}

	secretKey := "testkit_super_secret_hmac_key_for_testing_purposes_only"

	env := &TestEnv{
		T:            t,
		DB:           database,
		MusicSandbox: musicSb,
		TmpSandbox:   tmpSb,
		MockHTTP:     mockTransport,
		Client:       client,
		TempDir:      tempDir,
		MusicDir:     musicDir,
		TmpDir:       tmpDir,
		SecretKey:    secretKey,
	}

	return env
}

// Close вручную освобождает занятые ресурсы (сервер, БД, транспорт).
func (env *TestEnv) Close() {
	if env.Server != nil {
		env.Server.Close()
		env.Server = nil
	}
	if env.DB != nil {
		_ = env.DB.Close()
	}
}

// Mount монтирует обработчик (например, Chi Router) и запускает httptest.Server.
func (env *TestEnv) Mount(h http.Handler) *TestEnv {
	env.T.Helper()
	env.Handler = h
	if env.Server != nil {
		env.Server.Close()
	}
	env.Server = httptest.NewServer(h)
	env.T.Cleanup(func() {
		if env.Server != nil {
			env.Server.Close()
		}
	})
	return env
}

// SetHandler задает обработчик для быстрого in-memory исполнения через ResponseRecorder без TCP.
func (env *TestEnv) SetHandler(h http.Handler) *TestEnv {
	env.Handler = h
	return env
}

// SetAuthUser генерирует авторизационную cookie для сессии пользователя.
func (env *TestEnv) SetAuthUser(login string) *TestEnv {
	token := auth.CreateSessionToken(login, env.SecretKey)
	env.authCookie = &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
	return env
}

// ClearAuth очищает авторизационную cookie для тестирования 401 Unauthorized.
func (env *TestEnv) ClearAuth() *TestEnv {
	env.authCookie = nil
	return env
}

// NewRequest создает *http.Request с контекстом и сериализацией тела.
func (env *TestEnv) NewRequest(method, targetPath string, body any) *http.Request {
	env.T.Helper()

	var bodyReader io.Reader
	var contentType string

	if body != nil {
		switch v := body.(type) {
		case io.Reader:
			bodyReader = v
		case []byte:
			bodyReader = bytes.NewReader(v)
			contentType = "application/octet-stream"
		case string:
			bodyReader = strings.NewReader(v)
			contentType = "text/plain; charset=utf-8"
		default:
			data, err := json.Marshal(body)
			if err != nil {
				env.T.Fatalf("testkit: failed to marshal request body to JSON: %v", err)
			}
			bodyReader = bytes.NewReader(data)
			contentType = "application/json"
		}
	}

	urlStr := targetPath
	if env.Server != nil && !strings.HasPrefix(targetPath, "http://") && !strings.HasPrefix(targetPath, "https://") {
		urlStr = env.Server.URL + "/" + strings.TrimPrefix(targetPath, "/")
	}

	req, err := http.NewRequestWithContext(context.Background(), method, urlStr, bodyReader)
	if err != nil {
		env.T.Fatalf("testkit: failed to create http request: %v", err)
	}

	if contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}

	if env.authCookie != nil {
		req.AddCookie(env.authCookie)
	}

	return req
}

// PostJSON отправляет POST-запрос с JSON-телом и возвращает *http.Response.
func (env *TestEnv) PostJSON(targetPath string, body any) *http.Response {
	env.T.Helper()
	req := env.NewRequest(http.MethodPost, targetPath, body)
	return env.Do(req)
}

// Get отправляет GET-запрос и возвращает *http.Response.
func (env *TestEnv) Get(targetPath string) *http.Response {
	env.T.Helper()
	req := env.NewRequest(http.MethodGet, targetPath, nil)
	return env.Do(req)
}

// Do выполняет HTTP-запрос через httptest.Server либо ResponseRecorder.
func (env *TestEnv) Do(req *http.Request) *http.Response {
	env.T.Helper()

	if env.Server != nil {
		resp, err := env.Client.Do(req)
		if err != nil {
			env.T.Fatalf("testkit: http request failed: %v", err)
		}
		return resp
	}

	if env.Handler != nil {
		rec := httptest.NewRecorder()
		env.Handler.ServeHTTP(rec, req)
		return rec.Result()
	}

	env.T.Fatalf("testkit: no handler or server mounted. Call env.Mount(h) or env.SetHandler(h)")
	return nil
}

// DecodeJSON декодирует JSON-ответ в структуру и закрывает Body.
func (env *TestEnv) DecodeJSON(resp *http.Response, target any) {
	env.T.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		env.T.Fatalf("testkit: failed to decode response JSON: %v", err)
	}
}

// ReadBody вычитывает все тело ответа, закрывает Body и возвращает байты.
func (env *TestEnv) ReadBody(resp *http.Response) []byte {
	env.T.Helper()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		env.T.Fatalf("testkit: failed to read response body: %v", err)
	}
	return data
}

// CreateDummyMP3 создает валидный MP3 файл с тегами ID3v2.4 внутри MusicSandbox.
func (env *TestEnv) CreateDummyMP3(relPath, title, artist, album string) string {
	env.T.Helper()
	absPath, err := CreateDummyMP3(env.MusicSandbox, relPath, title, artist, album)
	if err != nil {
		env.T.Fatalf("testkit: CreateDummyMP3 failed for %q: %v", relPath, err)
	}
	return absPath
}

// CreateDummyMP3WithTags создает MP3 файл с расширенными тегами внутри MusicSandbox.
func (env *TestEnv) CreateDummyMP3WithTags(relPath string, tags id3.TrackTags, cover *id3.CoverInfo) string {
	env.T.Helper()
	absPath, err := CreateDummyMP3WithTags(env.MusicSandbox, relPath, tags, cover)
	if err != nil {
		env.T.Fatalf("testkit: CreateDummyMP3WithTags failed for %q: %v", relPath, err)
	}
	return absPath
}

// CreateDummyFile создает произвольный файл внутри заданной песочницы.
func (env *TestEnv) CreateDummyFile(sb *sandbox.PathSandbox, relPath string, content []byte) string {
	env.T.Helper()
	if sb == nil {
		env.T.Fatalf("testkit: sandbox cannot be nil in CreateDummyFile")
	}
	absPath, err := sb.Resolve(relPath, false)
	if err != nil {
		env.T.Fatalf("testkit: invalid relPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		env.T.Fatalf("testkit: failed to create parent dir: %v", err)
	}
	if err := os.WriteFile(absPath, content, 0644); err != nil {
		env.T.Fatalf("testkit: failed to write file: %v", err)
	}
	return absPath
}

// AssertGoldenJSON делегирует проверку в тестовый хелпер AssertGoldenJSON.
func (env *TestEnv) AssertGoldenJSON(actual []byte, goldenPath string) {
	env.T.Helper()
	AssertGoldenJSON(env.T, actual, goldenPath)
}
