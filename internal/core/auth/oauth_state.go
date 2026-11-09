package auth

import (
	"crypto/subtle"
	"net/http"
	"sync"
	"time"
)

var oauthStates = struct {
	sync.Mutex
	values map[string]time.Time
}{values: make(map[string]time.Time)}

// RegisterOAuthState expires pending authorizations and bounds memory use.
func RegisterOAuthState(state string) {
	oauthStates.Lock()
	defer oauthStates.Unlock()
	now := time.Now()
	for k, v := range oauthStates.values {
		if !now.Before(v) {
			delete(oauthStates.values, k)
		}
	}
	if len(oauthStates.values) >= 1024 {
		return
	}
	oauthStates.values[state] = now.Add(10 * time.Minute)
}

// ConsumeOAuthState binds the callback to its initiating browser and rejects replay.
func ConsumeOAuthState(w http.ResponseWriter, r *http.Request) bool {
	c, err := r.Cookie("mb_oauth_state")
	state := r.URL.Query().Get("state")
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		return false
	}
	oauthStates.Lock()
	expiry, ok := oauthStates.values[state]
	delete(oauthStates.values, state)
	oauthStates.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "mb_oauth_state", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil})
	return ok && time.Now().Before(expiry)
}
