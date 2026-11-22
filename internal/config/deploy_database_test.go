package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckDeployDatabase(t *testing.T) {
	dir := t.TempDir()
	guard := filepath.Join(dir, "music-manager.deploy-db")
	snapshotted := filepath.Join(dir, "current.db")
	next := filepath.Join(dir, "next.db")
	for _, path := range []string{snapshotted, next} {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkDeployDatabase(guard, next); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guard, []byte(snapshotted+"\x00"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkDeployDatabase(guard, snapshotted); err != nil {
		t.Fatal(err)
	}
	if err := checkDeployDatabase(guard, next); err == nil {
		t.Fatal("accepted a database outside the deployment snapshot")
	}
	link := filepath.Join(dir, "alias.db")
	if err := os.Symlink(snapshotted, link); err != nil {
		t.Fatal(err)
	}
	if err := checkDeployDatabase(guard, link); err != nil {
		t.Fatal(err)
	}
}
