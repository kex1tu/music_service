package testkit

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
)

// TestStress_MockTransport_Concurrency выполняет высоконагруженное конкурентное тестирование
// MockTransport: параллельную регистрацию фикстур (RegisterFixture, RegisterJSON, RegisterHandler),
// параллельный сброс (Reset) и параллельный вызов RoundTrip под go test -race.
func TestStress_MockTransport_Concurrency(t *testing.T) {
	mock := NewMockTransport()
	client := &http.Client{Transport: mock}

	const (
		numWriters = 8
		numReaders = 24
		duration   = 1200 * time.Millisecond
	)

	var (
		stopFlag   int32
		writeCount int64
		readCount  int64
		wg         sync.WaitGroup
	)

	// Пишущие горутины: регистрация и сброс фикстур
	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			i := 0
			for atomic.LoadInt32(&stopFlag) == 0 {
				i++
				pattern := fmt.Sprintf("https://api.test-concurrent-%d.com/item/%d", workerID, i%10)
				switch i % 4 {
				case 0:
					mock.RegisterFixture(pattern, http.StatusOK, "application/json", []byte(fmt.Sprintf(`{"worker":%d,"i":%d}`, workerID, i)))
				case 1:
					mock.RegisterJSON(pattern, http.StatusCreated, map[string]int{"worker": workerID, "i": i})
				case 2:
					mock.RegisterHandler(pattern, func(req *http.Request) (*http.Response, error) {
						return makeResponse(req, http.StatusOK, "application/json", []byte(`{"dynamic":true}`)), nil
					})
				case 3:
					if i%20 == 0 {
						mock.Reset()
					}
				}
				atomic.AddInt64(&writeCount, 1)
			}
		}(w)
	}

	// Читающие горутины: выполнение RoundTrip через client.Do / Get
	urlsToTest := []string{
		"https://api.test-concurrent-0.com/item/0",
		"https://api.test-concurrent-1.com/item/1",
		"https://api.deezer.com/search?q=stress",
		"https://itunes.apple.com/search?term=stress",
		"https://unknown-external-domain.org/data",
		"https://api-v2.soundcloud.com/search?q=stress",
		"https://musicbrainz.org/ws/2/release",
	}

	for r := 0; r < numReaders; r++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			i := 0
			for atomic.LoadInt32(&stopFlag) == 0 {
				i++
				targetURL := urlsToTest[(workerID+i)%len(urlsToTest)]
				req, err := http.NewRequest(http.MethodGet, targetURL, nil)
				if err != nil {
					continue
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Errorf("worker %d: unexpected client error: %v", workerID, err)
					return
				}
				if resp != nil && resp.Body != nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
				atomic.AddInt64(&readCount, 1)
			}
		}(r)
	}

	time.Sleep(duration)
	atomic.StoreInt32(&stopFlag, 1)
	wg.Wait()

	t.Logf("MockTransport concurrency stress passed: %d writes, %d reads under -race with 0 races",
		atomic.LoadInt64(&writeCount), atomic.LoadInt64(&readCount))
}

// TestStress_MockTransport_HermeticIsolation проверяет абсолютную изоляцию тестов от внешнего интернета:
// любые неизвестные внешние хосты, порты и пути обязаны немедленно получать ответ HTTP 501 JSON
// со структурированной ошибкой без совершения реальных сетевых вызовов (инвариант AGENTS.md).
func TestStress_MockTransport_HermeticIsolation(t *testing.T) {
	mock := NewMockTransport()
	client := &http.Client{Transport: mock}

	adversarialTargets := []struct {
		url          string
		expectedHost string
		expectedPath string
	}{
		{"http://google.com/search?q=test", "google.com", "/search"},
		{"https://api.github.com/repos/test/repo", "api.github.com", "/repos/test/repo"},
		{"https://evil-hacker.com:9443/exfiltrate", "evil-hacker.com:9443", "/exfiltrate"},
		{"http://192.168.1.1:8080/router-login", "192.168.1.1:8080", "/router-login"},
		{"http://10.0.0.1:9090/internal-metrics", "10.0.0.1:9090", "/internal-metrics"},
		{"http://169.254.169.254/latest/meta-data/", "169.254.169.254", "/latest/meta-data/"},
		{"https://unknown-cdn.cloudflare.net/track.mp3", "unknown-cdn.cloudflare.net", "/track.mp3"},
		{"http://203.0.113.195:80/data", "203.0.113.195:80", "/data"},
		{"http://127.attacker.com/leak", "127.attacker.com", "/leak"},
	}

	for _, tt := range adversarialTargets {
		req, err := http.NewRequest(http.MethodGet, tt.url, nil)
		if err != nil {
			t.Fatalf("failed to construct request for %s: %v", tt.url, err)
		}

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("unexpected transport error for %s (should return 501, not network error): %v", tt.url, err)
		}

		if resp.StatusCode != http.StatusNotImplemented {
			t.Errorf("url %s: expected status 501 Not Implemented, got %d", tt.url, resp.StatusCode)
		}

		contentType := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(contentType, "application/json") {
			t.Errorf("url %s: expected application/json content-type, got %s", tt.url, contentType)
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("failed to read 501 response body: %v", err)
		}

		bodyStr := string(bodyBytes)
		if !strings.Contains(bodyStr, `"error":"unmocked external request in testkit"`) {
			t.Errorf("url %s: missing expected error message in body: %s", tt.url, bodyStr)
		}
		if !strings.Contains(bodyStr, fmt.Sprintf(`"host":"%s"`, strings.ToLower(tt.expectedHost))) {
			t.Errorf("url %s: host mismatch in 501 response: %s", tt.url, bodyStr)
		}
		if !strings.Contains(bodyStr, fmt.Sprintf(`"path":"%s"`, tt.expectedPath)) {
			t.Errorf("url %s: path mismatch in 501 response: %s", tt.url, bodyStr)
		}
	}
}

