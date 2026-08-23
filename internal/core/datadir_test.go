package core

import (
	"path/filepath"
	"testing"
)

func TestDefaultDataDirUsesOverride(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "portable")
	t.Setenv(DataDirEnvironment, dir)
	got, err := DefaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("DefaultDataDir() = %q, want %q", got, dir)
	}
}
