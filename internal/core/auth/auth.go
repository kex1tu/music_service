package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// CookieName is the HTTP cookie key for session authorization.
const CookieName = "session_token"

type Policy struct {
	Name   string
	TTL    int
	Secure bool
}

func policy(ps []Policy) Policy {
	p := Policy{Name: CookieName, TTL: 7 * 24 * 3600}
	if len(ps) > 0 {
		p = ps[0]
		if p.Name == "" {
			p.Name = CookieName
		}
		if p.TTL <= 0 {
			p.TTL = 7 * 24 * 3600
		}
	}
	return p
}

// HashPassword generates a bcrypt hash of a password.
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

// CheckPasswordHash verifies a plain password against a bcrypt hash.
func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// CreateSessionToken creates an HMAC-signed session string: login:timestamp:signature
func CreateSessionToken(login, secretKey string) string {
	cleanLogin := strings.ReplaceAll(login, ":", "_")
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	payload := cleanLogin + ":" + ts
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return payload + ":" + sig
}

// ValidateSessionToken verifies HMAC signature and expiration (7 days).
func ValidateSessionToken(token, secretKey string, ps ...Policy) (string, bool) {
	p := policy(ps)
	parts := strings.Split(token, ":")
	if len(parts) != 3 {
		return "", false
	}
	login := parts[0]
	tsStr := parts[1]
	sig := parts[2]

	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return "", false
	}

	now := time.Now().Unix()
	// Disallow timestamps more than 5 minutes in the future, and expired after 7 days
	if ts > now+300 || ts < now-int64(p.TTL) {
		return "", false
	}

	payload := login + ":" + tsStr
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return "", false
	}

	return login, true
}

// SetSessionCookie sets an HTTP-only SameSite=Strict session cookie.
func SetSessionCookie(w http.ResponseWriter, token string, ps ...Policy) {
	p := policy(ps)
	http.SetCookie(w, &http.Cookie{
		Name:     p.Name,
		Secure:   p.Secure,
		Value:    token,
		Path:     "/",
		MaxAge:   p.TTL,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearSessionCookie deletes the session cookie.
func ClearSessionCookie(w http.ResponseWriter, ps ...Policy) {
	p := policy(ps)
	http.SetCookie(w, &http.Cookie{
		Name:     p.Name,
		Secure:   p.Secure,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// Middleware verifies authentication for protected routes.
func Middleware(secretKey string, ps ...Policy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ""
			if cookie, err := r.Cookie(policy(ps).Name); err == nil && cookie.Value != "" {
				token = cookie.Value
			} else if qToken := r.URL.Query().Get("token"); qToken != "" {
				token = qToken
			} else if authHdr := r.Header.Get("Authorization"); strings.HasPrefix(authHdr, "Bearer ") {
				token = strings.TrimPrefix(authHdr, "Bearer ")
			}

			if token == "" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			_, valid := ValidateSessionToken(token, secretKey, ps...)
			if !valid {
				http.Error(w, `{"error":"invalid session"}`, http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
