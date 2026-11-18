package tageditor_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"music-manager/internal/features/tageditor"
	"music-manager/internal/services/resolver"
	"music-manager/internal/testkit"
)

func TestMusicBrainzOAuthAtomicPersistence(t *testing.T) {
	env := testkit.NewEnv(t)
	conn := env.DB.Conn()
	repo := tageditor.NewRepository(conn)
	ctx := t.Context()
	expiry := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	if err := repo.SaveMusicBrainzTokens(ctx, "old-access", "old-refresh", expiry, "old-user"); err != nil {
		t.Fatal(err)
	}
	checkPrevious := func() {
		t.Helper()
		for _, prefix := range []string{"mb_", "musicbrainz_"} {
			for key, want := range map[string]string{
				"access_token": "old-access", "refresh_token": "old-refresh",
				"username": "old-user", "expires_at": expiry.Format(time.RFC3339),
			} {
				got, err := repo.GetSetting(ctx, prefix+key)
				if err != nil || got != want {
					t.Fatalf("previous setting %s%s was changed", prefix, key)
				}
			}
		}
	}
	if _, err := conn.ExecContext(ctx, `
		CREATE TRIGGER reject_refresh BEFORE INSERT ON app_settings
		WHEN NEW.key='mb_refresh_token'
		BEGIN SELECT RAISE(FAIL, 'test refresh write rejected'); END;
		CREATE TRIGGER reject_disconnect BEFORE DELETE ON app_settings
		WHEN OLD.key='musicbrainz_username'
		BEGIN SELECT RAISE(FAIL, 'test disconnect rejected'); END;`); err != nil {
		t.Fatal(err)
	}

	// Use a local token endpoint and verify both the callback service result
	// and the current client session after a failed transactional update.
	const provider = "https://musicbrainz.invalid"
	env.MockHTTP.RegisterFixture(provider+"/token", http.StatusOK, "", []byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`))
	env.MockHTTP.RegisterFixture(provider+"/userinfo", http.StatusOK, "", []byte(`{"sub":"new-user"}`))

	client := resolver.NewMusicBrainzClient("")
	client.SetOAuthConfig("fixture", "fixture", "http://localhost/callback")
	client.SetOAuthBaseURL(provider)
	client.SetTokens("old-access", "old-refresh", expiry, "old-user")
	service := tageditor.NewService(tageditor.ServiceConfig{MusicBrainz: client, TokenStore: repo})
	if _, err := service.HandleMusicBrainzCallback(ctx, "fixture", ""); err == nil || !strings.Contains(err.Error(), "test refresh write rejected") {
		t.Fatalf("callback did not return the expected storage error: %v", err)
	}
	checkPrevious()
	if status := service.GetMusicBrainzStatus(); !status.Connected || status.Username != "old-user" {
		t.Fatalf("callback replaced live OAuth session: %+v", status)
	}

	module := tageditor.NewModule(service)
	disconnect := func() int {
		rec := httptest.NewRecorder()
		module.MusicBrainzDisconnect(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/auth/musicbrainz/disconnect", nil))
		return rec.Code
	}
	if code := disconnect(); code != http.StatusInternalServerError {
		t.Fatalf("disconnect returned HTTP %d after a failed delete", code)
	}
	checkPrevious()
	if !service.GetMusicBrainzStatus().Connected {
		t.Fatal("failed disconnect changed the live OAuth session")
	}

	if _, err := conn.ExecContext(ctx, "DROP TRIGGER reject_disconnect"); err != nil {
		t.Fatal(err)
	}
	if code := disconnect(); code != http.StatusOK {
		t.Fatalf("successful disconnect returned HTTP %d", code)
	}
	newRepo := tageditor.NewRepository(conn)
	access, refresh, user, expiration, err := newRepo.LoadMusicBrainzTokens(ctx)
	if err != nil || access != "" || refresh != "" || user != "" || !expiration.IsZero() {
		t.Fatal("a new repository found old authorization after disconnect")
	}
	var keyCount int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM app_settings WHERE key GLOB 'mb_*' OR key GLOB 'musicbrainz_*'").Scan(&keyCount); err != nil || keyCount != 0 {
		t.Fatalf("OAuth keys remaining after disconnect: %d, %v", keyCount, err)
	}
}
