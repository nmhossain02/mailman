package application

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	store "github.com/nmhossain02/mailman/internal/adapters/sqlite"
	inference "github.com/nmhossain02/mailman/internal/agent"
	"github.com/nmhossain02/mailman/internal/application/progress"
	"github.com/nmhossain02/mailman/internal/application/provider"
	policy "github.com/nmhossain02/mailman/internal/automation"
	core "github.com/nmhossain02/mailman/internal/domain"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "mailman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type processed struct{ ids []string }

func (p *processed) ProcessConversation(_ context.Context, c core.Conversation, _ []core.Message, _ core.RoutePolicy) error {
	p.ids = append(p.ids, c.ID)
	return nil
}

func TestSyncOnlyProcessesChangedConversationAndPromotesFinalCursor(t *testing.T) {
	db := testDB(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	p := &processed{}
	var stages []string
	ctx := progress.WithReporter(context.Background(), func(event progress.Event) {
		stages = append(stages, event.Stage)
	})
	mail := &provider.FakeMailProvider{ProviderID: "gmail", SyncFunc: func(_ context.Context, c provider.OpaqueCursor) (provider.SyncPage, error) {
		calls++
		if calls == 1 {
			return provider.SyncPage{Upserts: []provider.ProviderMessage{{StableID: "m1", ConversationKey: "c1", Revision: "r1", Subject: "one", Sender: "a@x", ReceivedAt: now}}, Continuation: provider.OpaqueCursor(`"next"`)}, nil
		}
		return provider.SyncPage{Upserts: []provider.ProviderMessage{{StableID: "m2", ConversationKey: "c1", Revision: "r1", Subject: "two", Sender: "b@x", ReceivedAt: now.Add(time.Minute), ContentLoaded: true}}, Checkpoint: provider.OpaqueCursor(`"done"`), Done: true}, nil
	}, ContentFunc: func(_ context.Context, ids []string) ([]provider.ProviderContent, error) {
		return []provider.ProviderContent{{MessageID: ids[0], PlainText: "body"}}, nil
	}}
	got, err := (SyncService{Store: db, Processor: p, Now: func() time.Time { return now }}).Sync(ctx, "a", "mail", mail, core.RoutePolicy{Mode: "local_only", Privacy: "local_only"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pages != 2 || got.ChangedMessages != 2 || got.ChangedConversations != 1 || len(p.ids) != 1 || p.ids[0] != "a:c1" {
		t.Fatalf("bad incremental result %#v processed=%v", got, p.ids)
	}
	wantStages := []string{progress.StageStarting, progress.StageFetchingPage, progress.StagePageCommitted, progress.StageFetchingPage, progress.StagePageCommitted, progress.StageRules, progress.StageDone}
	if strings.Join(stages, ",") != strings.Join(wantStages, ",") {
		t.Fatalf("progress stages = %v, want %v", stages, wantStages)
	}
	cur, ok, err := db.Cursor(context.Background(), "a", "mail")
	if err != nil || !ok || cur != `"done"` {
		t.Fatalf("cursor %q %v %v", cur, ok, err)
	}
	m, err := db.Message(context.Background(), "a:m1")
	if err != nil || m.NormalizedBody != "body" {
		t.Fatalf("body not persisted: %#v %v", m, err)
	}
}

func seedMessage(t *testing.T, db *store.DB, id, rev string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	c := core.Conversation{ID: "a:c" + id, AccountID: "a", ProviderKey: "c" + id, Subject: id, LastMessageAt: now}
	if err := db.UpsertConversation(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(ctx, core.Message{ID: id, AccountID: "a", ProviderID: "remote-" + id, ConversationID: c.ID, Revision: rev, Subject: id, Sender: "x@y", ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
}
func cand(id, rev, kind, arg string) policy.Candidate {
	return policy.Candidate{TargetType: "message", TargetID: id, ExpectedRevision: rev, Source: "user", Action: core.Action{Kind: kind, Argument: arg}}
}
func approveAll(t *testing.T, s PlanService, p core.Plan) core.Plan {
	t.Helper()
	a := map[string]bool{}
	for _, op := range p.Operations {
		a[op.ID] = true
	}
	var err error
	p, err = s.Freeze(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.Decide(context.Background(), p, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCompoundPlanAppliesOneDesiredState(t *testing.T) {
	db := testDB(t)
	seedMessage(t, db, "m", "r1")
	var desired []provider.DesiredMailState
	mail := &provider.FakeMailProvider{ApplyFunc: func(_ context.Context, d []provider.DesiredMailState) ([]provider.OperationResult, error) {
		desired = append(desired, d...)
		return []provider.OperationResult{{ExecutionKey: d[0].ExecutionKey, Status: "succeeded"}}, nil
	}}
	s := PlanService{Store: db, Mail: map[string]provider.MailProvider{"a": mail}}
	p, err := s.Draft(context.Background(), "bulk", []policy.Candidate{cand("m", "r1", "archive", ""), cand("m", "r1", "mark_read", ""), cand("m", "r1", "add_label", "news")})
	if err != nil {
		t.Fatal(err)
	}
	p = approveAll(t, s, p)
	p, err = s.Apply(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "completed" || len(desired) != 1 || desired[0].Disposition != "archive" || desired[0].Read == nil || !*desired[0].Read || len(desired[0].EnsureTags) != 1 {
		t.Fatalf("bad compound apply plan=%#v desired=%#v", p, desired)
	}
}

func TestStaleRevisionRejectedBeforeProviderCall(t *testing.T) {
	db := testDB(t)
	seedMessage(t, db, "m", "new")
	calls := 0
	mail := &provider.FakeMailProvider{ApplyFunc: func(context.Context, []provider.DesiredMailState) ([]provider.OperationResult, error) {
		calls++
		return nil, nil
	}}
	s := PlanService{Store: db, Mail: map[string]provider.MailProvider{"a": mail}}
	p, err := s.Draft(context.Background(), "stale", []policy.Candidate{cand("m", "old", "archive", "")})
	if err != nil {
		t.Fatal(err)
	}
	p = approveAll(t, s, p)
	p, err = s.Apply(context.Background(), p)
	if !errors.Is(err, ErrStaleRevision) || calls != 0 || p.Operations[0].Status != "failed" {
		t.Fatalf("stale result calls=%d plan=%#v err=%v", calls, p, err)
	}
}

func TestPartialFailureAndUncertainAreJournaledWithoutRetry(t *testing.T) {
	db := testDB(t)
	seedMessage(t, db, "m1", "r")
	seedMessage(t, db, "m2", "r")
	calls := map[string]int{}
	mail := &provider.FakeMailProvider{ApplyFunc: func(_ context.Context, d []provider.DesiredMailState) ([]provider.OperationResult, error) {
		id := d[0].ProviderMessageID
		calls[id]++
		if id == "remote-m1" {
			return []provider.OperationResult{{ExecutionKey: d[0].ExecutionKey, Status: "succeeded"}}, nil
		}
		return nil, errors.New("connection lost")
	}}
	s := PlanService{Store: db, Mail: map[string]provider.MailProvider{"a": mail}}
	p, err := s.Draft(context.Background(), "partial", []policy.Candidate{cand("m1", "r", "archive", ""), cand("m2", "r", "archive", "")})
	if err != nil {
		t.Fatal(err)
	}
	p = approveAll(t, s, p)
	p, _ = s.Apply(context.Background(), p)
	if p.Status != "partial" || calls["remote-m1"] != 1 || calls["remote-m2"] != 1 {
		t.Fatalf("partial plan=%#v calls=%v", p, calls)
	}
	for i := range p.Operations {
		if p.Operations[i].TargetID == "m2" {
			p.Operations[i].Status = "approved"
		}
	}
	p.Status = "frozen"
	p, _ = s.Apply(context.Background(), p)
	if calls["remote-m2"] != 2 {
		t.Fatalf("uncertain desired state was not safely reconciled: %v", calls)
	}
}

func TestTaskAndEventRequireApproval(t *testing.T) {
	db := testDB(t)
	taskCalls, eventCalls := 0, 0
	tasks := &provider.FakeTaskTarget{EnsureFunc: func(context.Context, provider.TaskDraft, string) (provider.TargetReceipt, error) {
		taskCalls++
		return provider.TargetReceipt{Status: "succeeded"}, nil
	}}
	calendar := &provider.FakeCalendarTarget{EnsureFunc: func(context.Context, provider.EventDraft, string) (provider.TargetReceipt, error) {
		eventCalls++
		return provider.TargetReceipt{Status: "succeeded"}, nil
	}}
	s := PlanService{Store: db, Tasks: map[string]provider.TaskTarget{"a": tasks}, Calendars: map[string]provider.CalendarTarget{"a": calendar}}
	values := []policy.Candidate{{TargetType: "task", TargetID: "t", Source: "user", Action: core.Action{Kind: "create_task", Argument: `{"title":"Reply"}`}}, {TargetType: "event", TargetID: "e", Source: "user", Action: core.Action{Kind: "create_event", Argument: `{"title":"Review","start":"2026-01-01T10:00:00Z","end":"2026-01-01T11:00:00Z"}`}}}
	p, err := s.Draft(context.Background(), "targets", values)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.Freeze(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if taskCalls+eventCalls != 0 {
		t.Fatal("unapproved external target was created")
	}
	p, err = s.Draft(context.Background(), "targets approved", values)
	if err != nil {
		t.Fatal(err)
	}
	p = approveAll(t, s, p)
	p, err = s.Apply(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if taskCalls != 1 || eventCalls != 1 {
		t.Fatalf("approved targets calls=%d/%d plan=%#v", taskCalls, eventCalls, p)
	}
}

func TestUncertainTaskIsReconciledByEnsureBeforeRetry(t *testing.T) {
	db := testDB(t)
	calls := 0
	tasks := &provider.FakeTaskTarget{EnsureFunc: func(context.Context, provider.TaskDraft, string) (provider.TargetReceipt, error) {
		calls++
		if calls == 1 {
			return provider.TargetReceipt{Status: "uncertain"}, errors.New("lost response")
		}
		return provider.TargetReceipt{ProviderID: "task-1", Status: "succeeded"}, nil
	}}
	s := PlanService{Store: db, Tasks: map[string]provider.TaskTarget{"a": tasks}}
	c := policy.Candidate{TargetType: "task", TargetID: "source", Source: "user", Action: core.Action{Kind: "create_task", Argument: `{"Title":"Reply"}`}}
	p, err := s.Draft(context.Background(), "task reconcile", []policy.Candidate{c})
	if err != nil {
		t.Fatal(err)
	}
	p = approveAll(t, s, p)
	p, _ = s.Apply(context.Background(), p)
	if p.Operations[0].Status != "uncertain" {
		t.Fatalf("first result %#v", p)
	}
	p.Status = "frozen"
	p.Operations[0].Status = "approved"
	p, err = s.Apply(context.Background(), p)
	if err != nil || p.Operations[0].Status != "succeeded" || calls != 2 {
		t.Fatalf("reconcile result calls=%d plan=%#v err=%v", calls, p, err)
	}
}

func TestUndoProducesRestoreDraft(t *testing.T) {
	db := testDB(t)
	s := PlanService{Store: db}
	p := core.Plan{ID: "p", Name: "done", Status: "completed", Operations: []core.Operation{{ID: "o", ExecutionKey: "k", TargetType: "message", TargetID: "m", Kind: "trash", Risk: "low", Arguments: json.RawMessage(`{"value":""}`), Status: "succeeded"}}, CreatedAt: time.Now()}
	undo, err := s.Undo(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(undo.Operations) != 1 || undo.Operations[0].Kind != "restore" {
		t.Fatalf("bad undo %#v", undo)
	}
}

func TestPrimitiveRunnerUsesLocalCacheAndPersistsTrace(t *testing.T) {
	db := testDB(t)
	calls := 0
	backend := &inference.FakeBackend{BackendID: "ollama", InferFunc: func(context.Context, inference.Request) (inference.ProviderResult, error) {
		calls++
		return inference.ProviderResult{}, nil
	}}
	task, err := inference.BuiltinTask("message_kind", "small")
	if err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"subject":"hello"}`)
	key, err := inference.CacheKey("ollama", "sha256:model", task, input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	output := json.RawMessage(`{"Kind":"personal","EvidenceMessageIDs":["m"],"Abstain":false}`)
	if err = db.PutCache(context.Background(), key, output, now); err != nil {
		t.Fatal(err)
	}
	r := inference.PrimitiveRunner{Router: &inference.Router{Local: backend}, Store: db, LocalModel: "small", LocalRevision: "sha256:model", Now: func() time.Time { return now }}
	got, err := r.Run(context.Background(), "message_kind", "m", input, core.RoutePolicy{Mode: "local_only", Privacy: "local_only"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || got.Output == nil {
		t.Fatalf("cache miss calls=%d output=%#v", calls, got.Output)
	}
	traceID := stableID("trace", "cache", "message_kind", "m", key)
	trace, err := db.Trace(context.Background(), traceID)
	if err != nil || !trace.CacheHit || !trace.Selected {
		t.Fatalf("cache trace %#v err=%v", trace, err)
	}
}
