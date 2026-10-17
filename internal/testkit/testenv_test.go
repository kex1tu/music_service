package testkit

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"music-manager/internal/core/auth"
	"music-manager/internal/id3"
)

// TestTestEnv_LifecycleAndComponents проверяет создание изолированного окружения:
// инициализацию SQLite, накатывание schema.sql, создание песочниц и работу t.Cleanup.
func TestTestEnv_LifecycleAndComponents(t *testing.T) {
	env := NewEnv(t)

	if env.DB == nil {
		t.Fatal("expected non-nil DB")
	}
	if env.MusicSandbox == nil {
		t.Fatal("expected non-nil MusicSandbox")
	}
	if env.TmpSandbox == nil {
		t.Fatal("expected non-nil TmpSandbox")
	}
	if env.MockHTTP == nil {
		t.Fatal("expected non-nil MockHTTP")
	}
	if env.Client == nil {
		t.Fatal("expected non-nil Client")
	}

	// Проверяем, что таблицы схемы schema.sql созданы
	var tableCount int
	err := env.DB.Conn().QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('tracks', 'albums', 'download_jobs')").Scan(&tableCount)
	if err != nil {
		t.Fatalf("failed to query sqlite_master: %v", err)
	}
	if tableCount != 3 {
		t.Errorf("expected 3 verified core tables, got %d", tableCount)
	}

	// Проверяем существование физических директорий песочниц
	if st, err := os.Stat(env.MusicSandbox.Root()); err != nil || !st.IsDir() {
		t.Fatalf("music sandbox root is not a valid directory: %v", err)
	}
	if st, err := os.Stat(env.TmpSandbox.Root()); err != nil || !st.IsDir() {
		t.Fatalf("tmp sandbox root is not a valid directory: %v", err)
	}
}

// TestMockTransport_RegisteredFixtures проверяет регистрацию и вызов кастомных фикстур.
func TestMockTransport_RegisteredFixtures(t *testing.T) {
	env := NewEnv(t)

	pattern := "https://api.deezer.com/album/42"
	mockBody := `{"id":42,"title":"Hitchhiker"}`
	env.MockHTTP.RegisterFixture(pattern, http.StatusOK, "application/json", []byte(mockBody))

	req, _ := http.NewRequest(http.MethodGet, pattern, nil)
	resp, err := env.Client.Do(req)
	if err != nil {
		t.Fatalf("client.Do failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != mockBody {
		t.Errorf("expected body %s, got %s", mockBody, string(body))
	}
}

// TestMockTransport_RoutingAndFallbacks проверяет маршрутизацию внешних запросов,
// дефолтные fallback-ответы для Deezer, iTunes, SoundCloud, MusicBrainz, LRCLIB и блокировку немоканных запросов.
func TestMockTransport_RoutingAndFallbacks(t *testing.T) {
	mock := NewMockTransport()
	client := &http.Client{Transport: mock}

	tests := []struct {
		url          string
		expectedCode int
		expectedBody string
	}{
		{"https://api.deezer.com/search?q=queen", http.StatusOK, `{"data":[],"total":0}`},
		{"https://itunes.apple.com/search?term=queen", http.StatusOK, `{"resultCount":0,"results":[]}`},
		{"https://api-v2.soundcloud.com/search?q=queen", http.StatusOK, `{"collection":[],"total_results":0}`},
		{"https://musicbrainz.org/ws/2/release", http.StatusOK, `{"releases":[],"count":0,"release-groups":[],"recordings":[]}`},
		{"https://coverartarchive.org/release/123", http.StatusNotFound, `{"error":"Cover art not found"}`},
		{"https://lrclib.net/api/get?track=queen", http.StatusNotFound, `{"error":"not_found"}`},
		{"https://lrclib.net/api/search?q=queen", http.StatusOK, `[]`},
		{"https://unknown-external-host.com/data", http.StatusNotImplemented, `{"error":"unmocked external request in testkit"`},
	}

	for _, tt := range tests {
		req, _ := http.NewRequest(http.MethodGet, tt.url, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request to %s failed: %v", tt.url, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != tt.expectedCode {
			t.Errorf("url %s: expected status %d, got %d", tt.url, tt.expectedCode, resp.StatusCode)
		}
		if !strings.Contains(string(body), tt.expectedBody) {
			t.Errorf("url %s: expected body containing %s, got %s", tt.url, tt.expectedBody, string(body))
		}
	}

	// Регистрация кастомного ответа через RegisterJSON
	type TrackFixture struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
	}
	mock.RegisterJSON("https://api.deezer.com/track/123", http.StatusOK, TrackFixture{
		ID:    123,
		Title: "Bohemian Rhapsody",
	})

	customResp, err := client.Get("https://api.deezer.com/track/123")
	if err != nil {
		t.Fatalf("custom fixture request failed: %v", err)
	}
	defer customResp.Body.Close()
	var fixture TrackFixture
	if err := json.NewDecoder(customResp.Body).Decode(&fixture); err != nil {
		t.Fatalf("failed to decode registered fixture: %v", err)
	}
	if fixture.Title != "Bohemian Rhapsody" || fixture.ID != 123 {
		t.Errorf("unexpected fixture data: %+v", fixture)
	}

	// Регистрация кастомного обработчика через RegisterHandler
	mock.RegisterHandler("https://api.deezer.com/dynamic", func(req *http.Request) (*http.Response, error) {
		return makeResponse(req, http.StatusAccepted, "application/json", []byte(`{"status":"dynamic"}`)), nil
	})
	dynResp, err := client.Get("https://api.deezer.com/dynamic")
	if err != nil {
		t.Fatalf("dynamic handler request failed: %v", err)
	}
	defer dynResp.Body.Close()
	if dynResp.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202, got %d", dynResp.StatusCode)
	}

	// Reset
	mock.Reset()

	// Тестирование потокобезопасности при параллельных запросах
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r, err := client.Get("https://api.deezer.com/search?q=test")
			if err == nil {
				_ = r.Body.Close()
			}
		}(i)
	}
	wg.Wait()
}

