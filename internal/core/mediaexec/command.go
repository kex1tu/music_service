package mediaexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
)

// Cmd owns the descriptor arguments inherited by a media tool. Closing a Cmd
// releases parent handles; it does not terminate a running process.
type Cmd struct {
	*exec.Cmd
	files []*os.File
}

// CommandContext pins media inputs and staging output locations before exec.
// Tools see inherited descriptors instead of reopening a validated pathname.
func CommandContext(ctx context.Context, name string, args ...string) *Cmd {
	args = append([]string(nil), args...)
	c := &Cmd{Cmd: exec.CommandContext(ctx, name, args...)}
	if c.Err != nil {
		return c
	}
	if err := ctx.Err(); err != nil {
		c.Err = err
		return c
	}
	if filepath.Base(name) == "yt-dlp" {
		if err := ValidateRuntime(ctx); err != nil {
			c.Err = err
			return c
		}
	}
	for i, arg := range args {
		if !filepath.IsAbs(arg) {
			continue
		}
		root, _, err := sandbox.OperationRoot(arg)
		if err != nil {
			c.Err = err
			c.Close()
			return c
		}
		if root == nil {
			continue
		}
		var file *os.File
		if filepath.Base(name) == "yt-dlp" && i > 0 && (args[i-1] == "-o" || args[i-1] == "--output") {
			// Staging directories are private to the service; pin the directory so a
			// concurrent rename of its ancestors cannot redirect tool-created files.
			file, err = safefs.Open(filepath.Dir(arg))
		} else if filepath.Base(name) == "ffmpeg" && i == len(args)-1 {
			file, err = safefs.OpenFile(arg, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
		} else {
			file, err = safefs.Open(arg)
		}
		if err != nil {
			c.Err = err
			c.Close()
			return c
		}
		fd := fmt.Sprintf("/proc/self/fd/%d", 3+len(c.files))
		c.files = append(c.files, file)
		if filepath.Base(name) == "yt-dlp" && i > 0 && (args[i-1] == "-o" || args[i-1] == "--output") {
			// Go changes directory before remapping/closing inherited descriptors.
			// Pin cwd using the parent's handle, then use a relative output name:
			// yt-dlp's ffmpeg subprocesses close extra FDs, but inherit cwd.
			c.Dir = fmt.Sprintf("/proc/self/fd/%d", file.Fd())
			args[i] = "./" + filepath.Base(arg)
			continue
		}
		args[i] = fd
		if filepath.Base(name) == "ffmpeg" && i == len(args)-1 && strings.EqualFold(filepath.Ext(arg), ".mp3") {
			// The descriptor name has no extension from which ffmpeg can infer format.
			args = append(args[:i], "-f", "mp3", fd)
			break
		}
	}
	c.Args = append([]string{c.Path}, args...)
	c.ExtraFiles = c.files
	return c
}
func (c *Cmd) Close() {
	for _, f := range c.files {
		_ = f.Close()
	}
	c.files = nil
}
func (c *Cmd) Start() error {
	err := c.Cmd.Start()
	if err != nil {
		c.Close()
	}
	return err
}
func (c *Cmd) Wait() error                     { defer c.Close(); return c.Cmd.Wait() }
func (c *Cmd) Run() error                      { defer c.Close(); return c.Cmd.Run() }
func (c *Cmd) Output() ([]byte, error)         { defer c.Close(); return c.Cmd.Output() }
func (c *Cmd) CombinedOutput() ([]byte, error) { defer c.Close(); return c.Cmd.CombinedOutput() }
