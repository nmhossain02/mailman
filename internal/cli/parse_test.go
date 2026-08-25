package cli

import (
	"strings"
	"testing"
)

func TestParseModes(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		mode    Mode
		command string
		text    string
	}{
		{name: "no args opens TUI", mode: ModeTUI},
		{name: "exact setup", args: []string{"setup"}, mode: ModeExact, command: "setup"},
		{name: "exact sync", args: []string{"sync", "--json"}, mode: ModeExact, command: "sync"},
		{name: "exact version", args: []string{"version"}, mode: ModeExact, command: "version"},
		{name: "exact schedule", args: []string{"schedule", "run", "morning"}, mode: ModeExact, command: "schedule run"},
		{name: "natural text", args: []string{"find", "old", "newsletters"}, mode: ModeNatural, text: "find old newsletters"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if got.Mode != test.mode || got.Command != test.command || got.NaturalText != test.text {
				t.Fatalf("unexpected request: %#v", got)
			}
		})
	}
}

func TestExternalEvalRequiresExplicitPositiveCap(t *testing.T) {
	for _, args := range [][]string{
		{"eval", "run", "--allow-external"},
		{"eval", "run", "--max-external-calls", "2"},
		{"eval", "run", "--allow-external", "--max-external-calls", "0"},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("Parse(%q) should fail", args)
		}
	}
	req, err := Parse([]string{"eval", "run", "--allow-external", "--max-external-calls", "3", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if req.MaxExternalCalls != 3 || !req.AllowExternal || !req.JSON {
		t.Fatalf("unexpected request: %#v", req)
	}
	if notice := req.ExternalCapNotice(); !strings.Contains(notice, "maximum 3") {
		t.Fatalf("notice does not display cap: %q", notice)
	}
}

func TestExactCommandsRejectLooseArguments(t *testing.T) {
	for _, args := range [][]string{{"setup", "again"}, {"doctor", "now"}, {"schedule", "run"}, {"sync", "extra"}, {"eval", "later"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("Parse(%q) should fail", args)
		}
	}
}

func TestAuthAccount(t *testing.T) {
	req, err := Parse([]string{"auth", "personal"})
	if err != nil || req.Command != "auth" || req.Name != "personal" {
		t.Fatalf("request=%+v err=%v", req, err)
	}
}