// TestCreateDummyMP3_ValidFileAndTags проверяет синтез MP3, корректность структуры кадров
// MPEG-1 Layer 3 (0xFF 0xFB), правильность тегов ID3v2.4 и права 0644.
func TestCreateDummyMP3_ValidFileAndTags(t *testing.T) {
	env := NewEnv(t)

	relPath := "Rock/Queen/01. Bohemian Rhapsody.mp3"
	absPath := env.CreateDummyMP3(relPath, "Bohemian Rhapsody", "Queen", "A Night at the Opera")

	// 1. Проверяем существование файла
	st, err := os.Stat(absPath)
	if err != nil {
		t.Fatalf("created MP3 does not exist: %v", err)
	}

	// 2. Проверяем права 0644
	if perm := st.Mode().Perm(); perm != 0644 {
		t.Errorf("expected file mode 0644, got %#o", perm)
	}

	// 3. Проверяем наличие синхрослова MPEG-1 Layer 3 (0xFF 0xFB)
	fileBytes, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(fileBytes) < 100 {
		t.Fatalf("file is suspiciously small: %d bytes", len(fileBytes))
	}

	// Первые 3 байта файла должны быть ID3v2.4 заголовок
	if string(fileBytes[0:3]) != "ID3" {
		t.Errorf("file does not start with ID3 header: %v", fileBytes[0:3])
	}

	// Ищем синхрослово 0xFF 0xFB в теле аудиопотока
	foundSync := false
	for i := 10; i+1 < len(fileBytes); i++ {
		if fileBytes[i] == 0xFF && (fileBytes[i+1]&0xFE) == 0xFA { // 0xFF 0xFA or 0xFB
			foundSync = true
			break
		}
	}
	if !foundSync {
		t.Error("MPEG-1 Layer 3 sync word (0xFF 0xFB) was not found in synthesized MP3")
	}

	// 4. Считываем метаданные через внутренний парсер id3.ReadTags
	tags, err := id3.ReadTags(absPath, relPath)
	if err != nil {
		t.Fatalf("id3.ReadTags failed on dummy MP3: %v", err)
	}

	if tags.Title != "Bohemian Rhapsody" {
		t.Errorf("expected title 'Bohemian Rhapsody', got %q", tags.Title)
	}
	if tags.Artist != "Queen" {
		t.Errorf("expected artist 'Queen', got %q", tags.Artist)
	}
	if tags.Album != "A Night at the Opera" {
		t.Errorf("expected album 'A Night at the Opera', got %q", tags.Album)
	}
	if tags.Bitrate != 320 {
		t.Errorf("expected bitrate 320, got %d", tags.Bitrate)
	}
}

