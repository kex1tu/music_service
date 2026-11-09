package safefs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"music-manager/internal/core/sandbox"
)

func TestOperationsRejectSwapAfterResolve(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "secret")
	if err := os.WriteFile(victim, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	sb, err := sandbox.NewPathSandbox(root)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.KeepAlive(sb)
	checked, err := sb.Resolve("dir/secret", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "dir"), filepath.Join(root, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(checked); err == nil {
		t.Fatal("escaped read")
	}
	if err := WriteFile(checked, []byte("overwrite"), 0600); err == nil {
		t.Fatal("escaped write")
	}
	if err := Remove(checked); err == nil {
		t.Fatal("escaped remove")
	}
	b, err := os.ReadFile(victim)
	if err != nil || string(b) != "preserve" {
		t.Fatalf("outside changed: %q %v", b, err)
	}
}

func TestRootDescriptorSurvivesRename(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	sb, err := sandbox.NewPathSandbox(root)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.KeepAlive(sb)
	checked, err := sb.Resolve("track", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(checked, []byte("pinned"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root+"-old", "track")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(checked); !os.IsNotExist(err) {
		t.Fatal("wrote replacement root")
	}
}
