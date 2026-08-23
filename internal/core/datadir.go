package core

import (
	"fmt"
	"os"
	"path/filepath"
)

const DataDirEnvironment = "MAILMAN_DATA_DIR"

func DefaultDataDir() (string, error) {
	if override := os.Getenv(DataDirEnvironment); override != "" {
		absolute, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", DataDirEnvironment, err)
		}
		return filepath.Clean(absolute), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(base, "mailman"), nil
}
