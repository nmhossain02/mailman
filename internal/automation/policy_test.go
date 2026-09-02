package automation

import (
	core "github.com/nmhossain02/mailman/internal/domain"
	"testing"
	"time"
)

func TestEvaluateCompoundRuleAndException(t *testing.T) {
	m := core.Message{ID: "m", AccountID: "a", Revision: "r", Sender: "news@example.com", Read: false, ReceivedAt: time.Now().Add(-48 * time.Hour)}
	r := core.Rule{ID: "r", AccountID: "a", Enabled: true, Conditions: []core.Filter{{Field: "sender_domain", Operator: "eq", Value: "example.com"}, {Field: "age_days", Operator: "gte", Value: "1"}}, Actions: []core.Action{{Kind: "archive"}, {Kind: "mark_read"}, {Kind: "add_label", Argument: "newsletter"}}}
	got := Evaluate([]core.Rule{r}, Context{Message: m, Now: time.Now()})
	if len(got) != 3 {
		t.Fatalf("got %d effects", len(got))
	}
	r.Exceptions = []core.Filter{{Field: "read", Operator: "eq", Value: "false"}}
	if got := Evaluate([]core.Rule{r}, Context{Message: m, Now: time.Now()}); len(got) != 0 {
		t.Fatalf("exception did not suppress: %#v", got)
	}
}

func TestResolvePrecedenceAndConflict(t *testing.T) {
	base := Candidate{TargetType: "message", TargetID: "m"}
	got := Resolve([]Candidate{
		merge(base, Candidate{Source: "inferred", Action: core.Action{Kind: "trash"}}),
		merge(base, Candidate{Source: "user", Action: core.Action{Kind: "archive"}}),
		merge(base, Candidate{Source: "user", Action: core.Action{Kind: "mark_read"}}),
	}, nil)
	if len(got) != 2 || got[0].Action.Kind != "archive" || got[1].Action.Kind != "mark_read" {
		t.Fatalf("bad resolution %#v", got)
	}
	got = Resolve([]Candidate{merge(base, Candidate{Source: "user", Action: core.Action{Kind: "trash"}}), merge(base, Candidate{Source: "user", Action: core.Action{Kind: "archive"}})}, nil)
	if len(got) != 0 {
		t.Fatalf("equal conflict should defer: %#v", got)
	}
}
func TestDeriveContextIsConservative(t *testing.T) {
	now := time.Now().UTC()
	ctx := DeriveContext([]core.Message{{ID: "m", Sender: "me@example.com", Read: true, ReceivedAt: now.Add(-31 * 24 * time.Hour)}}, "me@example.com", now, false)
	if ctx.AwaitingMe || !ctx.Stale || !ctx.SafeToArchive {
		t.Fatalf("unexpected context %#v", ctx)
	}
	ctx = DeriveContext([]core.Message{{ID: "m", Sender: "other@example.com", Read: true, ReceivedAt: now.Add(-31 * 24 * time.Hour)}}, "me@example.com", now, false)
	if !ctx.AwaitingMe || ctx.SafeToArchive {
		t.Fatalf("unsafe disposition %#v", ctx)
	}
}
func TestResolveKeepsIndependentLabelsAndHonorsRetention(t *testing.T) {
	base := Candidate{TargetType: "message", TargetID: "m", Source: "user"}
	got := Resolve([]Candidate{merge(base, Candidate{Source: "user", Action: core.Action{Kind: "add_label", Argument: "a"}}), merge(base, Candidate{Source: "user", Action: core.Action{Kind: "add_label", Argument: "b"}})}, nil)
	if len(got) != 2 {
		t.Fatalf("independent labels collapsed: %#v", got)
	}
	got = Resolve([]Candidate{merge(base, Candidate{Source: "user", Action: core.Action{Kind: "trash"}})}, []string{"legal-hold"})
	if len(got) != 0 {
		t.Fatalf("retained message disposition survived: %#v", got)
	}
}
func merge(a, b Candidate) Candidate { b.TargetType = a.TargetType; b.TargetID = a.TargetID; return b }
