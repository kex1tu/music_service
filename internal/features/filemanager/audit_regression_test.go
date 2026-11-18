package filemanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/services/scanner"
	"music-manager/internal/testkit"
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

func TestDeleteDirectoryUsesLiteralPathAcrossDiskAndIndex(t *testing.T) {
	env := testkit.NewEnv(t)
	ctx := context.Background()
	index := scanner.NewScanner(env.MusicSandbox, env.DB, nil)
	svc := NewService(ServiceConfig{
		MusicSandbox: env.MusicSandbox,
		Repo:         NewRepository(env.DB.Conn()),
		LibraryIndex: index,
	})

	for _, dir := range []string{"A_B", "AxB", "a_b", "A%20B", "A B"} {
		path := filepath.Join(dir, "track.mp3")
		full, err := env.MusicSandbox.Resolve(path, false, sandbox.LiteralPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("audio fixture"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := index.ScanAll(ctx); count != 5 || err != nil {
		t.Fatalf("initial scan: count=%d err=%v", count, err)
	}
	for _, dir := range []string{"A_B", "A%20B"} {
		result, err := svc.DeleteFile(ctx, dir)
		if err != nil || result.DeletedTracks != 1 {
			t.Fatalf("delete %s: %+v, %v", dir, result, err)
		}
	}
	for _, dir := range []string{"AxB", "a_b", "A B"} {
		path := filepath.Join(dir, "track.mp3")
		if _, err := os.Stat(filepath.Join(env.MusicSandbox.Root(), path)); err != nil {
			t.Fatalf("%s was removed from disk: %v", path, err)
		}
		var count int
		if err := env.DB.Conn().QueryRow("SELECT count(*) FROM tracks WHERE file_path = ?", path).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s was removed from the index: count=%d err=%v", path, count, err)
		}
	}
}

func TestTrackIDFollowsAnIndexedMoveAfterOldPathIsReused(t *testing.T) {
	env := testkit.NewEnv(t)
	ctx := context.Background()
	index := scanner.NewScanner(env.MusicSandbox, env.DB, nil)
	svc := NewService(ServiceConfig{
		MusicSandbox: env.MusicSandbox,
		Repo:         NewRepository(env.DB.Conn()),
	})
	const source, target = "Album/original.mp3", "Album/renamed.mp3"
	fullSource := env.CreateDummyMP3(source, "Original", "Artist", "Album")
	if err := index.IndexFile(fullSource, source); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := env.DB.Conn().QueryRow("SELECT id FROM tracks WHERE file_path = ?", source).Scan(&id); err != nil {
		t.Fatal(err)
	}
	svc.trackCache.Store(id, source)
	fullTarget, err := env.MusicSandbox.Resolve(target, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.ChangeTrack(ctx, fullSource, fullTarget, false, func() error {
		return os.Rename(fullSource, fullTarget)
	}); err != nil {
		t.Fatal(err)
	}
	env.CreateDummyMP3(source, "Another track", "Artist", "Album")

	_, path, _, _, err := svc.ResolveTrack(ctx, id)
	if err != nil || path != target {
		t.Fatalf("track ID resolved to %q, expected %q: %v", path, target, err)
	}
}
