package filemanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/core/sandbox"
)

type failingJobCreator struct{}

func (failingJobCreator) CreateJob(context.Context, string, string) (string, error) {
	return "", errors.New("queue unavailable")
}

type fixtureFinder struct{}

func (fixtureFinder) FindTrackURL(context.Context, string, string, string, int) (string, error) {
	return "https://www.deezer.com/track/1", nil
}
func (fixtureFinder) FindAlbumURL(context.Context, string, string) (string, error) {
	return "https://www.deezer.com/album/1", nil
}
func TestRedownloadQueueFailurePreservesOriginal(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "track.mp3")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	sb, err := sandbox.NewPathSandbox(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(ServiceConfig{MusicSandbox: sb, JobCreator: failingJobCreator{}, SourceFinder: fixtureFinder{}})
	if _, err := svc.Redownload(context.Background(), RedownloadRequest{Path: "track.mp3", Type: "track", DeleteExisting: true}); err == nil {
		t.Fatal("expected queue failure")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "original" {
		t.Fatalf("lost original: %q %v", b, err)
	}
}
func TestDeleteAllRootAliasesDenied(t *testing.T) {
	root := t.TempDir()
	sb, err := sandbox.NewPathSandbox(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(ServiceConfig{MusicSandbox: sb})
	for _, path := range []string{".", "./", "a/..", "%2e", "%2e%2f", "/", ""} {
		if _, err := svc.DeleteFile(context.Background(), path); err == nil {
			t.Errorf("accepted root alias %q", path)
		}
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root removed: %v", err)
	}
}
