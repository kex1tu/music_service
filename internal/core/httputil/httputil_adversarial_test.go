package httputil

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type advPayload struct {
	Name    string         `json:"name"`
	Count   int            `json:"count"`
	Active  bool           `json:"active"`
	Nested  *advSubPayload `json:"nested,omitempty"`
	Tags    []string       `json:"tags,omitempty"`
	RawByte string         `json:"raw_byte,omitempty"`
}

type advSubPayload struct {
	ID   int64  `json:"id"`
	Data string `json:"data"`
}

// ==============================================================================
// 1. httputil.DecodeJSON Adversarial Stress Tests
// ==============================================================================

func TestAdversarial_DecodeJSON_ExceedingLimit(t *testing.T) {
	// A: Payload with a single huge string that exceeds 10MB limit (10MB + 1MB)
	t.Run("huge_string_exceeds_10MB", func(t *testing.T) {
		hugeSize := DefaultMaxBodyBytes + (1 << 20)
		body := bytes.NewBuffer(make([]byte, 0, hugeSize+64))
		body.WriteString(`{"name":"`)
		body.Write(bytes.Repeat([]byte("X"), hugeSize))
		body.WriteString(`"}`)

		req := httptest.NewRequest(http.MethodPost, "/test", body)
		var p advPayload
		err := DecodeJSON(req, &p)
		if err == nil {
			t.Fatalf("expected error for payload exceeding 10MB limit, got nil (read %d bytes)", len(p.Name))
		}
		// LimitReader truncates stream, causing syntax error or unexpected EOF
		if !errors.Is(err, io.ErrUnexpectedEOF) && !strings.Contains(err.Error(), "unexpected EOF") && !strings.Contains(err.Error(), "syntax error") {
			t.Logf("DecodeJSON error on 10MB limit as expected: %v", err)
		}
	})

	// B: Array of objects exceeding 10MB limit
	t.Run("huge_array_exceeds_10MB", func(t *testing.T) {
		// Repeats `{"name":"item"}` enough times to cross 10MB
		chunk := `{"name":"0123456789012345678901234567890123456789"},`
		repeats := (DefaultMaxBodyBytes / len(chunk)) + 100
		totalBytes := repeats * len(chunk)

		r, w := io.Pipe()
		go func() {
			defer w.Close()
			_, _ = w.Write([]byte(`[`))
			for i := 0; i < repeats; i++ {
				_, _ = w.Write([]byte(chunk))
			}
			_, _ = w.Write([]byte(`{"name":"last"}]`))
		}()

		req := httptest.NewRequest(http.MethodPost, "/test", r)
		var list []advPayload
		err := DecodeJSON(req, &list)
		if err == nil {
			t.Fatalf("expected error when array payload (%d bytes) exceeds 10MB limit, got nil", totalBytes)
		}
	})

	// C: Exactly 10MB payload (boundary check)
	t.Run("boundary_10MB_exact", func(t *testing.T) {
		prefix := `{"name":"`
		suffix := `"}`
		paddingNeeded := DefaultMaxBodyBytes - len(prefix) - len(suffix)
		body := bytes.NewBuffer(make([]byte, 0, DefaultMaxBodyBytes))
		body.WriteString(prefix)
		body.Write(bytes.Repeat([]byte("a"), paddingNeeded))
		body.WriteString(suffix)

		if body.Len() != DefaultMaxBodyBytes {
			t.Fatalf("setup error: expected length %d, got %d", DefaultMaxBodyBytes, body.Len())
		}

		req := httptest.NewRequest(http.MethodPost, "/test", body)
		var p advPayload
		err := DecodeJSON(req, &p)
		if err != nil {
			t.Fatalf("unexpected error for exact 10MB payload: %v", err)
		}
		if len(p.Name) != paddingNeeded {
			t.Errorf("expected string of %d chars, got %d", paddingNeeded, len(p.Name))
		}
	})

	// D: Exactly 10MB + 1 byte
	t.Run("boundary_10MB_plus_one", func(t *testing.T) {
		prefix := `{"name":"`
		suffix := `"}`
		paddingNeeded := DefaultMaxBodyBytes - len(prefix) - len(suffix) + 1
		body := bytes.NewBuffer(make([]byte, 0, DefaultMaxBodyBytes+1))
		body.WriteString(prefix)
		body.Write(bytes.Repeat([]byte("a"), paddingNeeded))
		body.WriteString(suffix)

		req := httptest.NewRequest(http.MethodPost, "/test", body)
		var p advPayload
		err := DecodeJSON(req, &p)
		if err == nil {
			t.Fatalf("expected error for 10MB+1 byte payload, got nil")
		}
	})
}

