package mediaexec

import (
	"context"
	"debug/elf"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ValidateRuntime rejects Python launcher scripts and requires the supported
// non-Node JavaScript runtime used by the standalone yt-dlp release.
func ValidateRuntime(parents ...context.Context) error {
	parent := context.Background()
	if len(parents) > 0 {
		parent = parents[0]
	}
	if err := parent.Err(); err != nil {
		return err
	}
	path, err := exec.LookPath("yt-dlp")
	if err != nil {
		return fmt.Errorf("standalone yt-dlp required: %w", err)
	}
	file, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("yt-dlp must be the standalone Linux ELF release, not a Python launcher: %w", err)
	}
	file.Close()
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "deno", "--version").Output()
	if err != nil {
		return fmt.Errorf("Deno >=2.3 required for YouTube EJS: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != "deno" {
		return fmt.Errorf("invalid Deno version response")
	}
	parts := strings.Split(fields[1], ".")
	if len(parts) < 2 {
		return fmt.Errorf("invalid Deno version")
	}
	major, e1 := strconv.Atoi(parts[0])
	minor, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || major < 2 || (major == 2 && minor < 3) {
		return fmt.Errorf("Deno >=2.3 required")
	}
	return nil
}
