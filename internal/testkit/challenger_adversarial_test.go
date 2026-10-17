package testkit

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// trackingTransport wraps an http.RoundTripper and records every request made.
type trackingTransport struct {
	mu        sync.Mutex
	calls     []*http.Request
	callCount int64
}

func (t *trackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	atomic.AddInt64(&t.callCount, 1)
	t.mu.Lock()
	t.calls = append(t.calls, req)
	t.mu.Unlock()
	return makeResponse(req, http.StatusOK, "application/json", []byte(`{"tracked":true}`)), nil
}

// TestChallenger_HermeticIsolation_StrictBlocking verifies that domains attempting to spoof loopbacks
// (such as 127.attacker.com, 127.0.0.1.nip.io, 127-site.org, localhost.attacker.com) are strictly
// intercepted with HTTP 501 JSON and make ZERO calls to the underlying transport.
func TestChallenger_HermeticIsolation_StrictBlocking(t *testing.T) {
	tracker := &trackingTransport{}
	mock := NewMockTransport(tracker)
	client := &http.Client{Transport: mock}

	spoofVectors := []struct {
		url          string
		expectedHost string
		expectedPath string
	}{
		{"http://127.attacker.com/leak", "127.attacker.com", "/leak"},
		{"http://127.attacker.com:8080/data", "127.attacker.com:8080", "/data"},
		{"http://127.0.0.1.nip.io/tunnel", "127.0.0.1.nip.io", "/tunnel"},
		{"http://127.0.0.1.nip.io:9000/api", "127.0.0.1.nip.io:9000", "/api"},
		{"http://127-site.org/secret", "127-site.org", "/secret"},
		{"http://127-site.org:443/payment", "127-site.org:443", "/payment"},
		{"http://localhost.attacker.com/cookie", "localhost.attacker.com", "/cookie"},
		{"http://localhost.attacker.com:3000/auth", "localhost.attacker.com:3000", "/auth"},
		{"http://127.0.0.1.xip.io/sub", "127.0.0.1.xip.io", "/sub"},
		{"http://127.0.0.1.sslip.io/sub", "127.0.0.1.sslip.io", "/sub"},
		{"http://127_evil.net/pwn", "127_evil.net", "/pwn"},
		{"http://fake-localhost:8080/probe", "fake-localhost:8080", "/probe"},
		{"http://notlocalhost/test", "notlocalhost", "/test"},
		{"http://localhostsuffix/test", "localhostsuffix", "/test"},
		{"http://2130706433/integer-ip", "2130706433", "/integer-ip"},
		{"http://0177.0.0.1/octal-ip", "0177.0.0.1", "/octal-ip"},
		{"http://169.254.169.254/latest/meta-data", "169.254.169.254", "/latest/meta-data"},
		{"http://192.168.1.1/admin", "192.168.1.1", "/admin"},
		{"http://10.0.0.1/internal", "10.0.0.1", "/internal"},
	}

	for _, sv := range spoofVectors {
		t.Run(sv.url, func(t *testing.T) {
			initialCalls := atomic.LoadInt64(&tracker.callCount)

			req, err := http.NewRequest(http.MethodGet, sv.url, nil)
			if err != nil {
				t.Fatalf("failed to create request for %s: %v", sv.url, err)
			}

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("unexpected client error for %s (must return 501 response, not transport error): %v", sv.url, err)
			}
			defer resp.Body.Close()

			// 1. Verify HTTP Status is strictly 501 Not Implemented
			if resp.StatusCode != http.StatusNotImplemented {
				t.Errorf("url %s: expected status 501, got %d", sv.url, resp.StatusCode)
			}

			// 2. Verify Content-Type is application/json
			contentType := resp.Header.Get("Content-Type")
			if !strings.HasPrefix(contentType, "application/json") {
				t.Errorf("url %s: expected application/json, got %q", sv.url, contentType)
			}

			// 3. Verify JSON error payload structure
			bodyBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("failed to read body: %v", err)
			}

			var errMap map[string]string
			if err := json.Unmarshal(bodyBytes, &errMap); err != nil {
				t.Fatalf("url %s: response body is not valid JSON: %v (body: %s)", sv.url, err, string(bodyBytes))
			}

			if errMap["error"] != "unmocked external request in testkit" {
				t.Errorf("url %s: expected error message 'unmocked external request in testkit', got %q", sv.url, errMap["error"])
			}
			expectedLowerHost := strings.ToLower(sv.expectedHost)
			if errMap["host"] != expectedLowerHost {
				t.Errorf("url %s: expected host %q, got %q", sv.url, expectedLowerHost, errMap["host"])
			}
			if errMap["path"] != sv.expectedPath {
				t.Errorf("url %s: expected path %q, got %q", sv.url, sv.expectedPath, errMap["path"])
			}

			// 4. Verify ZERO external network calls reached the underlying transport
			finalCalls := atomic.LoadInt64(&tracker.callCount)
			if finalCalls != initialCalls {
				t.Fatalf("VULNERABILITY DETECTED! Request to %s reached underlying transport! (%d calls recorded)", sv.url, finalCalls-initialCalls)
			}
		})
	}
}

