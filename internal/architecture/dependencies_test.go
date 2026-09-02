package architecture

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const module = "github.com/nmhossain02/mailman/"

type listedPackage struct {
	ImportPath string
	Imports    []string
}

func TestInternalDependencyBoundaries(t *testing.T) {
	command := exec.Command("go", "list", "-json", "./cmd/...", "./internal/...")
	command.Dir = filepath.Join("..", "..")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list packages: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	for decoder.More() {
		var pkg listedPackage
		if err = decoder.Decode(&pkg); err != nil {
			t.Fatalf("decode package list: %v", err)
		}
		for _, imported := range pkg.Imports {
			if strings.HasPrefix(imported, module) && !allowed(pkg.ImportPath, imported) {
				t.Errorf("architecture violation: %s imports %s", pkg.ImportPath, imported)
			}
		}
	}
}

func allowed(importer, imported string) bool {
	from := strings.TrimPrefix(importer, module)
	to := strings.TrimPrefix(imported, module)
	if strings.HasPrefix(from, "internal/bootstrap") {
		return true
	}
	if from == "cmd/mailman" {
		return strings.HasPrefix(to, "internal/bootstrap")
	}
	switch {
	case strings.HasPrefix(from, "internal/domain"):
		return false
	case strings.HasPrefix(from, "internal/application"):
		return hasLayer(to, "application") || hasLayer(to, "automation") || hasLayer(to, "domain")
	case strings.HasPrefix(from, "internal/agent"):
		return hasLayer(to, "agent") || hasLayer(to, "domain")
	case strings.HasPrefix(from, "internal/automation"):
		return hasLayer(to, "domain")
	case strings.HasPrefix(from, "internal/tui"):
		return hasLayer(to, "application") || hasLayer(to, "domain")
	case strings.HasPrefix(from, "internal/cli"):
		return false
	case strings.HasPrefix(from, "internal/adapters/google"), strings.HasPrefix(from, "internal/adapters/outlook"):
		return hasLayer(to, "application") || hasLayer(to, "domain") || strings.HasPrefix(to, "internal/adapters/keyring")
	case strings.HasPrefix(from, "internal/adapters/sqlite"):
		return hasLayer(to, "application") || hasLayer(to, "agent") || hasLayer(to, "domain")
	case strings.HasPrefix(from, "internal/adapters/keyring"):
		return false
	case strings.HasPrefix(from, "internal/system/config"):
		return hasLayer(to, "domain")
	case strings.HasPrefix(from, "internal/system/health"):
		return hasLayer(to, "application") || hasLayer(to, "agent") || strings.HasPrefix(to, "internal/adapters/keyring")
	case strings.HasPrefix(from, "internal/system/install"), strings.HasPrefix(from, "internal/architecture"):
		return false
	default:
		return false
	}
}

func hasLayer(path, layer string) bool {
	prefix := "internal/" + layer
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}
