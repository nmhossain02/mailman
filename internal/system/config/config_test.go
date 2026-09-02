package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsAndStrictFile(t *testing.T) {
	v, err := Load("")
	if err != nil || v.Core.Local.Backend != "ollama" || v.Core.Routing.Mode != "local_only" {
		t.Fatalf("defaults=%+v err=%v", v, err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(path, []byte(`{"Core":{"Routing":{"Mode":"local_only","Privacy":"local_only"}},"Accounts":[],"surprise":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(path); err == nil {
		t.Fatal("unknown field accepted")
	}
}