// TestCreateDummyMP3_PathTraversalProtection проверяет, что попытка записи за пределы
// песочницы жестко пресекается (инвариант AGENTS.md).
func TestCreateDummyMP3_PathTraversalProtection(t *testing.T) {
	env := NewEnv(t)

	dangerousPaths := []string{
		"../../etc/passwd.mp3",
		"sub/../../../../secret.mp3",
		"normal/../../escape.mp3",
		"%2e%2e/%2e%2e/secret.mp3",
		"test\x00.mp3",
	}

	for _, p := range dangerousPaths {
		_, err := CreateDummyMP3(env.MusicSandbox, p, "Exploit", "Attacker", "PWN")
		if err == nil {
			t.Errorf("expected error for path traversal %q, got nil", p)
			continue
		}
		if !strings.Contains(err.Error(), "testkit: invalid relPath") {
			t.Errorf("expected sandbox rejection error, got: %v", err)
		}
	}

	// Проверка передачи nil песочницы
	_, err := CreateDummyMP3(nil, "track.mp3", "T", "A", "Alb")
	if err == nil {
		t.Error("expected error for nil sandbox, got nil")
	}
}

// TestCreateDummyMP3WithTags_CoverAndMultiValues проверяет создание MP3 с обложкой и
// множественными значениями артистов (ID3v2.4 null-separated).
func TestCreateDummyMP3WithTags_CoverAndMultiValues(t *testing.T) {
	env := NewEnv(t)

	// Минимальный JPEG заголовок для фикстуры обложки
	fakeJPEG := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xFF, 0xD9}
	cover := &id3.CoverInfo{
		MimeType: "image/jpeg",
		Data:     fakeJPEG,
	}

	tags := id3.TrackTags{
		Title:       "Under Pressure",
		Artist:      "Queen\\David Bowie", // Двойной слэш фронтенда
		Album:       "Hot Space",
		TrackNumber: 11,
		TrackTotal:  11,
		Genre:       "Rock\\Classic Rock",
		Year:        1981,
	}

	relPath := "Queen/Hot Space/11. Under Pressure.mp3"
	absPath := env.CreateDummyMP3WithTags(relPath, tags, cover)

	readTags, err := id3.ReadTags(absPath, relPath)
	if err != nil {
		t.Fatalf("failed to read tags: %v", err)
	}

	if readTags.Title != "Under Pressure" {
		t.Errorf("unexpected title: %s", readTags.Title)
	}
	// Движок парсера id3 возвращает null-separated или объединенные теги
	if !strings.Contains(readTags.Artist, "Queen") || !strings.Contains(readTags.Artist, "David Bowie") {
		t.Errorf("artists not preserved properly: %s", readTags.Artist)
	}

	// Проверяем считывание обложки
	readCover, err := id3.ReadCover(absPath)
	if err != nil {
		t.Fatalf("failed to read cover: %v", err)
	}
	if readCover == nil || len(readCover.Data) == 0 {
		t.Error("expected valid cover in dummy MP3, got nil")
	}
}

func TestTestEnv_HTTPHelpersAndAuth(t *testing.T) {
	env := NewEnv(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.CookieName)
		if err != nil || cookie.Value == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "created", "name": body["name"]})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	env.Mount(handler)

	// 1. Без авторизации -> 401
	unauthResp := env.Get("/test")
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", unauthResp.StatusCode)
	}

	// 2. С авторизацией -> 200
	env.SetAuthUser("admin")
	authResp := env.Get("/test")
	if authResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", authResp.StatusCode)
	}

	// 3. POST JSON
	postResp := env.PostJSON("/test", map[string]string{"name": "testkit"})
	if postResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for post, got %d", postResp.StatusCode)
	}
	var res map[string]string
	env.DecodeJSON(postResp, &res)
	if res["name"] != "testkit" || res["status"] != "created" {
		t.Errorf("unexpected response: %+v", res)
	}

	// 4. ReadBody
	getResp := env.Get("/test")
	bodyBytes := env.ReadBody(getResp)
	if !strings.Contains(string(bodyBytes), `"status":"ok"`) {
		t.Errorf("unexpected body from ReadBody: %s", string(bodyBytes))
	}

	// 5. ClearAuth -> снова 401
	env.ClearAuth()
	clearedResp := env.Get("/test")
	if clearedResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 after ClearAuth, got %d", clearedResp.StatusCode)
	}

	// 6. SetHandler (без Server)
	env.Close()
	env.SetHandler(handler)
	env.SetAuthUser("tester")
	inMemoryResp := env.Get("/in-memory")
	if inMemoryResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from in-memory handler, got %d", inMemoryResp.StatusCode)
	}
}