func TestAdversarial_DecodeJSON_NilAndMalformed(t *testing.T) {
	// Nil request
	t.Run("nil_request", func(t *testing.T) {
		var p advPayload
		err := DecodeJSON[advPayload](nil, &p)
		if err == nil || err.Error() != "empty request body" {
			t.Fatalf("expected 'empty request body', got %v", err)
		}
	})

	// Nil request body
	t.Run("nil_request_body", func(t *testing.T) {
		req := &http.Request{Body: nil}
		var p advPayload
		err := DecodeJSON(req, &p)
		if err == nil || err.Error() != "empty request body" {
			t.Fatalf("expected 'empty request body', got %v", err)
		}
	})

	// Nil target pointer
	t.Run("nil_target", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"name":"alice"}`))
		err := DecodeJSON[advPayload](req, nil)
		if err == nil || err.Error() != "target cannot be nil" {
			t.Fatalf("expected 'target cannot be nil', got %v", err)
		}
	})

	// Truncated JSON variants
	truncatedCases := map[string]string{
		"cut_in_key":           `{"na`,
		"cut_after_colon":      `{"name":`,
		"cut_in_string_value":  `{"name":"ali`,
		"cut_in_number":        `{"count":12`,
		"cut_in_array":         `{"tags":["pop", "ro`,
		"cut_in_nested_object": `{"nested":{"id":42, "da`,
		"open_brace_only":      `{`,
		"open_bracket_only":    `[`,
	}
	for name, raw := range truncatedCases {
		t.Run("truncated_"+name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(raw))
			var p advPayload
			err := DecodeJSON(req, &p)
			if err == nil {
				t.Fatalf("expected error for truncated json %q, got nil", raw)
			}
		})
	}

	// Invalid syntax variants
	syntaxCases := map[string]string{
		"unquoted_key":       `{name: "Alice"}`,
		"single_quotes":      `{'name': 'Alice'}`,
		"trailing_comma_obj": `{"name": "Alice",}`,
		"trailing_comma_arr": `{"tags": ["pop",]}`,
		"double_colon":       `{"name":: "Alice"}`,
		"colon_without_key":  `{: "Alice"}`,
		"missing_colon":      `{"name" "Alice"}`,
		"nan_value":          `{"count": NaN}`,
		"infinity_value":     `{"count": Infinity}`,
		"control_character":  "{\"name\": \"line\nbreak\"}", // unescaped literal newline inside JSON string
	}
	for name, raw := range syntaxCases {
		t.Run("syntax_"+name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(raw))
			var p advPayload
			err := DecodeJSON(req, &p)
			if err == nil {
				t.Fatalf("expected syntax error for %q, got nil", raw)
			}
		})
	}

	// Type mismatches
	typeMismatchCases := map[string]string{
		"string_for_int":     `{"name":"Alice","count":"hundred"}`,
		"object_for_string":  `{"name":{"first":"Alice"}}`,
		"array_for_bool":     `{"active":[true]}`,
		"number_for_nested":  `{"nested":42}`,
		"root_array_instead": `["not", "a", "struct"]`,
		"root_scalar_bool":   `true`,
		"root_scalar_number": `12345`,
		"root_scalar_string": `"just a string"`,
	}
	for name, raw := range typeMismatchCases {
		t.Run("type_mismatch_"+name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(raw))
			var p advPayload
			err := DecodeJSON(req, &p)
			if err == nil {
				t.Fatalf("expected type unmarshal error for %q, got nil", raw)
			}
		})
	}
}

