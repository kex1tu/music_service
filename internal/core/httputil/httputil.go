package httputil

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

// DefaultMaxBodyBytes limits incoming JSON payloads to 10 MB (safe for batch uploads).
const DefaultMaxBodyBytes = 10 << 20

// ErrorResponse represents the canonical JSON error structure.
// It includes Error, Detail (for compatibility with legacy UI/docs), and Code.
type ErrorResponse struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
	Code   int    `json:"code,omitempty"`
}

// DecodeJSON reads and decodes JSON from r.Body into target with a size limit.
func DecodeJSON[T any](r *http.Request, target *T) error {
	if r == nil || r.Body == nil {
		return errors.New("empty request body")
	}
	if target == nil {
		return errors.New("target cannot be nil")
	}
	defer r.Body.Close()

	reader := io.LimitReader(r.Body, DefaultMaxBodyBytes)
	if err := json.NewDecoder(reader).Decode(target); err != nil {
		return err
	}
	return nil
}

// RespondJSON writes HTTP status and serializes data as JSON with appropriate Content-Type header.
func RespondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

// RespondError writes a standard JSON error response with status code.
func RespondError(w http.ResponseWriter, status int, message string) {
	RespondJSON(w, status, ErrorResponse{
		Error:  message,
		Detail: message,
		Code:   status,
	})
}

// QueryString extracts a string parameter from query params or returns defaultVal if missing/empty.
func QueryString(r *http.Request, key string, defaultVal string) string {
	if r == nil || r.URL == nil {
		return defaultVal
	}
	val := r.URL.Query().Get(key)
	if val == "" {
		return defaultVal
	}
	return val
}

// QueryInt extracts an integer parameter from query params or returns defaultVal on error/missing.
func QueryInt(r *http.Request, key string, defaultVal int) int {
	if r == nil || r.URL == nil {
		return defaultVal
	}
	valStr := r.URL.Query().Get(key)
	if valStr == "" {
		return defaultVal
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return defaultVal
	}
	return val
}

// QueryInt64 extracts a 64-bit integer parameter from query params or returns defaultVal on error/missing.
func QueryInt64(r *http.Request, key string, defaultVal int64) int64 {
	if r == nil || r.URL == nil {
		return defaultVal
	}
	valStr := r.URL.Query().Get(key)
	if valStr == "" {
		return defaultVal
	}
	val, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return defaultVal
	}
	return val
}

// QueryBool extracts a boolean parameter from query params (e.g. "true", "1", "t").
func QueryBool(r *http.Request, key string, defaultVal bool) bool {
	if r == nil || r.URL == nil {
		return defaultVal
	}
	valStr := r.URL.Query().Get(key)
	if valStr == "" {
		return defaultVal
	}
	val, err := strconv.ParseBool(valStr)
	if err != nil {
		return defaultVal
	}
	return val
}