// TestStress_MockTransport_LoopbackPassthrough верифицирует беспрепятственный пропуск локального трафика
// (127.0.0.1, localhost, [::1], 127.0.0.2) к реальным серверам httptest.Server без блокировки 501.
func TestStress_MockTransport_LoopbackPassthrough(t *testing.T) {
	// Поднимаем реальный тестовый сервер httptest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		defer r.Body.Close()

		w.Header().Set("X-Echo-Method", r.Method)
		w.Header().Set("X-Echo-Header", r.Header.Get("X-Custom-Test"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"loopback_ok","path":"%s","body":"%s"}`, r.URL.Path, string(body))))
	}))
	defer ts.Close()

	mock := NewMockTransport()
	client := &http.Client{Transport: mock}

	// 1. Прямой запрос к 127.0.0.1:<port>
	req1, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/ping", strings.NewReader("hello loopback"))
	req1.Header.Set("X-Custom-Test", "secret-token")
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("loopback request failed: %v", err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK from loopback, got %d", resp1.StatusCode)
	}
	if resp1.Header.Get("X-Echo-Method") != http.MethodPost {
		t.Errorf("expected echoed method POST, got %s", resp1.Header.Get("X-Echo-Method"))
	}
	if resp1.Header.Get("X-Echo-Header") != "secret-token" {
		t.Errorf("expected echoed header, got %s", resp1.Header.Get("X-Echo-Header"))
	}

	body1, _ := io.ReadAll(resp1.Body)
	if !strings.Contains(string(body1), "loopback_ok") || !strings.Contains(string(body1), "hello loopback") {
		t.Errorf("unexpected loopback response body: %s", string(body1))
	}

	// 2. Запрос с хостом localhost:<port>
	port := ts.Listener.Addr().String()
	if colonIdx := strings.LastIndex(port, ":"); colonIdx != -1 {
		port = port[colonIdx:]
	}
	localhostURL := "http://localhost" + port + "/api/v1/localhost"
	req2, _ := http.NewRequest(http.MethodGet, localhostURL, nil)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("localhost request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for localhost, got %d", resp2.StatusCode)
	}

	// 2b. Запрос с хостом в верхнем регистре LOCALHOST:<port> (RFC 7230 case-insensitivity)
	localhostUpperURL := "http://LOCALHOST" + port + "/api/v1/localhost"
	reqUpper, _ := http.NewRequest(http.MethodGet, localhostUpperURL, nil)
	respUpper, err := client.Do(reqUpper)
	if err != nil {
		t.Fatalf("LOCALHOST upper request failed: %v", err)
	}
	defer respUpper.Body.Close()
	if respUpper.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for LOCALHOST, got %d", respUpper.StatusCode)
	}

	// 3. Параллельные конкурентные запросы к loopback
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r, err := client.Get(ts.URL + fmt.Sprintf("/parallel/%d", idx))
			if err != nil {
				t.Errorf("parallel loopback request %d failed: %v", idx, err)
				return
			}
			_ = r.Body.Close()
			if r.StatusCode != http.StatusOK {
				t.Errorf("parallel request %d got unexpected status %d", idx, r.StatusCode)
			}
		}(i)
	}
	wg.Wait()
}

// TestStress_MockTransport_HandlerSelfRegistrationDeadlock проверяет отсутствие взаимной блокировки (deadlock),
// если кастомный обработчик f.Handler внутри RoundTrip вызывает методы MockTransport (например, RegisterFixture или Reset).
func TestStress_MockTransport_HandlerSelfRegistrationDeadlock(t *testing.T) {
	mock := NewMockTransport()
	mock.RegisterHandler("https://api.test.com/step1", func(req *http.Request) (*http.Response, error) {
		mock.RegisterFixture("https://api.test.com/step2", http.StatusOK, "text/plain", []byte("step2"))
		return makeResponse(req, http.StatusOK, "text/plain", []byte("step1")), nil
	})

	done := make(chan bool, 1)
	go func() {
		client := &http.Client{Transport: mock}
		req, _ := http.NewRequest(http.MethodGet, "https://api.test.com/step1", nil)
		resp, err := client.Do(req)
		if err == nil && resp != nil {
			_ = resp.Body.Close()
		}
		done <- true
	}()

	select {
	case <-done:
		t.Log("Handler self-registration succeeded without deadlock")
	case <-time.After(300 * time.Millisecond):
		t.Errorf("DEADLOCK DETECTED! RegisterHandler callback deadlocked when calling RegisterFixture while holding m.mu.RLock()")
	}
}

// TestStress_CreateDummyMP3_PathTraversal проверяет защиту CreateDummyMP3 и CreateDummyMP3WithTags
// от полного спектра атак обхода директорий (Path Traversal, CWE-22): относительные пути ../,
// URL-encoding, Unicode normalization bypass, нулевые байты (\x00), абсолютные пути и попытки симлинк-побега.
func TestStress_CreateDummyMP3_PathTraversal(t *testing.T) {
	tempDir := t.TempDir()
	musicDir := filepath.Join(tempDir, "music_library")
	outsideDir := filepath.Join(tempDir, "outside_target")

	if err := os.MkdirAll(musicDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatal(err)
	}

	sb, err := sandbox.NewPathSandbox(musicDir)
	if err != nil {
		t.Fatalf("failed to create sandbox: %v", err)
	}

	traversalVectors := []struct {
		name string
		path string
	}{
		{"Parent escape 1", "../evil.mp3"},
		{"Parent escape 2", "../../evil.mp3"},
		{"Parent escape 3", "../../../evil.mp3"},
		{"Deep nested escape", "sub1/sub2/../../../../evil.mp3"},
		{"Leading slashes with parent escape", "////../../etc/passwd.mp3"},
		{"Null byte in filename", "track\x00.mp3"},
		{"Null byte in directory", "dir\x00/track.mp3"},
		{"Null byte middle", "a/b/\x00/c.mp3"},
		{"Single dot root attempt", "."},
		{"Empty path attempt", ""},
		{"Slash root attempt", "/"},
		{"URL encoded parent 1", "%2e%2e/%2e%2e/evil.mp3"},
		{"URL encoded slash", "..%2fevil.mp3"},
		{"Double URL encoded", "%252e%252e/%252e%252e/evil.mp3"},
		{"Deep multi-level URL encoded", "%2525252e%2525252e/evil.mp3"},
		{"Unicode fullwidth dots escape", "\uff0e\uff0e/\uff0e\uff0e/evil.mp3"},
		{"Unicode fullwidth slash escape", "..\uff0f..\uff0fevil.mp3"},
		{"Unicode fullwidth dots and slash", "\uff0e\uff0e\uff0f\uff0e\uff0e\uff0fevil.mp3"},
		{"Windows backslash escape 1", `..\..\evil.mp3`},
		{"Windows backslash escape 2", `sub\..\..\evil.mp3`},
	}

	for _, tt := range traversalVectors {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CreateDummyMP3(sb, tt.path, "Title", "Artist", "Album")
			if err == nil {
				t.Fatalf("VULNERABILITY DETECTED! Expected rejection for path %q, but CreateDummyMP3 succeeded", tt.path)
			}
		})
	}

	// 2. Проверяем, что за пределами песочницы НЕ появилось никаких файлов
	outsideEntries, err := os.ReadDir(outsideDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(outsideEntries) > 0 {
		t.Fatalf("VULNERABILITY DETECTED! Files were created in outsideDir: %d files found", len(outsideEntries))
	}

	// 3. Тест на попытку выхода через симлинк
	symlinkEscapeDir := filepath.Join(musicDir, "symlink_dir")
	if err := os.Symlink(outsideDir, symlinkEscapeDir); err == nil {
		_, err := CreateDummyMP3(sb, "symlink_dir/evil_symlink.mp3", "Title", "Artist", "Album")
		if err == nil {
			t.Fatal("VULNERABILITY DETECTED! CreateDummyMP3 allowed writing through symlink pointing outside sandbox!")
		}
	}
}

// TestStress_MPEG1Layer3_FrameValidation_And_ID3Tags верифицирует точную бинарную структуру
// сгенерированных аудиокадров MPEG-1 Layer 3 (sync word 0xFF 0xFB, 320 kbps, 44.1 kHz, Stereo),
// а также корректность чтения тегов ID3v2.4 через парсер id3.ReadTags.
func TestStress_MPEG1Layer3_FrameValidation_And_ID3Tags(t *testing.T) {
	tempDir := t.TempDir()
	sb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	testCases := []struct {
		relPath string
		title   string
		artist  string
		album   string
	}{
		{
			relPath: "Rock/Queen/Bohemian.mp3",
			title:   "Bohemian Rhapsody",
			artist:  "Queen",
			album:   "A Night at the Opera",
		},
		{
			relPath: "Russian/Kino/Pachka.mp3",
			title:   "Пачка сигарет",
			artist:  "Кино",
			album:   "Звезда по имени Солнце",
		},
		{
			relPath: "Special/Chars [2026]/Track #1 (Remastered).mp3",
			title:   "Song (feat. Guest) [Live @ London]",
			artist:  "Artist 1 & Artist 2",
			album:   "Greatest Hits (Vol. 1)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.title, func(t *testing.T) {
			absPath, err := CreateDummyMP3(sb, tc.relPath, tc.title, tc.artist, tc.album)
			if err != nil {
				t.Fatalf("CreateDummyMP3 failed for %q: %v", tc.relPath, err)
			}

			// 1. Проверяем права доступа файла (строго 0644)
			st, err := os.Stat(absPath)
			if err != nil {
				t.Fatalf("failed to stat created MP3: %v", err)
			}
			if perm := st.Mode().Perm(); perm != 0644 {
				t.Errorf("expected permissions 0644, got %#o", perm)
			}

			// 2. Читаем бинарные данные файла
			data, err := os.ReadFile(absPath)
			if err != nil {
				t.Fatalf("failed to read created MP3: %v", err)
			}

			// 3. Проверяем заголовок ID3v2.4 в начале файла
			if len(data) < 10 {
				t.Fatalf("file is too short: %d bytes", len(data))
			}
			if string(data[0:3]) != "ID3" {
				t.Fatalf("file does not start with ID3 header: %q", string(data[0:3]))
			}
			if data[3] != 0x04 { // ID3v2.4 major version
				t.Errorf("expected ID3 major version 4, got %d", data[3])
			}

			// Вычисляем размер ID3v2 тега
			tagSize := int(data[6]&0x7F)<<21 | int(data[7]&0x7F)<<14 | int(data[8]&0x7F)<<7 | int(data[9]&0x7F)
			audioOffset := 10 + tagSize

			if audioOffset > len(data) {
				t.Fatalf("audio offset %d exceeds file length %d", audioOffset, len(data))
			}

			audioBytes := data[audioOffset:]
			if len(audioBytes) < MPEG1Layer3FrameSize*4 {
				t.Fatalf("expected at least 4 MPEG-1 Layer 3 frames (%d bytes), got %d",
					MPEG1Layer3FrameSize*4, len(audioBytes))
			}

			// 4. Валидируем каждый из 4 сгенерированных кадров MPEG-1 Layer 3
			for frameIdx := 0; frameIdx < 4; frameIdx++ {
				offset := frameIdx * MPEG1Layer3FrameSize
				header := audioBytes[offset : offset+4]

				// Байт 0: 0xFF (Sync word bits 11-4)
				if header[0] != 0xFF {
					t.Errorf("frame %d: byte 0 expected 0xFF, got 0x%02X", frameIdx, header[0])
				}

				// Байт 1: 0xFB -> Sync word bits 3-1: 111, MPEG-1: 11, Layer 3: 01, Protection: 1
				if header[1] != 0xFB {
					t.Errorf("frame %d: byte 1 expected 0xFB, got 0x%02X", frameIdx, header[1])
				}

				// Проверяем битовые поля заголовка
				mpegVersion := (header[1] >> 3) & 0x03
				if mpegVersion != 0x03 { // 11 = MPEG Version 1
					t.Errorf("frame %d: expected MPEG Version 1 (3), got %d", frameIdx, mpegVersion)
				}
				layer := (header[1] >> 1) & 0x03
				if layer != 0x01 { // 01 = Layer 3
					t.Errorf("frame %d: expected Layer 3 (1), got %d", frameIdx, layer)
				}

				// Байт 2: 0xE0 -> Bitrate index: 1110 (320 kbps), Sample rate: 00 (44.1 kHz), Padding: 0
				bitrateIndex := (header[2] >> 4) & 0x0F
				if bitrateIndex != 0x0E { // 1110 = 320 kbps
					t.Errorf("frame %d: expected bitrate index 0x0E (320 kbps), got 0x%02X", frameIdx, bitrateIndex)
				}
				sampleRateIndex := (header[2] >> 2) & 0x03
				if sampleRateIndex != 0x00 { // 00 = 44.1 kHz
					t.Errorf("frame %d: expected sample rate 44.1 kHz (0), got %d", frameIdx, sampleRateIndex)
				}

				// Байт 3: 0x04 -> Channel mode: 00 (Stereo)
				channelMode := (header[3] >> 6) & 0x03
				if channelMode != 0x00 { // 00 = Stereo
					t.Errorf("frame %d: expected Stereo mode (0), got %d", frameIdx, channelMode)
				}
			}

			// 5. Верифицируем парсинг метаданных через internal/id3.ReadTags
			tags, err := id3.ReadTags(absPath, tc.relPath)
			if err != nil {
				t.Fatalf("id3.ReadTags failed for %s: %v", absPath, err)
			}

			if tags.Title != tc.title {
				t.Errorf("expected title %q, got %q", tc.title, tags.Title)
			}
			if tags.Artist != tc.artist {
				t.Errorf("expected artist %q, got %q", tc.artist, tags.Artist)
			}
			if tags.Album != tc.album {
				t.Errorf("expected album %q, got %q", tc.album, tags.Album)
			}
			if tags.Bitrate != 320 {
				t.Errorf("expected bitrate 320, got %d", tags.Bitrate)
			}
		})
	}
}

// TestStress_CreateDummyMP3WithTags_ExtendedAndOverwriting проверяет расширенные теги,
// перезапись существующего файла и сохранение обложки APIC.
func TestStress_CreateDummyMP3WithTags_ExtendedAndOverwriting(t *testing.T) {
	tempDir := t.TempDir()
	sb, err := sandbox.NewPathSandbox(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	// The ID3 reads locate the sandbox through a weak registry reference.
	defer runtime.KeepAlive(sb)

	relPath := "Electronic/Daft Punk/01. Around the World.mp3"

	// 1. Создаем первый раз
	tags1 := id3.TrackTags{
		Title:       "Around the World",
		Artist:      "Daft Punk",
		Album:       "Homework",
		TrackNumber: 1,
		TrackTotal:  16,
		Year:        1997,
	}
	absPath1, err := CreateDummyMP3WithTags(sb, relPath, tags1, nil)
	if err != nil {
		t.Fatalf("first creation failed: %v", err)
	}

	readTags1, err := id3.ReadTags(absPath1, relPath)
	if err != nil {
		t.Fatalf("reading first tags failed: %v", err)
	}
	if readTags1.Title != "Around the World" || readTags1.Year != 1997 {
		t.Errorf("unexpected tags: %+v", readTags1)
	}

	// 2. Перезаписываем тот же файл с новыми тегами и обложкой
	var artwork bytes.Buffer
	if err := jpeg.Encode(&artwork, image.NewRGBA(image.Rect(0, 0, 16, 16)), nil); err != nil {
		t.Fatal(err)
	}
	fakeCover := artwork.Bytes()
	cover := &id3.CoverInfo{
		MimeType: "image/jpeg",
		Data:     fakeCover,
	}
	tags2 := id3.TrackTags{
		Title:       "Around the World (Remastered)",
		Artist:      "Daft Punk",
		Album:       "Homework (25th Anniversary)",
		TrackNumber: 1,
		TrackTotal:  16,
		Year:        2022,
	}
	absPath2, err := CreateDummyMP3WithTags(sb, relPath, tags2, cover)
	if err != nil {
		t.Fatalf("overwriting creation failed: %v", err)
	}

	if absPath1 != absPath2 {
		t.Errorf("expected same path on overwrite: %s vs %s", absPath1, absPath2)
	}

	readTags2, err := id3.ReadTags(absPath2, relPath)
	if err != nil {
		t.Fatalf("reading overwritten tags failed: %v", err)
	}
	if readTags2.Title != "Around the World (Remastered)" || readTags2.Year != 2022 {
		t.Errorf("overwritten tags mismatch: %+v", readTags2)
	}

	readCover, err := id3.ReadCover(absPath2)
	if err != nil {
		t.Fatalf("reading cover failed: %v", err)
	}
	if readCover == nil || !bytes.Equal(readCover.Data, fakeCover) {
		t.Errorf("cover data mismatch or nil")
	}
}