func TestTestEnv_CreateDummyFile(t *testing.T) {
	env := NewEnv(t)

	lrcContent := []byte("[00:10.00] Bohemian Rhapsody\n[00:15.00] Is this the real life?")
	lrcPath := env.CreateDummyFile(env.MusicSandbox, "Queen/Bohemian.lrc", lrcContent)

	readData, err := os.ReadFile(lrcPath)
	if err != nil {
		t.Fatalf("failed to read created dummy file: %v", err)
	}
	if string(readData) != string(lrcContent) {
		t.Errorf("dummy file content mismatch: expected %q, got %q", string(lrcContent), string(readData))
	}
}

func TestAssertGoldenJSON_Success(t *testing.T) {
	tempDir := t.TempDir()
	goldenPath := filepath.Join(tempDir, "sample.golden.json")

	// Записываем эталон с форматированием в 2 пробела
	expectedContent := []byte("{\n  \"album\": \"A Night at the Opera\",\n  \"artist\": \"Queen\",\n  \"tracks_count\": 12\n}\n")
	if err := os.WriteFile(goldenPath, expectedContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Фактический JSON: другие пробелы и противоположный порядок ключей
	actualPayload := []byte(`{"tracks_count":12, "artist":"Queen", "album":"A Night at the Opera"}`)

	// Сравнение обязано пройти без ошибок
	AssertGoldenJSON(t, actualPayload, goldenPath)

	// Также проверяем вызов через env
	env := NewEnv(t)
	env.AssertGoldenJSON(actualPayload, goldenPath)
}

func TestAssertGoldenJSON_UpdateGolden(t *testing.T) {
	tempDir := t.TempDir()
	goldenPath := filepath.Join(tempDir, "subfolder", "new_feature.golden.json")

	t.Setenv("UPDATE_GOLDEN", "true")

	actualPayload := []byte(`{"status":"success","code":200}`)

	// Файл отсутствует, но UPDATE_GOLDEN=true должен создать его и директорию
	AssertGoldenJSON(t, actualPayload, goldenPath)

	savedData, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("failed to read generated golden file: %v", err)
	}

	expectedNormalized := "{\n  \"code\": 200,\n  \"status\": \"success\"\n}\n"
	if string(savedData) != expectedNormalized {
		t.Errorf("golden file content mismatch:\nWANT:\n%s\nGOT:\n%s", expectedNormalized, string(savedData))
	}
}

func TestAssertGoldenJSON_DiffGeneration(t *testing.T) {
	want := []byte("{\n  \"artist\": \"Queen\",\n  \"status\": \"pending\"\n}\n")
	got := []byte("{\n  \"artist\": \"Queen\",\n  \"status\": \"completed\"\n}\n")

	diff := formatJSONDiff("test.golden.json", want, got)

	if !strings.Contains(diff, "@@ line 3 @@") {
		t.Errorf("expected line number in diff, got: %s", diff)
	}
	if !strings.Contains(diff, "-   \"status\": \"pending\"") {
		t.Errorf("expected removed line indicator, got: %s", diff)
	}
	if !strings.Contains(diff, "+   \"status\": \"completed\"") {
		t.Errorf("expected added line indicator, got: %s", diff)
	}
}

