package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupGmailCreatesPrivateCompleteConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "mailman")
	input := strings.NewReader("\n\n\nclient-id\nclient-secret\n\n\n")
	var output bytes.Buffer
	account, err := Setup(input, &output, dir)
	if err != nil {
		t.Fatal(err)
	}
	if account.ID != "personal" || account.Provider != "gmail" || account.TokenKey != "google.personal" {
		t.Fatalf("unexpected account: %+v", account)
	}
	path := filepath.Join(dir, "config.json")
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0700 {
		t.Fatalf("application directory permissions = %o, want 700", dirInfo.Mode().Perm())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Core.Google.ClientID != "client-id" || got.Core.Google.ClientSecret != "client-secret" || !got.Accounts[0].Enabled {
		t.Fatalf("unexpected config: %+v", got)
	}
	if !strings.Contains(output.String(), path) {
		t.Fatalf("output does not identify saved config: %q", output.String())
	}
}

func TestSetupCanEnableGoogleIntegrationsWithoutEditingJSON(t *testing.T) {
	dir := t.TempDir()
	account, err := Setup(strings.NewReader("gmail\nmain\nMy Mail\nclient-id\nclient-secret\nyes\ny\n"), &bytes.Buffer{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(account.Integrations, ",") != "google_tasks,google_calendar" || account.TaskListID != "@default" || account.CalendarID != "primary" {
		t.Fatalf("unexpected integrations: %+v", account)
	}
}

func TestSetupOutlookUsesSafeDefaults(t *testing.T) {
	dir := t.TempDir()
	account, err := Setup(strings.NewReader("outlook\n\n\napplication-id\n\n"), &bytes.Buffer{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if account.ID != "work" || account.RedirectURL != outlookRedirectURL || got.Core.Microsoft.Tenant != "common" {
		t.Fatalf("account=%+v config=%+v", account, got.Core.Microsoft)
	}
}

func TestSetupDoesNotOverwriteConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(strings.NewReader(""), &bytes.Buffer{}, dir); err == nil {
		t.Fatal("Setup should refuse to overwrite an existing config")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "keep me" {
		t.Fatalf("existing config changed: %q", b)
	}
}
