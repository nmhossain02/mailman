package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const outlookRedirectURL = "http://127.0.0.1:53682/oauth/callback"

var accountIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Setup creates a complete config through a short interactive prompt. The
// generated JSON is an internal persistence format, not part of normal setup.
func Setup(in io.Reader, out io.Writer, dataDir string) (Account, error) {
	if strings.TrimSpace(dataDir) == "" {
		return Account{}, errors.New("setup: data directory is empty")
	}
	configPath := filepath.Join(dataDir, "config.json")
	if _, err := os.Stat(configPath); err == nil {
		return Account{}, fmt.Errorf("setup: %s already exists", configPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Account{}, fmt.Errorf("setup: inspect existing config: %w", err)
	}

	p := setupPrompter{scanner: bufio.NewScanner(in), out: out}
	fmt.Fprintln(out, "Let's connect your first mailbox. Press Enter to accept a default.")
	provider, err := p.choice("Email provider", "gmail", "gmail", "outlook")
	if err != nil {
		return Account{}, err
	}
	provider = strings.ToLower(provider)
	defaultID, defaultName := "personal", "Personal Gmail"
	if provider == "outlook" {
		defaultID, defaultName = "work", "Work Outlook"
	}
	accountID, err := p.value("Short account name", defaultID, func(value string) error {
		if !accountIDPattern.MatchString(value) {
			return errors.New("use only letters, numbers, dot, underscore, or hyphen")
		}
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	displayName, err := p.value("Display name", defaultName, nil)
	if err != nil {
		return Account{}, err
	}

	file := Defaults()
	account := Account{ID: accountID, Name: displayName, Provider: provider, Enabled: true}
	if provider == "gmail" {
		file.Core.Google.ClientID, err = p.required("Google OAuth client ID")
		if err != nil {
			return Account{}, err
		}
		file.Core.Google.ClientSecret, err = p.required("Google OAuth client secret")
		if err == nil {
			var enabled bool
			enabled, err = p.confirm("Enable Google Tasks integration", false)
			if enabled {
				account.Integrations = append(account.Integrations, "google_tasks")
				account.TaskListID = "@default"
			}
		}
		if err == nil {
			var enabled bool
			enabled, err = p.confirm("Enable Google Calendar integration", false)
			if enabled {
				account.Integrations = append(account.Integrations, "google_calendar")
				account.CalendarID = "primary"
			}
		}
		account.TokenKey = "google." + accountID
	} else {
		file.Core.Microsoft.ClientID, err = p.required("Microsoft application (client) ID")
		if err == nil {
			file.Core.Microsoft.Tenant, err = p.value("Microsoft tenant", "common", nil)
		}
		account.TokenKey = "microsoft." + accountID
		account.RedirectURL = outlookRedirectURL
	}
	if err != nil {
		return Account{}, err
	}
	file.Accounts = []Account{account}

	if err = os.MkdirAll(dataDir, 0700); err != nil {
		return Account{}, fmt.Errorf("setup: create application directory: %w", err)
	}
	if err = os.Chmod(dataDir, 0700); err != nil {
		return Account{}, fmt.Errorf("setup: secure application directory: %w", err)
	}
	if err = saveNew(configPath, file); err != nil {
		return Account{}, err
	}
	fmt.Fprintf(out, "\nSetup saved privately at %s\n", configPath)
	return account, nil
}

func saveNew(path string, value File) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("setup: create config: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("setup: close config: %w", closeErr)
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(value); err != nil {
		return fmt.Errorf("setup: write config: %w", err)
	}
	return nil
}

type setupPrompter struct {
	scanner *bufio.Scanner
	out     io.Writer
}

func (p setupPrompter) read(label string) (string, error) {
	fmt.Fprint(p.out, label+": ")
	if !p.scanner.Scan() {
		if err := p.scanner.Err(); err != nil {
			return "", fmt.Errorf("setup: read answer: %w", err)
		}
		return "", errors.New("setup: input ended before setup was complete")
	}
	return strings.TrimSpace(p.scanner.Text()), nil
}

func (p setupPrompter) value(label, fallback string, validate func(string) error) (string, error) {
	for {
		value, err := p.read(fmt.Sprintf("%s [%s]", label, fallback))
		if err != nil {
			return "", err
		}
		if value == "" {
			value = fallback
		}
		if validate == nil {
			return value, nil
		}
		if err = validate(value); err == nil {
			return value, nil
		}
		fmt.Fprintf(p.out, "  %s\n", err)
	}
}

func (p setupPrompter) required(label string) (string, error) {
	for {
		value, err := p.read(label)
		if err != nil {
			return "", err
		}
		if value != "" {
			return value, nil
		}
		fmt.Fprintln(p.out, "  This value is required.")
	}
}

func (p setupPrompter) choice(label, fallback string, choices ...string) (string, error) {
	wanted := map[string]bool{}
	for _, choice := range choices {
		wanted[choice] = true
	}
	return p.value(label+" (gmail/outlook)", fallback, func(value string) error {
		if !wanted[strings.ToLower(value)] {
			return fmt.Errorf("choose %s", strings.Join(choices, " or "))
		}
		return nil
	})
}

func (p setupPrompter) confirm(label string, fallback bool) (bool, error) {
	defaultText := "no"
	if fallback {
		defaultText = "yes"
	}
	value, err := p.value(label+" (yes/no)", defaultText, func(value string) error {
		switch strings.ToLower(value) {
		case "yes", "y", "no", "n":
			return nil
		default:
			return errors.New("answer yes or no")
		}
	})
	if err != nil {
		return false, err
	}
	return strings.EqualFold(value, "yes") || strings.EqualFold(value, "y"), nil
}
