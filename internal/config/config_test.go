package config

import (
	"os"
	"strings"
	"testing"

	"music-manager/internal/core/auth"
)

func TestLoadConfig(t *testing.T) {
	hash, err := auth.HashPassword("test-only-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SECRET_KEY", strings.Repeat("x", 32))
	t.Setenv("ADMIN_PASSWORD_HASH", hash)
	t.Setenv("DATABASE_PATH", t.TempDir()+"/test.db")

	tempMusic, err := os.MkdirTemp("", "cfg_music_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempMusic)

	tempTmp, err := os.MkdirTemp("", "cfg_tmp_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempTmp)

	os.Setenv("MUSIC_DIR", tempMusic)
	os.Setenv("TMP_DIR", tempTmp)
	os.Setenv("PORT", "9999")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Port != 9999 {
		t.Errorf("expected port 9999, got %d", cfg.Port)
	}
	if cfg.MusicDir != tempMusic {
		t.Errorf("expected music dir '%s', got '%s'", tempMusic, cfg.MusicDir)
	}
}

func TestMissingSecretsFailBeforeStorageAccess(t *testing.T) {
	for _, pair := range [][2]string{{"", ""}, {strings.Repeat("x", 32), ""}, {"short", "invalid"}, {strings.Repeat("x", 32), "invalid"}} {
		t.Setenv("SECRET_KEY", pair[0])
		t.Setenv("ADMIN_PASSWORD_HASH", pair[1])
		t.Setenv("MUSIC_DIR", t.TempDir()+"/must-not-create")
		if _, err := loadEnvironment(); err == nil {
			t.Fatal("accepted missing or invalid secrets")
		}
		if _, err := os.Stat(os.Getenv("MUSIC_DIR")); !os.IsNotExist(err) {
			t.Fatal("touched storage before validating secrets")
		}
	}
}
