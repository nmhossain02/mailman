package install

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var ErrNotInstalled = errors.New("mailman is not installed locally")

func Destination() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "bin", "mailman"), nil
}

func Install(source, destination string) (err error) {
	sourceFile, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open running executable: %w", err)
	}
	defer sourceFile.Close()

	info, err := sourceFile.Stat()
	if err != nil {
		return fmt.Errorf("inspect running executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("running executable is not a regular file")
	}

	dir := filepath.Dir(destination)
	if err = os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create local bin directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".mailman-install-*")
	if err != nil {
		return fmt.Errorf("create temporary executable: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()

	if _, err = io.Copy(temporary, sourceFile); err != nil {
		return fmt.Errorf("copy executable: %w", err)
	}
	if err = temporary.Chmod(0755); err != nil {
		return fmt.Errorf("set executable permissions: %w", err)
	}
	if err = temporary.Sync(); err != nil {
		return fmt.Errorf("sync executable: %w", err)
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("close executable: %w", err)
	}
	if err = os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("install executable: %w", err)
	}
	return nil
}

func Uninstall(destination string) error {
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotInstalled
	}
	if err != nil {
		return fmt.Errorf("inspect local installation: %w", err)
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("refusing to remove non-file installation at %s", destination)
	}
	if err = os.Remove(destination); err != nil {
		return fmt.Errorf("remove local installation: %w", err)
	}
	return nil
}

func DirectoryInPath(destination, pathValue string) bool {
	wanted := filepath.Clean(filepath.Dir(destination))
	for _, entry := range filepath.SplitList(pathValue) {
		if entry != "" && filepath.Clean(entry) == wanted {
			return true
		}
	}
	return false
}
