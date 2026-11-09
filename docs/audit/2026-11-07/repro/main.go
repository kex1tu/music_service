// Audit reproductions. Uses temporary fixtures only; no external requests.
package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"music-manager/internal/core/auth"
	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	fm "music-manager/internal/features/filemanager"
	"music-manager/internal/services/organizer"
)

type fakeSource struct{}

func (fakeSource) FindTrackURL(context.Context, string, string, string, int) (string, error) {
	return "https://www.deezer.com/track/1", nil
}
func (fakeSource) FindAlbumURL(context.Context, string, string) (string, error) {
	return "https://www.deezer.com/album/1", nil
}

type failedQueue struct{}

func (failedQueue) CreateJob(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("injected queue failure")
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	root, err := os.MkdirTemp("", "music-audit-repro-")
	must(err)
	defer os.RemoveAll(root)
	music := filepath.Join(root, "music")
	must(os.MkdirAll(music, 0700))
	sb, err := sandbox.NewPathSandbox(music)
	must(err)
	p, err := sb.Resolve("track.mp3", false)
	must(err)
	must(os.WriteFile(p, []byte("original"), 0600))
	svc := fm.NewService(fm.ServiceConfig{MusicSandbox: sb, SourceFinder: fakeSource{}, JobCreator: failedQueue{}})
	_, err = svc.Redownload(context.Background(), fm.RedownloadRequest{Path: "track.mp3", Type: "track", DeleteExisting: true})
	_, statErr := os.Stat(p)
	fmt.Printf("redownload: queue_error=%v original_missing=%v\n", err, os.IsNotExist(statErr))
	must(os.WriteFile(p, []byte("root-marker"), 0600))
	_, err = svc.DeleteFile(context.Background(), "./")
	_, statErr = os.Stat(music)
	fmt.Printf("delete_root_alias: error=%v root_missing=%v\n", err, os.IsNotExist(statErr))
	must(os.MkdirAll(filepath.Join(music, "dir"), 0700))
	outside := filepath.Join(root, "outside")
	must(os.MkdirAll(outside, 0700))
	must(os.WriteFile(filepath.Join(outside, "secret"), []byte("outside-fixture"), 0600))
	checked, err := sb.Resolve("dir/secret", false)
	must(err)
	must(os.Rename(filepath.Join(music, "dir"), filepath.Join(music, "old")))
	must(os.Symlink(outside, filepath.Join(music, "dir")))
	data, err := safefs.ReadFile(checked)
	fmt.Printf("resolve_then_swap: outside_read=%v error=%v\n", string(data) == "outside-fixture", err)
	src := filepath.Join(root, "src.mp3")
	dst := filepath.Join(root, "dst.mp3")
	must(os.WriteFile(src, []byte("same-audio"), 0600))
	must(os.WriteFile(dst, []byte("same-audio"), 0600))
	must(os.WriteFile(filepath.Join(root, "src.lrc"), []byte("unique-new-lyrics"), 0600))
	must(os.WriteFile(filepath.Join(root, "dst.lrc"), []byte("old-lyrics"), 0600))
	actual, err := organizer.MoveTrackWithCompanions(src, dst, false)
	must(err)
	_, statErr = os.Stat(filepath.Join(root, "src.lrc"))
	data, err = os.ReadFile(filepath.Join(root, "dst.lrc"))
	must(err)
	newLyrics, readErr := os.ReadFile(strings.TrimSuffix(actual, ".mp3") + ".lrc")
	must(readErr)
	fmt.Printf("companion_collision: source_missing=%v destination=%q retained_lyrics=%q\n", os.IsNotExist(statErr), data, newLyrics)
	w := httptest.NewRecorder()
	auth.SetSessionCookie(w, "fixture-token", auth.Policy{Secure: true, TTL: 120, Name: "configured"})
	fmt.Printf("session_cookie: %s\n", w.Header().Get("Set-Cookie"))
}