func TestAdversarial_DecodeJSON_UnicodeNullBytes(t *testing.T) {
	// A: Valid JSON with escaped unicode null byte `\u0000` inside string
	t.Run("escaped_unicode_null_in_string", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"name":"track\u0000name","count":1}`))
		var p advPayload
		err := DecodeJSON(req, &p)
		if err != nil {
			t.Fatalf("unexpected error for escaped unicode null: %v", err)
		}
		if p.Name != "track\x00name" {
			t.Errorf("expected string containing null byte, got %q", p.Name)
		}
	})

	// B: Escaped unicode null byte as single character
	t.Run("single_escaped_null", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"name":"\u0000"}`))
		var p advPayload
		err := DecodeJSON(req, &p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Name != "\x00" {
			t.Errorf("expected '\\x00', got %q", p.Name)
		}
	})

	// C: Escaped unicode null byte in object key
	t.Run("escaped_null_in_map_key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"key\u0000with\u0000null":"value"}`))
		var m map[string]string
		err := DecodeJSON(req, &m)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val, exists := m["key\x00with\x00null"]; !exists || val != "value" {
			t.Errorf("expected key with null byte, map: %+v", m)
		}
	})

	// D: Raw, unescaped null byte (0x00) inside JSON string (RFC 8259 violation)
	t.Run("raw_unescaped_null_byte", func(t *testing.T) {
		rawJSON := []byte("{\"name\":\"track\x00name\"}")
		req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(rawJSON))
		var p advPayload
		err := DecodeJSON(req, &p)
		if err == nil {
			t.Fatalf("expected error for unescaped raw null byte in JSON string, got nil")
		}
		if !strings.Contains(err.Error(), "invalid character") {
			t.Logf("raw null byte rejected with: %v", err)
		}
	})
}

// ==============================================================================
// 2. QueryInt64 Adversarial Stress Tests
// ==============================================================================

func TestAdversarial_QueryInt64_ExtremeValues(t *testing.T) {
	tests := []struct {
		name       string
		val        string
		defaultVal int64
		want       int64
	}{
		// Math limits
		{"max_int64", "9223372036854775807", -1, math.MaxInt64},
		{"min_int64", "-9223372036854775808", 0, math.MinInt64},
		{"max_int64_minus_1", "9223372036854775806", 0, math.MaxInt64 - 1},
		{"min_int64_plus_1", "-9223372036854775807", 0, math.MinInt64 + 1},

		// Overflow / Underflow
		{"overflow_positive_by_1", "9223372036854775808", 42, 42},
		{"underflow_negative_by_1", "-9223372036854775809", 42, 42},
		{"massive_positive_overflow", "999999999999999999999999999999999999999999", -100, -100},
		{"massive_negative_underflow", "-999999999999999999999999999999999999999999", 500, 500},
		{"max_uint64_overflow", "18446744073709551615", 7, 7},

		// Hexadecimal (ParseInt with base 10 MUST reject)
		{"hex_lowercase", "0x10", 99, 99},
		{"hex_uppercase", "0X1F", 99, 99},
		{"hex_max_int64", "0x7FFFFFFFFFFFFFFF", 99, 99},
		{"hex_without_prefix_a_f", "12a34", 99, 99},

		// Binary / Octal
		{"binary_prefix", "0b1010", 99, 99},
		{"octal_leading_zero", "0777", 0, 777}, // Base 10 parses "0777" as decimal 777
		{"octal_prefix", "0o777", 99, 99},

		// Signs and zeros
		{"zero", "0", 123, 0},
		{"negative_zero", "-0", 123, 0},
		{"explicit_positive_sign", "+42", 0, 42},
		{"explicit_positive_max", "+9223372036854775807", 0, math.MaxInt64},
		{"double_plus", "++42", 99, 99},
		{"double_minus", "--42", 99, 99},
		{"plus_minus", "+-42", 99, 99},

		// Whitespace
		{"leading_space", " 42", 99, 99},
		{"trailing_space", "42 ", 99, 99},
		{"surrounding_space", " 42 ", 99, 99},
		{"tab_character", "\t42", 99, 99},
		{"newline_character", "\n42", 99, 99},

		// Empty / Special Strings
		{"empty_string", "", 888, 888},
		{"space_only", "   ", 888, 888},
		{"null_string", "null", 888, 888},
		{"nan_string", "NaN", 888, 888},
		{"infinity_string", "Inf", 888, 888},

		// Floats / Scientific
		{"float_simple", "42.0", 99, 99},
		{"float_sub_one", "0.999", 99, 99},
		{"scientific_notation", "1e5", 99, 99},

		// Unicode & Nulls
		{"unicode_digits", "１２３", 99, 99}, // Full-width unicode digits
		{"embedded_null_byte", "42\x00", 99, 99},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{}
			q.Set("param", tc.val)
			req := httptest.NewRequest(http.MethodGet, "/?"+q.Encode(), nil)

			got := QueryInt64(req, "param", tc.defaultVal)
			if got != tc.want {
				t.Errorf("QueryInt64 for %s (%q, default=%d): got %d, want %d",
					tc.name, tc.val, tc.defaultVal, got, tc.want)
			}
		})
	}

	// Missing key
	t.Run("missing_key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/?other=123", nil)
		if v := QueryInt64(req, "param", 777); v != 777 {
			t.Errorf("expected default 777 for missing key, got %d", v)
		}
	})

	// Nil request & nil URL
	t.Run("nil_request_and_url", func(t *testing.T) {
		if v := QueryInt64(nil, "param", 1234); v != 1234 {
			t.Errorf("expected 1234 on nil req, got %d", v)
		}
		if v := QueryInt64(&http.Request{URL: nil}, "param", 1234); v != 1234 {
			t.Errorf("expected 1234 on nil URL, got %d", v)
		}
	})
}

