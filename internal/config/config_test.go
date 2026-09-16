package config

import (
	"os"
	"testing"
)

func TestLoadConfig(t *testing.T) {
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