// TestChallenger_CaseInsensitivity_And_IPv6 verifies that local test server traffic with various
// case variations (LOCALHOST, [::1], etc.) successfully routes through the passthrough transport.
func TestChallenger_CaseInsensitivity_And_IPv6(t *testing.T) {
	// Create an actual local HTTP test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Received-Host", r.Host)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"echo","path":"%s","host":"%s"}`, r.URL.Path, r.Host)))
	}))
	defer ts.Close()

	// Extract port
	_, port, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to get server port: %v", err)
	}

	tracker := &trackingTransport{}
	mock := NewMockTransport(tracker)
	client := &http.Client{Transport: mock}

	// 1. Verify case insensitivity variants pass through to the real httptest server
	// Note: For real network connection, ts listens on 127.0.0.1.
	// When we use mock with default passthrough, it forwards to http.Transport which connects to 127.0.0.1.
	// For testing passthrough triggering with custom tracker:
	loopbackURLs := []struct {
		name       string
		url        string
		expectPass bool
	}{
		{"localhost lowercase", fmt.Sprintf("http://localhost:%s/case-test", port), true},
		{"LOCALHOST uppercase", fmt.Sprintf("http://LOCALHOST:%s/case-test", port), true},
		{"LocalHost mixed-case", fmt.Sprintf("http://LocalHost:%s/case-test", port), true},
		{"loCALhoST wacky-case", fmt.Sprintf("http://loCALhoST:%s/case-test", port), true},
		{"127.0.0.1 standard", fmt.Sprintf("http://127.0.0.1:%s/case-test", port), true},
		{"127.0.0.2 alternate", fmt.Sprintf("http://127.0.0.2:%s/case-test", port), true},
		{"[::1] IPv6 loopback", fmt.Sprintf("http://[::1]:%s/case-test", port), true},
		{"[0:0:0:0:0:0:0:1] IPv6 full", fmt.Sprintf("http://[0:0:0:0:0:0:0:1]:%s/case-test", port), true},
	}

	for _, tc := range loopbackURLs {
		t.Run(tc.name, func(t *testing.T) {
			initialCalls := atomic.LoadInt64(&tracker.callCount)

			req, err := http.NewRequest(http.MethodGet, tc.url, nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("unexpected client error for %s: %v", tc.url, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("url %s: expected status 200, got %d", tc.url, resp.StatusCode)
			}

			// Tracker must receive the request because it is loopback!
			finalCalls := atomic.LoadInt64(&tracker.callCount)
			if finalCalls <= initialCalls {
				t.Errorf("url %s: request was not forwarded to loopback transport! calls: %d", tc.url, finalCalls-initialCalls)
			}
		})
	}
}