func TestMockTransport_PatternMatchingVariants(t *testing.T) {
	mock := NewMockTransport(&http.Transport{})

	// 1. Path match
	mock.RegisterFixture("/api/v1/ping", http.StatusOK, "text/plain", []byte("pong"))
	// 2. Substring match
	mock.RegisterFixture("substring-token", http.StatusOK, "application/json", []byte(`{"matched":"substring"}`))
	// 3. Overwriting existing fixture
	mock.RegisterFixture("/api/v1/ping", http.StatusAccepted, "text/plain", []byte("pong2"))

	client := &http.Client{Transport: mock}

	req1, _ := http.NewRequest(http.MethodGet, "http://external.com/api/v1/ping", nil)
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202 after overwrite, got %d", resp1.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodGet, "http://external.com/items?query=substring-token", nil)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for substring match, got %d", resp2.StatusCode)
	}

	// Empty pattern does not match
	req3, _ := http.NewRequest(http.MethodGet, "http://external.com/none", nil)
	if matchesPattern(req3, "http://external.com/none", "external.com", "/none", "") {
		t.Error("expected false for empty pattern")
	}
}

func TestTestEnv_BodyTypesAndMount(t *testing.T) {
	env := NewEnv(t)

	// String body
	reqStr := env.NewRequest(http.MethodPost, "/str", "raw string body")
	if reqStr.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("unexpected content type for string: %s", reqStr.Header.Get("Content-Type"))
	}

	// Bytes body
	reqBytes := env.NewRequest(http.MethodPost, "/bytes", []byte("raw bytes"))
	if reqBytes.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("unexpected content type for bytes: %s", reqBytes.Header.Get("Content-Type"))
	}

	// Reader body
	reqReader := env.NewRequest(http.MethodPost, "/reader", strings.NewReader("reader data"))
	if reqReader.Body == nil {
		t.Error("expected body reader")
	}

	// Remount server
	h1 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h2 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	env.Mount(h1)
	resp1 := env.Get("/")
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from h1, got %d", resp1.StatusCode)
	}
	env.Mount(h2)
	resp2 := env.Get("/")
	if resp2.StatusCode != http.StatusCreated {
		t.Errorf("expected 201 from h2, got %d", resp2.StatusCode)
	}
}

func TestAssertGoldenJSON_ExistingUpdate(t *testing.T) {
	tempDir := t.TempDir()
	goldenPath := filepath.Join(tempDir, "existing.golden.json")

	initial := []byte(`{"version":1}`)
	AssertGoldenJSON(t, initial, goldenPath)

	// Теперь обновляем файл с UPDATE_GOLDEN=1
	t.Setenv("UPDATE_GOLDEN", "1")
	updated := []byte(`{"version":2}`)
	AssertGoldenJSON(t, updated, goldenPath)

	data, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": 2`) {
		t.Errorf("expected version 2 in updated golden, got: %s", string(data))
	}
}

func TestGenerateMinimalMPEG1Layer3Payload_Zero(t *testing.T) {
	payload := generateMinimalMPEG1Layer3Payload(0)
	if len(payload) != MPEG1Layer3FrameSize {
		t.Errorf("expected 1 frame (%d bytes) for 0 input, got %d", MPEG1Layer3FrameSize, len(payload))
	}
}

