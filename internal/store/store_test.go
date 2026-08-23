package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nabeel/mailman/internal/core"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "mailman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMigrationEmptyRepeatedAndChecksumMismatch(t *testing.T) {
	db := openTestDB(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if err := db.CorruptMigrationChecksumForTest(context.Background(), "migrations/001_initial.sql"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background()); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func fixtureMessage(id, body string, at time.Time) core.Message {
	return core.Message{ID: id, AccountID: "a", ProviderID: "p-" + id, ConversationID: "c", Revision: "r", InternetMessageID: "i-" + id, Subject: "Weekly news", Sender: "alerts@example.com", NormalizedBody: body, Recipients: []string{"me@example.com"}, ReceivedAt: at, FolderID: "inbox", TagIDs: []string{"unread"}}
}

func TestFTSInsertUpdateDeleteAndConversationOrdering(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC()
	if err := db.UpsertConversation(ctx, core.Conversation{ID: "c", AccountID: "a", ProviderKey: "pc", Subject: "news", LastMessageAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(ctx, fixtureMessage("2", "obsolete notice", now)); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(ctx, fixtureMessage("1", "fresh digest", now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	got, err := db.SearchMessages(ctx, "obsolete", 10)
	if err != nil || len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("insert search=%v err=%v", got, err)
	}
	m := fixtureMessage("2", "current notice", now)
	if err = db.UpsertMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err = db.SearchMessages(ctx, "obsolete", 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("stale FTS rows=%v err=%v", got, err)
	}
	ordered, err := db.ConversationMessages(ctx, "c")
	if err != nil || len(ordered) != 2 || ordered[0].ID != "1" {
		t.Fatalf("order=%v err=%v", ordered, err)
	}
	if err = db.DeleteMessage(ctx, "2"); err != nil {
		t.Fatal(err)
	}
	got, err = db.SearchMessages(ctx, "current", 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("deleted FTS rows=%v err=%v", got, err)
	}
}

func TestTraceRoundTripAndCache(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC().Round(0)
	trace := core.InferenceTrace{ID: "t", TaskName: "classify", InputSnapshot: json.RawMessage(`{"x":1}`), StartedAt: now, CompletedAt: now, Selected: true}
	if err := db.PutTrace(ctx, trace); err != nil {
		t.Fatal(err)
	}
	got, err := db.Trace(ctx, "t")
	if err != nil || got.ID != trace.ID || string(got.InputSnapshot) != string(trace.InputSnapshot) {
		t.Fatalf("trace=%+v err=%v", got, err)
	}
	if err = db.PutCache(ctx, "k", json.RawMessage(`{"kind":"alert"}`), now); err != nil {
		t.Fatal(err)
	}
	value, ok, err := db.Cache(ctx, "k")
	if err != nil || !ok || string(value) != "{\"kind\":\"alert\"}" {
		t.Fatalf("cache=%s,%v,%v", value, ok, err)
	}
}

func TestOperationIdempotencyAndHashMismatch(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now()
	entry, exists, err := db.BeginOperation(ctx, "exec-1", "hash-a", now)
	if err != nil || exists || entry.State != "pending" {
		t.Fatalf("first=%+v,%v,%v", entry, exists, err)
	}
	entry, exists, err = db.BeginOperation(ctx, "exec-1", "hash-a", now)
	if err != nil || !exists || entry.State != "pending" {
		t.Fatalf("repeat=%+v,%v,%v", entry, exists, err)
	}
	_, _, err = db.BeginOperation(ctx, "exec-1", "hash-b", now)
	if !errors.Is(err, ErrRequestHashMismatch) {
		t.Fatalf("mismatch=%v", err)
	}
	if err = db.FinishOperation(ctx, "exec-1", "succeeded", json.RawMessage(`{"id":"remote"}`), now); err != nil {
		t.Fatal(err)
	}
	entry, _, err = db.BeginOperation(ctx, "exec-1", "hash-a", now)
	if err != nil || entry.State != "succeeded" {
		t.Fatalf("finished=%+v err=%v", entry, err)
	}
}

func TestDomainRecordsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	seq := 2
	rule := core.Rule{ID: "rule", AccountID: "a", Source: "local", Name: "news", Enabled: true, Sequence: &seq, Conditions: []core.Filter{{Field: "sender", Operator: "contains", Value: "news"}}, Actions: []core.Action{{Kind: "archive"}}, RawProvider: json.RawMessage(`{}`)}
	if err := db.SaveRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	rules, err := db.Rules(ctx)
	if err != nil || len(rules) != 1 || rules[0].Actions[0].Kind != "archive" {
		t.Fatalf("rules=%+v err=%v", rules, err)
	}
	plan := core.Plan{ID: "plan", Name: "review", Status: "draft", CreatedAt: time.Now().UTC(), Operations: []core.Operation{{ID: "op", ExecutionKey: "key", TargetType: "message", TargetID: "m", Kind: "archive", Risk: "low", Arguments: json.RawMessage(`{}`), Status: "draft"}}}
	if err = db.SavePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	plans, err := db.Plans(ctx)
	if err != nil || len(plans) != 1 || len(plans[0].Operations) != 1 {
		t.Fatalf("plans=%+v err=%v", plans, err)
	}
	schedule := core.Schedule{ID: "s", Name: "hourly", DraftPlanName: "review", Enabled: true, EverySeconds: 3600, AccountIDs: []string{"a"}, Route: core.RoutePolicy{Mode: "local"}}
	if err = db.SaveSchedule(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	schedules, err := db.Schedules(ctx)
	if err != nil || len(schedules) != 1 || schedules[0].EverySeconds != 3600 {
		t.Fatalf("schedules=%+v err=%v", schedules, err)
	}
}

func TestSyncRepositoryMethods(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC()
	if _, ok, err := db.Cursor(ctx, "a", "inbox"); err != nil || ok {
		t.Fatalf("empty cursor ok=%v err=%v", ok, err)
	}
	if err := db.PromoteCursor(ctx, "a", "inbox", "next", now); err != nil {
		t.Fatal(err)
	}
	if cursor, ok, err := db.Cursor(ctx, "a", "inbox"); err != nil || !ok || cursor != "next" {
		t.Fatalf("cursor=%q ok=%v err=%v", cursor, ok, err)
	}
	if err := db.UpsertConversation(ctx, core.Conversation{ID: "c", AccountID: "a", ProviderKey: "provider-c", Subject: "subject", LastMessageAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(ctx, fixtureMessage("m", "body", now)); err != nil {
		t.Fatal(err)
	}
	if m, err := db.Message(ctx, "m"); err != nil || m.ConversationID != "c" {
		t.Fatalf("message=%+v err=%v", m, err)
	}
	if c, err := db.Conversation(ctx, "c"); err != nil || len(c.MessageIDs) != 1 {
		t.Fatalf("conversation=%+v err=%v", c, err)
	}
	claim := core.Claim{ID: "claim", TargetType: "message", TargetID: "m", Name: "stale", Value: json.RawMessage(`true`), Evidence: json.RawMessage(`[]`), CreatedAt: now}
	if err := db.UpsertClaim(ctx, claim); err != nil {
		t.Fatal(err)
	}
	rules := []core.Rule{{ID: "native", AccountID: "a", Source: "provider", ProviderID: "remote", Name: "native", Enabled: true, RawProvider: json.RawMessage(`{}`)}}
	if err := db.ReplaceProviderRules(ctx, "a", "provider", rules); err != nil {
		t.Fatal(err)
	}
	got, err := db.Rules(ctx)
	if err != nil || len(got) != 1 || got[0].ID != "native" {
		t.Fatalf("rules=%+v err=%v", got, err)
	}
}

func TestCommitSyncPagePromotesOnlyFinalCheckpoint(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC()
	conversation := core.Conversation{ID: "c", AccountID: "a", ProviderKey: "pc", Subject: "s", LastMessageAt: now}
	message := fixtureMessage("m", "body", now)
	if err := db.CommitSyncPage(ctx, "a", "inbox", []core.Message{message}, []core.Conversation{conversation}, nil, "", now); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := db.Cursor(ctx, "a", "inbox"); err != nil || ok {
		t.Fatalf("intermediate promoted ok=%v err=%v", ok, err)
	}
	if err := db.CommitSyncPage(ctx, "a", "inbox", nil, nil, nil, "final", now); err != nil {
		t.Fatal(err)
	}
	if cursor, ok, err := db.Cursor(ctx, "a", "inbox"); err != nil || !ok || cursor != "final" {
		t.Fatalf("cursor=%q ok=%v err=%v", cursor, ok, err)
	}
}
