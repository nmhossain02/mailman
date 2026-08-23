package core

import (
	"strings"
	"testing"
)

func TestCommandRejectsUnknownFilterField(t *testing.T) {
	t.Parallel()
	draft := CommandDraft{
		Intent:  "find",
		Target:  "message",
		Filters: []Filter{{Field: "made_up", Operator: "eq", Value: "x"}},
	}
	if err := draft.Validate(); err == nil {
		t.Fatal("Validate() accepted an unknown filter field")
	}
}

func TestDecodeCommandRejectsUnknownJSONField(t *testing.T) {
	t.Parallel()
	_, err := DecodeCommandDraft([]byte(`{"Intent":"find","Target":"message","Surprise":true}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("DecodeCommandDraft() error = %v, want unknown field", err)
	}
}

func TestValidationRejectsInvalidEnums(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{"command intent", CommandDraft{Intent: "execute", Target: "message"}.Validate()},
		{"operation risk", Operation{ID: "o", ExecutionKey: "k", TargetType: "message", TargetID: "m", Kind: "archive", Risk: "catastrophic", Status: "proposed"}.Validate()},
		{"schedule route", Schedule{ID: "s", Name: "daily", DraftPlanName: "review", EverySeconds: 60, Route: RoutePolicy{Mode: "cloud", Privacy: "external_allowed"}}.Validate()},
	}
	for _, test := range tests {
		if test.err == nil {
			t.Errorf("%s accepted invalid enum", test.name)
		}
	}
}

func TestDeleteActionIsNotPermanentDelete(t *testing.T) {
	t.Parallel()
	if err := (Action{Kind: "delete"}).Validate(); err == nil {
		t.Fatal("Validate() accepted delete; v1 only permits recoverable trash")
	}
}