func TestIsLoopbackHost_ComprehensiveMatrix(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected bool
	}{
		// --- 1. Valid IPv4 Loopback Addresses (127.0.0.0/8) ---
		{"IPv4 standard loopback", "127.0.0.1", true},
		{"IPv4 standard loopback with port", "127.0.0.1:8080", true},
		{"IPv4 alternate loopback", "127.0.0.2", true},
		{"IPv4 alternate loopback with port", "127.0.0.2:9000", true},
		{"IPv4 internal subnet address", "127.1.2.3:3000", true},
		{"IPv4 high loopback boundary", "127.255.255.254:443", true},
		{"IPv4 broadcast boundary", "127.255.255.255", true},

		// --- 2. Valid Named Localhost & Case Variants (RFC 7230 §5.4) ---
		{"Named localhost lowercase", "localhost", true},
		{"Named localhost with port", "localhost:8080", true},
		{"Named LOCALHOST uppercase", "LOCALHOST", true},
		{"Named LOCALHOST uppercase with port", "LOCALHOST:8080", true},
		{"Named LocalHost mixed case", "LocalHost", true},
		{"Named LocalHost mixed case with port", "LocalHost:3000", true},
		{"Named loCALhoST mixed case", "loCALhoST", true},
		{"Named localhost with trailing dot (FQDN)", "localhost.", true},
		{"Named localhost with trailing dot and port", "localhost.:8080", true},
		{"Named LOCALHOST with trailing dot (FQDN)", "LOCALHOST.", true},
		{"Named LOCALHOST with trailing dot and port", "LOCALHOST.:8080", true},
		{"Named whitespace padded", "  LOCALHOST:8080  ", true},

		// --- 3. Valid IPv6 Loopback Addresses (::1) ---
		{"IPv6 loopback bare", "::1", true},
		{"IPv6 loopback in brackets", "[::1]", true},
		{"IPv6 loopback in brackets with port", "[::1]:8080", true},
		{"IPv6 loopback in brackets with TLS port", "[::1]:443", true},
		{"IPv6 loopback with zone id", "::1%lo0", true},
		{"IPv6 loopback with zone id in brackets and port", "[::1%lo0]:8080", true},
		{"IPv6 full form bare", "0:0:0:0:0:0:0:1", true},
		{"IPv6 full form in brackets", "[0:0:0:0:0:0:0:1]", true},
		{"IPv6 full form in brackets with port", "[0:0:0:0:0:0:0:1]:8080", true},
		{"IPv6 IPv4-mapped loopback", "::ffff:127.0.0.1", true},
		{"IPv6 IPv4-mapped loopback in brackets with port", "[::ffff:127.0.0.1]:8080", true},
		{"IPv6 IPv4-mapped loopback uppercase hex", "[::FFFF:127.0.0.1]:8080", true},

		// --- 4. Adversarial Attack Vectors (Domain Spoofing / DNS Leak Attempts) - MUST BE FALSE ---
		{"Attacker domain starting with 127.", "127.attacker.com", false},
		{"Attacker domain with port", "127.attacker.com:80", false},
		{"Attacker domain with TLS port", "127.attacker.com:443", false},
		{"NIP.io wildcard DNS resolving to 127.0.0.1", "127.0.0.1.nip.io", false},
		{"NIP.io wildcard with port", "127.0.0.1.nip.io:8080", false},
		{"XIP.io wildcard", "127.0.0.1.xip.io", false},
		{"SSLIP.io wildcard", "127.0.0.1.sslip.io", false},
		{"Hyphenated 127 domain", "127-site.org", false},
		{"Hyphenated 127 domain with port", "127-site.org:8080", false},
		{"Underscore 127 domain", "127_evil.net", false},
		{"Subdomain containing 127.0.0.1", "127.0.0.1.evil.com", false},
		{"Trailing domain spoofing", "127001.com", false},
		{"Domain with suffix 127", "attacker127.com", false},
		{"Fake localhost subdomain", "fake.localhost.com", false},
		{"Localhost as subdomain of attacker", "localhost.evil.com", false},
		{"Localhost attacker with port", "localhost.attacker.com:8080", false},
		{"Prefix resembling localhost", "notlocalhost", false},
		{"Suffix resembling localhost", "localhostsuffix", false},
		{"Hyphenated fake localhost", "fake-localhost:8080", false},
		{"Public domain", "example.com:443", false},
		{"Search engine domain", "google.com", false},

		// --- 5. Non-Loopback Private & Public IP Addresses - MUST BE FALSE ---
		{"Private IPv4 Class C gateway", "192.168.1.1", false},
		{"Private IPv4 Class C with port", "192.168.1.1:8080", false},
		{"Private IPv4 Class A", "10.0.0.1", false},
		{"Private IPv4 Class A with port", "10.0.0.1:8080", false},
		{"Private IPv4 Class B", "172.16.0.1", false},
		{"Cloud metadata service IP (SSRF vector)", "169.254.169.254", false},
		{"Unspecified IPv4 (INADDR_ANY)", "0.0.0.0", false},
		{"Unspecified IPv4 with port", "0.0.0.0:8080", false},
		{"Broadcast IPv4", "255.255.255.255", false},
		{"Public DNS IP", "8.8.8.8", false},
		{"IPv6 link-local in brackets with port", "[fe80::1]:80", false},
		{"IPv6 documentation address", "[2001:db8::1]:80", false},
		{"IPv6 unspecified in brackets with port", "[::]:8080", false},
		{"IPv6 non-loopback bare", "::2", false},
		{"IPv6 non-loopback in brackets with port", "[::2]:8080", false},

		// --- 6. Malformed & Boundary Inputs - MUST BE FALSE ---
		{"Empty string", "", false},
		{"Whitespace only", "   ", false},
		{"Port only with no host", ":8080", false},
		{"Invalid IP syntax", "127.0.0.999", false},
		{"Malformed brackets", "[invalid]:8080", false},
		{"Invalid colons", ":::", false},
		{"Octal format attempt", "0177.0.0.1", false},
		{"Hex format attempt", "0x7f.0.0.1", false},
		{"Integer representation", "2130706433", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := isLoopbackHost(tc.input)
			if got != tc.expected {
				t.Errorf("isLoopbackHost(%q) = %v; want %v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestMockTransport_LoopbackRoutingIntegration(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo-Host", r.Host)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	port := ts.Listener.Addr().String()
	if colonIdx := strings.LastIndex(port, ":"); colonIdx != -1 {
		port = port[colonIdx:]
	}

	mock := NewMockTransport()
	client := &http.Client{Transport: mock}

	cases := []struct {
		name       string
		url        string
		wantStatus int
	}{
		{"localhost lowercase", "http://localhost" + port + "/ping", http.StatusOK},
		{"LOCALHOST uppercase", "http://LOCALHOST" + port + "/ping", http.StatusOK},
		{"LocalHost mixed-case", "http://LocalHost" + port + "/ping", http.StatusOK},
		{"127.0.0.1 standard", "http://127.0.0.1" + port + "/ping", http.StatusOK},
		{"attacker 127 domain blocked", "http://127.attacker.com/leak", http.StatusNotImplemented},
		{"attacker localhost domain blocked", "http://localhost.attacker.com/leak", http.StatusNotImplemented},
		{"attacker nip.io domain blocked", "http://127.0.0.1.nip.io/tunnel", http.StatusNotImplemented},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tc.url, nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("client.Do failed for %s: %v", tc.url, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("url %s: got status %d, want %d", tc.url, resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

func TestMockTransport_Handler_RegisterJSON_NoDeadlock(t *testing.T) {
	mock := NewMockTransport()
	client := &http.Client{Transport: mock}

	mock.RegisterHandler("https://api.test.com/phase1", func(req *http.Request) (*http.Response, error) {
		// Calling RegisterJSON while servicing phase1
		mock.RegisterJSON("https://api.test.com/phase2", http.StatusCreated, map[string]string{
			"status": "created_by_phase1",
		})
		return makeResponse(req, http.StatusOK, "application/json", []byte(`{"status":"phase1_done"}`)), nil
	})

	done := make(chan bool, 1)
	go func() {
		// 1. Trigger phase 1
		resp1, err := client.Get("https://api.test.com/phase1")
		if err != nil {
			t.Errorf("phase 1 request failed: %v", err)
			return
		}
		defer resp1.Body.Close()
		if resp1.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp1.StatusCode)
		}

		// 2. Trigger dynamically registered phase 2
		resp2, err := client.Get("https://api.test.com/phase2")
		if err != nil {
			t.Errorf("phase 2 request failed: %v", err)
			return
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusCreated {
			t.Errorf("expected 201, got %d", resp2.StatusCode)
		}

		body, _ := io.ReadAll(resp2.Body)
		if !strings.Contains(string(body), "created_by_phase1") {
			t.Errorf("unexpected body: %s", string(body))
		}

		done <- true
	}()

	select {
	case <-done:
		// Passed successfully
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK DETECTED: Handler execution deadlocked when calling RegisterJSON")
	}
}

func TestMockTransport_Handler_Reset_NoDeadlock(t *testing.T) {
	mock := NewMockTransport()
	client := &http.Client{Transport: mock}

	mock.RegisterJSON("https://api.test.com/target", http.StatusOK, map[string]string{"foo": "bar"})

	mock.RegisterHandler("https://api.test.com/reset-trigger", func(req *http.Request) (*http.Response, error) {
		mock.Reset() // Wipes all registered fixtures
		return makeResponse(req, http.StatusOK, "application/json", []byte(`{"reset":true}`)), nil
	})

	done := make(chan bool, 1)
	go func() {
		// 1. Call reset trigger
		resp1, err := client.Get("https://api.test.com/reset-trigger")
		if err != nil {
			t.Errorf("reset trigger failed: %v", err)
			return
		}
		defer resp1.Body.Close()
		if resp1.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp1.StatusCode)
		}

		// 2. Target should now fall through to 501 Not Implemented because Reset() cleared fixtures
		resp2, err := client.Get("https://api.test.com/target")
		if err != nil {
			t.Errorf("target request failed: %v", err)
			return
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusNotImplemented {
			t.Errorf("expected 501 for unmocked target after Reset, got %d", resp2.StatusCode)
		}

		done <- true
	}()

	select {
	case <-done:
		// Passed successfully
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK DETECTED: Handler execution deadlocked when calling Reset")
	}
}

func TestMockTransport_Handler_ChainedCalls_NoDeadlock(t *testing.T) {
	mock := NewMockTransport()
	client := &http.Client{Transport: mock}

	mock.RegisterJSON("https://api.test.com/step2", http.StatusOK, map[string]string{"result": "step2_ok"})

	mock.RegisterHandler("https://api.test.com/step1", func(req *http.Request) (*http.Response, error) {
		// Nested HTTP call using same client and mock transport
		subResp, err := client.Get("https://api.test.com/step2")
		if err != nil {
			return nil, err
		}
		defer subResp.Body.Close()
		subBody, _ := io.ReadAll(subResp.Body)

		return makeResponse(req, http.StatusOK, "application/json", subBody), nil
	})

	// Concurrent writer continuously mutating fixtures
	stopWriter := make(chan struct{})
	go func() {
		i := 0
		for {
			select {
			case <-stopWriter:
				return
			default:
				mock.RegisterJSON(fmt.Sprintf("https://api.test.com/dummy/%d", i), http.StatusOK, map[string]int{"n": i})
				i++
			}
		}
	}()

	done := make(chan bool, 1)
	go func() {
		for i := 0; i < 20; i++ {
			resp, err := client.Get("https://api.test.com/step1")
			if err != nil {
				t.Errorf("step1 request %d failed: %v", i, err)
				break
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !strings.Contains(string(body), "step2_ok") {
				t.Errorf("unexpected body on iteration %d: %s", i, string(body))
			}
		}
		close(stopWriter)
		done <- true
	}()

	select {
	case <-done:
		// Passed successfully
	case <-time.After(3 * time.Second):
		close(stopWriter)
		t.Fatal("DEADLOCK DETECTED: Nested RoundTrip blocked by concurrent writer")
	}
}

func TestMockTransport_HighConcurrency_RaceFree(t *testing.T) {
	mock := NewMockTransport()
	client := &http.Client{Transport: mock}

	mock.RegisterHandler("https://api.test.com/worker", func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query().Get("id")
		// Dynamically register and query
		mock.RegisterJSON(fmt.Sprintf("https://api.test.com/worker/%s", q), http.StatusOK, map[string]string{"id": q})
		return makeResponse(req, http.StatusOK, "application/json", []byte(fmt.Sprintf(`{"handled":"%s"}`, q))), nil
	})

	const numGoroutines = 15
	const requestsPerGoroutine = 40
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for r := 0; r < requestsPerGoroutine; r++ {
				url := fmt.Sprintf("https://api.test.com/worker?id=g%d_r%d", gid, r)
				resp, err := client.Get(url)
				if err != nil {
					t.Errorf("goroutine %d request %d failed: %v", gid, r, err)
					return
				}
				_ = resp.Body.Close()

				if r%10 == 0 {
					mock.Reset()
					mock.RegisterHandler("https://api.test.com/worker", func(req *http.Request) (*http.Response, error) {
						q := req.URL.Query().Get("id")
						return makeResponse(req, http.StatusOK, "application/json", []byte(fmt.Sprintf(`{"re-registered":true,"id":"%s"}`, q))), nil
					})
				}
			}
		}(g)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Passed cleanly
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK or TIMEOUT under concurrent mutation stress")
	}
}
