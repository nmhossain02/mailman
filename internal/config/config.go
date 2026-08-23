package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nmhossain02/mailman/internal/core"
)

type Account struct {
	ID, Name, Provider, TokenKey        string
	Enabled                             bool
	Integrations                        []string
	TaskListID, CalendarID, RedirectURL string
}

type File struct {
	Core     core.Config
	Accounts []Account
}

func Defaults() File {
	return File{Core: core.Config{
		Local:    core.ModelConfig{Backend: "ollama", BaseURL: "http://127.0.0.1:11434", Model: "qwen3:8b", Enabled: true, HealthTimeoutSeconds: 2, InteractiveTimeoutSeconds: 30, BackgroundTimeoutSeconds: 120},
		External: core.ModelConfig{Backend: "openai", BaseURL: "https://api.openai.com", Model: "gpt-5-mini", Enabled: false, HealthTimeoutSeconds: 2, ExternalTimeoutSeconds: 90},
		Routing:  core.RoutePolicy{Mode: "local_only", Privacy: "local_only"},
	}}
}

func Load(path string) (File, error) {
	v := Defaults()
	if strings.TrimSpace(path) == "" {
		return v, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return File{}, fmt.Errorf("read config: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&v); err != nil {
		return File{}, fmt.Errorf("decode config: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) == nil {
		return File{}, errors.New("decode config: trailing JSON")
	}
	if v.Core.DataDir != "" {
		v.Core.DataDir = filepath.Clean(v.Core.DataDir)
	}
	if err = v.Core.Routing.Validate(); err != nil {
		return File{}, fmt.Errorf("routing: %w", err)
	}
	seen := map[string]bool{}
	for _, a := range v.Accounts {
		if a.ID == "" || a.Provider == "" || a.TokenKey == "" {
			return File{}, errors.New("each account requires id, provider, and token key")
		}
		if seen[a.ID] {
			return File{}, fmt.Errorf("duplicate account %q", a.ID)
		}
		seen[a.ID] = true
		switch a.Provider {
		case "gmail", "outlook":
		default:
			return File{}, fmt.Errorf("account %q has unsupported provider %q", a.ID, a.Provider)
		}
	}
	return v, nil
}