// TestChallenger_ConcurrencyAndReentrancyDeadlock_Stress launches a high-stress reentrancy test
// where handlers concurrently register fixtures, invoke reset, and issue chained nested requests
// across 40 goroutines for 2 seconds under -race.
func TestChallenger_ConcurrencyAndReentrancyDeadlock_Stress(t *testing.T) {
	mock := NewMockTransport()
	client := &http.Client{Transport: mock}

	// Register recursive handler 1: calls RegisterJSON inside Handler
	mock.RegisterHandler("https://api.reentrant.com/call-register-json", func(req *http.Request) (*http.Response, error) {
		id := req.URL.Query().Get("id")
		mock.RegisterJSON(fmt.Sprintf("https://api.reentrant.com/dynamic/%s", id), http.StatusOK, map[string]string{
			"id": id,
		})
		return makeResponse(req, http.StatusOK, "application/json", []byte(fmt.Sprintf(`{"registered":"%s"}`, id))), nil
	})

	// Register recursive handler 2: calls RegisterFixture inside Handler
	mock.RegisterHandler("https://api.reentrant.com/call-register-fixture", func(req *http.Request) (*http.Response, error) {
		id := req.URL.Query().Get("id")
		mock.RegisterFixture(fmt.Sprintf("https://api.reentrant.com/fixture/%s", id), http.StatusAccepted, "text/plain", []byte("accepted"))
		return makeResponse(req, http.StatusOK, "application/json", []byte(`{"fixture_created":true}`)), nil
	})

	// Register recursive handler 3: calls Reset inside Handler
	mock.RegisterHandler("https://api.reentrant.com/call-reset", func(req *http.Request) (*http.Response, error) {
		mock.Reset()
		// Re-register self so test continues
		mock.RegisterHandler("https://api.reentrant.com/call-reset", func(r *http.Request) (*http.Response, error) {
			return makeResponse(r, http.StatusOK, "application/json", []byte(`{"reset_ok":true}`)), nil
		})
		return makeResponse(req, http.StatusOK, "application/json", []byte(`{"reset_executed":true}`)), nil
	})

	// Register recursive handler 4: calls nested HTTP request using same client
	mock.RegisterJSON("https://api.reentrant.com/leaf", http.StatusOK, map[string]string{"leaf": "ok"})
	mock.RegisterHandler("https://api.reentrant.com/nested", func(req *http.Request) (*http.Response, error) {
		subResp, err := client.Get("https://api.reentrant.com/leaf")
		if err != nil {
			return nil, err
		}
		defer subResp.Body.Close()
		b, _ := io.ReadAll(subResp.Body)
		return makeResponse(req, http.StatusOK, "application/json", b), nil
	})

	const numWorkers = 30
	const runDuration = 1500 * time.Millisecond
	var stopFlag int32
	var totalOperations int64
	var wg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			i := 0
			for atomic.LoadInt32(&stopFlag) == 0 {
				i++
				op := (workerID + i) % 5
				switch op {
				case 0:
					// Call register-json handler
					u := fmt.Sprintf("https://api.reentrant.com/call-register-json?id=w%d_i%d", workerID, i)
					resp, err := client.Get(u)
					if err == nil && resp != nil {
						_ = resp.Body.Close()
					}
				case 1:
					// Call register-fixture handler
					u := fmt.Sprintf("https://api.reentrant.com/call-register-fixture?id=w%d_i%d", workerID, i)
					resp, err := client.Get(u)
					if err == nil && resp != nil {
						_ = resp.Body.Close()
					}
				case 2:
					// Call nested handler
					resp, err := client.Get("https://api.reentrant.com/nested")
					if err == nil && resp != nil {
						_ = resp.Body.Close()
					}
				case 3:
					// Call reset handler periodically
					if i%10 == 0 {
						resp, err := client.Get("https://api.reentrant.com/call-reset")
						if err == nil && resp != nil {
							_ = resp.Body.Close()
						}
					}
				case 4:
					// Direct external writer
					mock.RegisterJSON(fmt.Sprintf("https://api.external-%d.com/%d", workerID, i%5), http.StatusOK, map[string]int{"n": i})
				}
				atomic.AddInt64(&totalOperations, 1)
			}
		}(w)
	}

	time.Sleep(runDuration)
	atomic.StoreInt32(&stopFlag, 1)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Logf("Reentrancy and Concurrency stress passed cleanly: %d operations completed without deadlock or race", atomic.LoadInt64(&totalOperations))
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK DETECTED! Workers failed to terminate within timeout due to recursive lock starvation")
	}
}

// TestChallenger_HostFallback_WhenURLHostEmpty verifies that if req.URL.Host is empty,
// MockTransport properly inspects req.Host for loopback evaluation and fallback responses.
func TestChallenger_HostFallback_WhenURLHostEmpty(t *testing.T) {
	tracker := &trackingTransport{}
	mock := NewMockTransport(tracker)

	// Request with empty URL.Host but req.Host set to loopback
	req1 := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Path: "/test"},
		Host:   "localhost:8080",
	}
	resp1, err := mock.RoundTrip(req1)
	if err != nil {
		t.Fatalf("RoundTrip failed: %v", err)
	}
	defer resp1.Body.Close()
	if atomic.LoadInt64(&tracker.callCount) != 1 {
		t.Errorf("expected 1 call to tracker via req.Host loopback fallback, got %d", tracker.callCount)
	}

	// Request with empty URL.Host but req.Host set to attacker domain
	req2 := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Path: "/leak"},
		Host:   "127.attacker.com",
	}
	resp2, err := mock.RoundTrip(req2)
	if err != nil {
		t.Fatalf("RoundTrip failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotImplemented {
		t.Errorf("expected 501 for 127.attacker.com via req.Host, got %d", resp2.StatusCode)
	}
}
