package httputil

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type samplePayload struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func TestDecodeJSON(t *testing.T) {
	t.Run("valid payload", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"Alice","age":30}`))
		var p samplePayload
		err := DecodeJSON(req, &p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Name != "Alice" || p.Age != 30 {
			t.Errorf("unexpected decoded payload: %+v", p)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{invalid`))
		var p samplePayload
		err := DecodeJSON(req, &p)
		if err == nil {
			t.Fatalf("expected error for invalid json, got nil")
		}
	})

	t.Run("nil body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		var p samplePayload
		err := DecodeJSON(req, &p)
		if err == nil {
			t.Fatalf("expected error for empty/nil body, got nil")
		}
	})

	t.Run("nil request", func(t *testing.T) {
		var p samplePayload
		err := DecodeJSON[samplePayload](nil, &p)
		if err == nil {
			t.Fatalf("expected error for nil request, got nil")
		}
	})

	t.Run("nil target", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"Alice"}`))
		err := DecodeJSON[samplePayload](req, nil)
		if err == nil {
			t.Fatalf("expected error for nil target, got nil")
		}
	})

	t.Run("empty body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(""))
		var p samplePayload
		err := DecodeJSON(req, &p)
		if err != io.EOF {
			t.Fatalf("expected io.EOF for empty body, got: %v", err)
		}
	})

	t.Run("payload exceeds max body limit", func(t *testing.T) {
		hugePayload := `{"name":"` + strings.Repeat("a", DefaultMaxBodyBytes+1024) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(hugePayload))
		var p samplePayload
		err := DecodeJSON(req, &p)
		if err == nil {
			t.Fatalf("expected error for oversized body, got nil")
		}
	})
}

func TestRespondJSON(t *testing.T) {
	t.Run("struct payload", func(t *testing.T) {
		rec := httptest.NewRecorder()
		payload := samplePayload{Name: "Bob", Age: 25}
		RespondJSON(rec, http.StatusOK, payload)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
		if rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("expected application/json header, got %s", rec.Header().Get("Content-Type"))
		}

		var out samplePayload
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if out != payload {
			t.Errorf("got %+v, want %+v", out, payload)
		}
	})

	t.Run("nil data", func(t *testing.T) {
		rec := httptest.NewRecorder()
		RespondJSON(rec, http.StatusNoContent, nil)

		if rec.Code != http.StatusNoContent {
			t.Errorf("expected 204, got %d", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("expected empty body for nil data, got %q", rec.Body.String())
		}
	})
}

func TestRespondError(t *testing.T) {
	rec := httptest.NewRecorder()
	RespondError(rec, http.StatusBadRequest, "Invalid input")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected application/json header, got %s", rec.Header().Get("Content-Type"))
	}

	var errResp ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to unmarshal error: %v", err)
	}
	if errResp.Error != "Invalid input" {
		t.Errorf("expected Error == 'Invalid input', got %q", errResp.Error)
	}
	if errResp.Detail != "Invalid input" {
		t.Errorf("expected Detail == 'Invalid input', got %q", errResp.Detail)
	}
	if errResp.Code != http.StatusBadRequest {
		t.Errorf("expected Code == 400, got %d", errResp.Code)
	}

	var rawMap map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rawMap); err != nil {
		t.Fatalf("failed to unmarshal raw map: %v", err)
	}
	if rawMap["error"] != "Invalid input" || rawMap["detail"] != "Invalid input" {
		t.Errorf("unexpected raw map content: %+v", rawMap)
	}
	if int(rawMap["code"].(float64)) != http.StatusBadRequest {
		t.Errorf("unexpected raw code: %v", rawMap["code"])
	}
}

func TestQueryString(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?name=Charlie&empty=", nil)

	if s := QueryString(req, "name", "default"); s != "Charlie" {
		t.Errorf("expected Charlie, got %s", s)
	}
	if s := QueryString(req, "missing", "default"); s != "default" {
		t.Errorf("expected default, got %s", s)
	}
	if s := QueryString(req, "empty", "default"); s != "default" {
		t.Errorf("expected default for empty string, got %s", s)
	}
	if s := QueryString(nil, "name", "fallback"); s != "fallback" {
		t.Errorf("expected fallback for nil request, got %s", s)
	}
	if s := QueryString(&http.Request{}, "name", "fallback"); s != "fallback" {
		t.Errorf("expected fallback for nil URL, got %s", s)
	}
}

