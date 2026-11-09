package mediaexec

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"music-manager/internal/core/sandbox"
)

// The helper acts like yt-dlp: it writes the output itself, then starts a
// postprocessor which does not inherit ExtraFiles (as with ffmpeg).
func TestYTDLPPostprocessHelper(t *testing.T) {
	if os.Getenv("MUSIC_MEDIAEXEC_HELPER") != "1" {
		return
	}
	var output string
	for i, arg := range os.Args {
		if arg == "-o" || arg == "--output" {
			output = os.Args[i+1]
		}
		if arg == "--cookies" {
			data, err := os.ReadFile(os.Args[i+1])
			if err != nil || string(data) != "cookie-fixture" {
				t.Fatalf("lost cookie descriptor: %q %v", data, err)
			}
		}
	}
	if err := os.WriteFile(output, []byte("audio-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := exec.Command("sh", "-c", `exec 3<&-; exec cat "$1"`, "postprocessor", output).CombinedOutput()
	if err != nil || !bytes.Equal(b, []byte("audio-fixture")) {
		t.Fatalf("postprocessor lost output: %s %v", b, err)
	}
}

func TestYTDLPPostprocessorSurvivesDirectorySwap(t *testing.T) {
	root := t.TempDir()
	tools := t.TempDir()
	outside := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(executable, filepath.Join(tools, "yt-dlp")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(tools, "deno"), []byte("#!/bin/sh\nprintf 'deno 2.9.8\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MUSIC_MEDIAEXEC_HELPER", "1")
	sb, err := sandbox.NewPathSandbox(root)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.KeepAlive(sb)
	cookies, err := sb.Resolve("cookies.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cookies, []byte("cookie-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"-o", "--output"} {
		t.Run(flag, func(t *testing.T) {
			staging := "staging-" + strings.TrimLeft(flag, "-")
			dir := filepath.Join(root, staging)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			output, err := sb.Resolve(staging+"/track.m4a", false)
			if err != nil {
				t.Fatal(err)
			}
			cmd := CommandContext(context.Background(), "yt-dlp", "-test.run=^TestYTDLPPostprocessHelper$", "--", flag, output, "--cookies", cookies)
			defer cmd.Close()
			pinned := dir + "-" + strings.TrimLeft(flag, "-")
			if err := os.Rename(dir, pinned); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, dir); err != nil {
				t.Fatal(err)
			}
			runtime.GC()
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("yt-dlp/postprocessor: %v %s", err, out)
			}
			if _, err := os.Stat(filepath.Join(outside, "track.m4a")); !os.IsNotExist(err) {
				t.Fatal("output escaped staging directory")
			}
			if err := os.Remove(dir); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInputDescriptorSurvivesDirectorySwap(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	dir := filepath.Join(root, "album")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "track"), []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "track"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	sb, err := sandbox.NewPathSandbox(root)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.KeepAlive(sb)
	cmd := CommandContext(context.Background(), "cat", filepath.Join(dir, "track"))
	defer cmd.Close()
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	b, err := cmd.Output()
	if err != nil || string(b) != "inside" {
		t.Fatalf("uncontained input: %q %v", b, err)
	}
}

func TestRegisteredMP3OutputUsesPinnedDescriptor(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	sb, err := sandbox.NewPathSandbox(root)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.KeepAlive(sb)
	dst, err := sb.Resolve("output.mp3", false)
	if err != nil {
		t.Fatal(err)
	}
	cmd := CommandContext(context.Background(), "ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=1000:duration=1", "-codec:a", "libmp3lame", "-b:a", "320k", "-ar", "44100", dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	probe := CommandContext(context.Background(), "ffprobe", "-v", "error", "-show_entries", "stream=bit_rate,sample_rate", "-of", "default=noprint_wrappers=1", dst)
	out, err = probe.Output()
	if err != nil || !strings.Contains(string(out), "bit_rate=320000") || !strings.Contains(string(out), "sample_rate=44100") {
		t.Fatalf("bad output: %s %v", out, err)
	}
}

func TestPythonLauncherRejectedBeforeExecution(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "yt-dlp")
	marker := filepath.Join(dir, "executed")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	cmd := CommandContext(context.Background(), "yt-dlp", "--version")
	if err := cmd.Run(); err == nil {
		t.Fatal("accepted launcher")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("launcher executed")
	}
}