// ==============================================================================
// 3. ErrorResponse JSON Output & Contract Verification
// ==============================================================================

func TestAdversarial_ErrorResponse_Contract(t *testing.T) {
	statusCodes := []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusMethodNotAllowed,
		http.StatusConflict,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
	}

	for _, code := range statusCodes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			rec := httptest.NewRecorder()
			msg := "Error condition: " + http.StatusText(code)
			RespondError(rec, code, msg)

			if rec.Code != code {
				t.Fatalf("expected status code %d, got %d", code, rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("expected Content-Type 'application/json', got %q", ct)
			}

			// Validate raw JSON string
			rawBody := rec.Body.Bytes()
			var rawMap map[string]any
			if err := json.Unmarshal(rawBody, &rawMap); err != nil {
				t.Fatalf("failed to parse JSON response: %v\nBody: %s", err, string(rawBody))
			}

			// Both 'error' and 'detail' must be present and match contract
			valError, hasError := rawMap["error"]
			valDetail, hasDetail := rawMap["detail"]
			valCode, hasCode := rawMap["code"]

			if !hasError {
				t.Errorf("contract violation: missing 'error' key in JSON response: %s", string(rawBody))
			} else if valError != msg {
				t.Errorf("expected 'error'=%q, got %q", msg, valError)
			}

			if !hasDetail {
				t.Errorf("contract violation: missing 'detail' key in JSON response: %s", string(rawBody))
			} else if valDetail != msg {
				t.Errorf("expected 'detail'=%q, got %q", msg, valDetail)
			}

			if !hasCode {
				t.Errorf("contract violation: missing 'code' key in JSON response: %s", string(rawBody))
			} else if int(valCode.(float64)) != code {
				t.Errorf("expected 'code'=%d, got %v", code, valCode)
			}
		})
	}

	// Special characters in error message
	specialMessages := map[string]string{
		"quotes_and_slashes":   `Invalid input with "quotes" and /slashes\`,
		"html_tags":            `<script>alert('xss')</script>`,
		"unicode_multilingual": `Ошибка обработки: файл "песня.mp3" not found (404 🎵)`,
		"control_characters":   "Message with tabs\tand\nnewlines",
	}

	for name, msg := range specialMessages {
		t.Run("special_"+name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			RespondError(rec, http.StatusBadRequest, msg)

			var resp ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to unmarshal JSON with special characters: %v", err)
			}
			if resp.Error != msg {
				t.Errorf("Error field mismatch: got %q, want %q", resp.Error, msg)
			}
			if resp.Detail != msg {
				t.Errorf("Detail field mismatch: got %q, want %q", resp.Detail, msg)
			}
		})
	}

	// Empty error message edge case
	t.Run("empty_message_behavior", func(t *testing.T) {
		rec := httptest.NewRecorder()
		RespondError(rec, http.StatusInternalServerError, "")

		var rawMap map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &rawMap); err != nil {
			t.Fatalf("failed to parse JSON response: %v", err)
		}

		// 'error' has no omitempty -> always serialized
		if rawMap["error"] != "" {
			t.Errorf("expected error='' in raw JSON, got %v", rawMap["error"])
		}

		// Note: 'detail' has omitempty in ErrorResponse struct.
		// If message is "", Detail is omitted by encoding/json!
		_, hasDetail := rawMap["detail"]
		t.Logf("When message is empty, 'detail' present: %v (omitempty behavior)", hasDetail)
	})
}