func TestQueryInt(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?limit=25&negative=-5&zero=0&invalid=abc&float=1.5&empty=", nil)

	if i := QueryInt(req, "limit", 10); i != 25 {
		t.Errorf("expected 25, got %d", i)
	}
	if i := QueryInt(req, "negative", 10); i != -5 {
		t.Errorf("expected -5, got %d", i)
	}
	if i := QueryInt(req, "zero", 10); i != 0 {
		t.Errorf("expected 0, got %d", i)
	}
	if i := QueryInt(req, "missing", 10); i != 10 {
		t.Errorf("expected 10, got %d", i)
	}
	if i := QueryInt(req, "empty", 10); i != 10 {
		t.Errorf("expected 10 on empty value, got %d", i)
	}
	if i := QueryInt(req, "invalid", 10); i != 10 {
		t.Errorf("expected 10 on parse error, got %d", i)
	}
	if i := QueryInt(req, "float", 10); i != 10 {
		t.Errorf("expected 10 on float value, got %d", i)
	}
	if i := QueryInt(nil, "limit", 10); i != 10 {
		t.Errorf("expected default on nil request, got %d", i)
	}
	if i := QueryInt(&http.Request{}, "limit", 10); i != 10 {
		t.Errorf("expected default on nil URL, got %d", i)
	}
}

func TestQueryInt64(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?id=42&large=70000000000&max=9223372036854775807&min=-9223372036854775808&overflow=9223372036854775808&underflow=-9223372036854775809&negative=-100&zero=0&invalid=xyz&float=123.45&empty=", nil)

	if v := QueryInt64(req, "id", 1); v != 42 {
		t.Errorf("expected 42, got %d", v)
	}
	if v := QueryInt64(req, "large", 1); v != 70000000000 {
		t.Errorf("expected 70000000000, got %d", v)
	}
	if v := QueryInt64(req, "max", 0); v != math.MaxInt64 {
		t.Errorf("expected MaxInt64, got %d", v)
	}
	if v := QueryInt64(req, "min", 0); v != math.MinInt64 {
		t.Errorf("expected MinInt64, got %d", v)
	}
	if v := QueryInt64(req, "overflow", 99); v != 99 {
		t.Errorf("expected default 99 on overflow, got %d", v)
	}
	if v := QueryInt64(req, "underflow", 99); v != 99 {
		t.Errorf("expected default 99 on underflow, got %d", v)
	}
	if v := QueryInt64(req, "negative", 0); v != -100 {
		t.Errorf("expected -100, got %d", v)
	}
	if v := QueryInt64(req, "zero", 99); v != 0 {
		t.Errorf("expected 0, got %d", v)
	}
	if v := QueryInt64(req, "missing", 555); v != 555 {
		t.Errorf("expected default 555 on missing key, got %d", v)
	}
	if v := QueryInt64(req, "empty", 555); v != 555 {
		t.Errorf("expected default 555 on empty key, got %d", v)
	}
	if v := QueryInt64(req, "invalid", 555); v != 555 {
		t.Errorf("expected default 555 on invalid string, got %d", v)
	}
	if v := QueryInt64(req, "float", 555); v != 555 {
		t.Errorf("expected default 555 on float string, got %d", v)
	}
	if v := QueryInt64(nil, "id", 999); v != 999 {
		t.Errorf("expected default 999 on nil request, got %d", v)
	}
	if v := QueryInt64(&http.Request{}, "id", 999); v != 999 {
		t.Errorf("expected default 999 on nil URL, got %d", v)
	}
}

func TestQueryBool(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?t1=true&t2=1&t3=t&t4=TRUE&f1=false&f2=0&f3=f&f4=FALSE&invalid=yes&empty=", nil)

	truthy := []string{"t1", "t2", "t3", "t4"}
	for _, key := range truthy {
		if !QueryBool(req, key, false) {
			t.Errorf("expected true for key %q", key)
		}
	}

	falsy := []string{"f1", "f2", "f3", "f4"}
	for _, key := range falsy {
		if QueryBool(req, key, true) {
			t.Errorf("expected false for key %q", key)
		}
	}

	if b := QueryBool(req, "missing", true); !b {
		t.Errorf("expected default true on missing key, got false")
	}
	if b := QueryBool(req, "missing", false); b {
		t.Errorf("expected default false on missing key, got true")
	}
	if b := QueryBool(req, "empty", true); !b {
		t.Errorf("expected default true on empty key, got false")
	}
	if b := QueryBool(req, "invalid", true); !b {
		t.Errorf("expected default true on invalid value, got false")
	}
	if b := QueryBool(nil, "t1", true); !b {
		t.Errorf("expected default true on nil request, got false")
	}
	if b := QueryBool(&http.Request{}, "t1", true); !b {
		t.Errorf("expected default true on nil URL, got false")
	}
}
