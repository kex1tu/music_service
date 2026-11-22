package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"music-manager/internal/core/safefs"
)

// CheckDeployDatabase validates the configured database before the server can
// migrate it. The deployment script publishes the guard next to the binary.
func CheckDeployDatabase(configured string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return checkDeployDatabase(executable+".deploy-db", configured)
}

func checkDeployDatabase(guard, configured string) error {
	raw, err := safefs.ReadFile(guard)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	expected, ok := strings.CutSuffix(string(raw), "\x00")
	if !ok || !filepath.IsAbs(expected) {
		return errors.New("invalid deployment database guard")
	}
	configured, err = filepath.Abs(configured)
	if err != nil {
		return err
	}
	configured, err = filepath.EvalSymlinks(configured)
	if err != nil {
		return err
	}
	if configured != expected {
		return fmt.Errorf("configured database %q differs from the deployment snapshot at %q", configured, expected)
	}
	return nil
}
