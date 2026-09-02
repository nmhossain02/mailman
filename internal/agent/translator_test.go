package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	core "github.com/nmhossain02/mailman/internal/domain"
)

func TestRelativeDateFixtures(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) // Saturday
	cases := map[string]string{
		"today": "2026-08-22", "tomorrow": "2026-08-23", "next monday": "2026-08-24",
		"next tuesday": "2026-08-25", "next wednesday": "2026-08-26", "next thursday": "2026-08-27",
		"next friday": "2026-08-28", "next saturday": "2026-08-29", "next sunday": "2026-08-23",
		"in 1 day": "2026-08-23", "in 2 days": "2026-08-24", "in 1 week": "2026-08-29",
		"in 3 weeks": "2026-09-12", "2027-01-02": "2027-01-02", "in 0 days": "2026-08-22",
	}
	for phrase, want := range cases {
		t.Run(phrase, func(t *testing.T) {
			got, ok := ParseRelativeDate(phrase, now)
			if !ok || got.Format("2006-01-02") != want {
				t.Fatalf("got %v/%v want %s", got, ok, want)
			}
		})
	}
	if _, ok := ParseRelativeDate("sometime soon", now); ok {
		t.Fatal("unbounded date language should not parse")
	}
	if !HasUnsupportedDateLanguage("do this sometime soon") || HasUnsupportedDateLanguage("do this tomorrow") {
		t.Fatal("unsupported date language detection")
	}
}

func TestNaturalCommandFixtures(t *testing.T) {
	data, err := os.ReadFile("../../testdata/inference/natural_commands.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Input  string            `json:"input"`
		Output core.CommandDraft `json:"output"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) < 15 {
		t.Fatalf("need at least 15 fixtures, got %d", len(fixtures))
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Input, func(t *testing.T) {
			encoded, _ := json.Marshal(fixture.Output)
			backend := &FakeBackend{BackendID: "local", InferFunc: func(context.Context, Request) (ProviderResult, error) {
				return ProviderResult{RawOutput: encoded}, nil
			}}
			got, err := (Translator{Backend: backend, Model: "tiny"}).Translate(context.Background(), fixture.Input, core.TranslationContext{
				SelectedType: "conversation", SelectedID: "c1", RuleNames: []string{"Newsletters", "Receipts"},
				Now: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC), Timezone: "UTC",
			})
			if err != nil {
				t.Fatal(err)
			}
			want := fixture.Output
			if want.Reference == "this" {
				want.Reference = "c1"
			}
			if !equalJSON(got, want) {
				t.Fatalf("got %#v want %#v", got, want)
			}
		})
	}
}

func equalJSON(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func TestResolveReferences(t *testing.T) {
	context := core.TranslationContext{SelectedType: "conversation", SelectedID: "c1", LabelNames: []string{"Travel", "Travel Receipts", "Finance"}}
	draft := core.CommandDraft{Intent: "view", Target: "conversation", Reference: "this"}
	if got := ResolveCommandReferences(draft, context); got.Reference != "c1" {
		t.Fatalf("selected: %#v", got)
	}
	draft.Reference = "Finance"
	if got := ResolveCommandReferences(draft, context); got.Reference != "Finance" {
		t.Fatalf("exact: %#v", got)
	}
	draft.Reference = "Travel"
	if got := ResolveCommandReferences(draft, context); got.Reference != "Travel" {
		t.Fatalf("exact should win: %#v", got)
	}
	draft.Reference = "tra"
	if got := ResolveCommandReferences(draft, context); got.Intent != "clarify" {
		t.Fatalf("ambiguous: %#v", got)
	}
	draft.Reference = "missing"
	if got := ResolveCommandReferences(draft, context); got.Intent != "clarify" {
		t.Fatalf("missing: %#v", got)
	}
}

func TestUnsupportedDateClarifiesWithoutModel(t *testing.T) {
	calls := 0
	backend := &FakeBackend{BackendID: "local", InferFunc: func(context.Context, Request) (ProviderResult, error) {
		calls++
		return ProviderResult{}, nil
	}}
	got, err := (Translator{Backend: backend}).Translate(context.Background(), "do this at the end of next month", core.TranslationContext{})
	if err != nil || got.Intent != "clarify" || calls != 0 {
		t.Fatalf("got %#v err=%v calls=%d", got, err, calls)
	}
}
