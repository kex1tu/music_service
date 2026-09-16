package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathSandbox_Resolve(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "sandbox_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	sb, err := NewPathSandbox(tempDir)
	if err != nil {
		t.Fatalf("failed to init sandbox: %v", err)
	}

	// 1. Позитивный сценарий: вложенный файл
	res, err := sb.Resolve("album/01. Track.mp3", false)
	if err != nil {
		t.Fatalf("expected valid resolution, got: %v", err)
	}
	expected := filepath.Join(tempDir, "album/01. Track.mp3")
	if res != expected {
		t.Errorf("expected '%s', got '%s'", expected, res)
	}

	// 2. Позитивный сценарий: корень при allowRoot=true
	rootRes, err := sb.Resolve("", true)
	if err != nil || rootRes != tempDir {
		t.Errorf("expected root resolution, got: %v, %s", err, rootRes)
	}

	// 3. Негативный сценарий: корень при allowRoot=false
	_, err = sb.Resolve("", false)
	if err == nil {
		t.Errorf("expected error on root with allowRoot=false")
	}

	// 4. Защита от ../
	_, err = sb.Resolve("../../etc/passwd", false)
	if err == nil {
		t.Errorf("expected error on ../ traversal")
	}

	// 5. Защита от URL-encoding %2e%2e
	_, err = sb.Resolve("%2e%2e/%2e%2e/etc/passwd", false)
	if err == nil {
		t.Errorf("expected error on url encoded traversal")
	}

	// 6. Защита от double URL-encoding %252e%252e
	_, err = sb.Resolve("%252e%252e/etc/passwd", false)
	if err == nil {
		t.Errorf("expected error on double url encoded traversal")
	}

	// 7. Защита от нулевого байта
	_, err = sb.Resolve("test\x00.mp3", false)
	if err == nil {
		t.Errorf("expected error on null-byte")
	}

	// 8. Защита от обратных слэшей Windows
	winRes, err := sb.Resolve("folder\\subfolder\\track.mp3", false)
	if err != nil {
		t.Errorf("failed windows backslash resolution: %v", err)
	}
	expectedWin := filepath.Join(tempDir, "folder/subfolder/track.mp3")
	if winRes != expectedWin {
		t.Errorf("expected '%s', got '%s'", expectedWin, winRes)
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Normal Track", "Normal Track"},
		{"Track: Special? \"Chars\"", "Track_ Special_ _Chars_"},
		{"Artist 1\\\\Artist 2", "Artist 1 & Artist 2"},
		{"..", "Unknown"},
		{"", "Unknown"},
		{"   .Hidden.   ", "Hidden"},
	}

	for _, tc := range tests {
		got := SanitizeFilename(tc.input, "Unknown")
		if got != tc.expected {
			t.Errorf("SanitizeFilename('%s'): expected '%s', got '%s'", tc.input, tc.expected, got)
		}
	}
}

func TestPathSandbox_SymlinkEscape(t *testing.T) {
	sandboxDir, err := os.MkdirTemp("", "sandbox_symlink_root_*")
	if err != nil {
		t.Fatalf("failed to create sandbox temp dir: %v", err)
	}
	defer os.RemoveAll(sandboxDir)

	outsideDir, err := os.MkdirTemp("", "sandbox_symlink_outside_*")
	if err != nil {
		t.Fatalf("failed to create outside temp dir: %v", err)
	}
	defer os.RemoveAll(outsideDir)

	secretFile := filepath.Join(outsideDir, "secret.txt")
	_ = os.WriteFile(secretFile, []byte("sensitive info"), 0644)

	// Create symlink inside sandbox pointing to outsideDir
	symlinkPath := filepath.Join(sandboxDir, "escape_link")
	if err := os.Symlink(outsideDir, symlinkPath); err != nil {
		t.Skipf("skipping symlink test (symlink creation failed: %v)", err)
	}

	sb, err := NewPathSandbox(sandboxDir)
	if err != nil {
		t.Fatalf("failed to init sandbox: %v", err)
	}

	// 1. Attempt resolving the symlink itself
	_, err = sb.Resolve("escape_link", false)
	if err == nil {
		t.Errorf("expected error resolving symlink pointing outside sandbox, got nil")
	}

	// 2. Attempt resolving file through the symlink
	_, err = sb.Resolve("escape_link/secret.txt", false)
	if err == nil {
		t.Errorf("expected error resolving file via escaping symlink, got nil")
	}
}
