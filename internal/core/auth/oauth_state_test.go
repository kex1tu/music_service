package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOAuthStateSingleUseAndBinding(t *testing.T) {
	RegisterOAuthState("once")
	for _, v := range []struct {
		state, cookie string
		want          bool
	}{{"once", "wrong", false}, {"", "once", false}, {"once", "once", true}, {"once", "once", false}} {
		r := httptest.NewRequest("GET", "https://example.test/callback?state="+v.state, nil)
		r.AddCookie(&http.Cookie{Name: "mb_oauth_state", Value: v.cookie})
		if got := ConsumeOAuthState(httptest.NewRecorder(), r); got != v.want {
			t.Fatalf("state=%q cookie=%q got %v", v.state, v.cookie, got)
		}
	}
	oauthStates.Lock()
	oauthStates.values["expired"] = time.Now().Add(-time.Second)
	oauthStates.Unlock()
	r := httptest.NewRequest("GET", "https://example.test/?state=expired", nil)
	r.AddCookie(&http.Cookie{Name: "mb_oauth_state", Value: "expired"})
	if ConsumeOAuthState(httptest.NewRecorder(), r) {
		t.Fatal("accepted expired state")
	}
}
func TestConfiguredCookiePolicy(t *testing.T) {
	w := httptest.NewRecorder()
	p := Policy{Name: "custom", TTL: 120, Secure: true}
	SetSessionCookie(w, "test", p)
	c := w.Result().Cookies()[0]
	if c.Name != "custom" || c.MaxAge != 120 || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("bad cookie: %+v", c)
	}
}
