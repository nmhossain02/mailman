package install

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallAndUninstall(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	destination := filepath.Join(dir, "bin", "mailman")
	if err := os.WriteFile(source, []byte("mailman executable"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Install(source, destination); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "mailman executable" {
		t.Fatalf("installed contents = %q", contents)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("installed permissions = %o, want 755", info.Mode().Perm())
	}
	if err = Uninstall(destination); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination still exists: %v", err)
	}
}

func TestInstallReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	destination := filepath.Join(dir, "mailman")
	if err := os.WriteFile(source, []byte("new"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Install(source, destination); err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(destination)
	if string(contents) != "new" {
		t.Fatalf("installed contents = %q", contents)
	}
}

func TestUninstallMissingIsIdentifiable(t *testing.T) {
	if err := Uninstall(filepath.Join(t.TempDir(), "mailman")); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Uninstall error = %v, want ErrNotInstalled", err)
	}
}

func TestDirectoryInPath(t *testing.T) {
	destination := filepath.Join("", "users", "me", ".local", "bin", "mailman")
	pathValue := filepath.Join("", "usr", "bin") + string(os.PathListSeparator) + filepath.Dir(destination)
	if !DirectoryInPath(destination, pathValue) {
		t.Fatal("expected destination directory in PATH")
	}
}
