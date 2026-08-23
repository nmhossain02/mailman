package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nmhossain02/mailman/internal/app"
	"github.com/nmhossain02/mailman/internal/core"
	evalpkg "github.com/nmhossain02/mailman/internal/eval"
	"github.com/nmhossain02/mailman/internal/inference"
	"github.com/nmhossain02/mailman/internal/policy"
	"github.com/nmhossain02/mailman/internal/provider"
	"github.com/nmhossain02/mailman/internal/store"
)

func TestFixtureBackedEndToEnd(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "mailman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	mail := &provider.FakeMailProvider{ProviderID: "fake", SyncFunc: func(context.Context, provider.OpaqueCursor) (provider.SyncPage, error) {
		return provider.SyncPage{Done: true, Checkpoint: provider.OpaqueCursor(`{"cursor":"1"}`), Upserts: []provider.ProviderMessage{{StableID: "m", ConversationKey: "c", Revision: "r1", Subject: "Old alert", Sender: "alerts@example.test", ReceivedAt: now.Add(-60 * 24 * time.Hour), Read: true, ContentLoaded: true}}}, nil
	}, ApplyFunc: func(_ context.Context, states []provider.DesiredMailState) ([]provider.OperationResult, error) {
		return []provider.OperationResult{{ExecutionKey: states[0].ExecutionKey, Status: "succeeded", RemoteID: "m"}}, nil
	}}
	if _, err = (app.SyncService{Store: db}).Sync(ctx, "account", "mail", mail, core.RoutePolicy{Mode: "local_only", Privacy: "local_only"}); err != nil {
		t.Fatal(err)
	}
	model := &inference.FakeBackend{BackendID: "fixture-local", InferFunc: func(context.Context, inference.Request) (inference.ProviderResult, error) {
		return inference.ProviderResult{RawOutput: json.RawMessage(`{"Intent":"propose","Target":"message","Filters":[{"Field":"sender_domain","Operator":"eq","Value":"example.test"}],"Actions":[{"Kind":"mark_read","Argument":""},{"Kind":"archive","Argument":""}],"GroupBy":"sender","SortBy":"received_at","Reference":"","Clarification":""}`)}, nil
	}}
	draft, err := (inference.Translator{Backend: model, Model: "fixture"}).Translate(ctx, "archive old example.test alerts and mark them read", core.TranslationContext{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := db.ConversationMessages(ctx, "account:c")
	if err != nil {
		t.Fatal(err)
	}
	rule := core.Rule{ID: "request", Name: "request", Enabled: true, Conditions: draft.Filters, Actions: draft.Actions}
	derived := policy.DeriveContext(messages, "me@example.test", now, false)
	candidates := policy.Evaluate([]core.Rule{rule}, derived)
	plans := app.PlanService{Store: db, Mail: map[string]provider.MailProvider{"account": mail}, Now: func() time.Time { return now }}
	plan, err := plans.Draft(ctx, "review", candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 2 {
		t.Fatalf("operations=%d", len(plan.Operations))
	}
	approved := map[string]bool{}
	for _, op := range plan.Operations {
		approved[op.ID] = true
	}
	plan, err = plans.Decide(ctx, plan, approved, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = plans.Freeze(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = plans.Apply(ctx, plan)
	if err != nil || plan.Status != "completed" {
		t.Fatalf("apply status=%s err=%v", plan.Status, err)
	}
	trace := core.InferenceTrace{ID: "trace", TargetID: "account:c", TaskName: "translate_command", InputSnapshot: json.RawMessage(`{"request":"archive alerts"}`), CanonicalOutput: mustJSON(draft), BackendID: "fixture-local", BackendClass: "local", Outcome: "ok", Selected: true, StartedAt: now, CompletedAt: now}
	if err = db.PutTrace(ctx, trace); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Trace(ctx, "trace"); err != nil {
		t.Fatal(err)
	}
	record := evalpkg.DatasetRecord{Case: core.EvalCase{ID: "case", Dataset: "fixture", TaskName: "translate_command", TaskVersion: "1", InputJSON: json.RawMessage(`{"request":"archive alerts"}`)}, ExpectedJSON: mustJSON(draft)}
	runCfg := evalpkg.Snapshot("run", "fixture", evalpkg.RouteLocalOnly)
	result, err := evalpkg.Run(ctx, runCfg, []evalpkg.DatasetRecord{record}, func(context.Context, core.EvalCase, string) evalpkg.Observation {
		return evalpkg.Observation{Outcome: "ok", Output: mustJSON(draft)}
	})
	if err != nil {
		t.Fatal(err)
	}
	var report bytes.Buffer
	if err = evalpkg.WriteTable(&report, result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report.String(), "case") || !strings.Contains(report.String(), "1.000") {
		t.Fatalf("report=%s", report.String())
	}
}
func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
