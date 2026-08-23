package ui

import (
	"context"
	"encoding/json"

	"github.com/nabeel/mailman/internal/core"
)

// Backend is the presentation-layer boundary. Implementations own all policy,
// persistence, and provider behavior; the UI only presents their results.
type Backend interface {
	ListConversations(context.Context, core.CommandDraft) ([]core.Conversation, error)
	GetConversation(context.Context, string) (ConversationDetail, error)
	ListRules(context.Context) ([]core.Rule, error)
	ListSchedules(context.Context) ([]core.Schedule, error)
	ListPlans(context.Context) ([]core.Plan, error)
	Interpret(context.Context, string, core.TranslationContext) (Interpretation, error)
	Preview(context.Context, core.CommandDraft) (PlanPreview, error)
	FreezePlan(context.Context, string) (core.Plan, error)
	ApplyPlan(context.Context, string) (core.Plan, error)
	SaveEvalLabel(context.Context, core.EvalLabel) error
}

type ConversationDetail struct {
	Conversation core.Conversation
	Messages     []core.Message
	Claims       []core.Claim
	Receipts     []Receipt
}

// Receipt is a compact view of a Mailman-created task or calendar event.
type Receipt struct {
	Kind, ProviderID, Summary, Status string
}

type Interpretation struct {
	Draft     core.CommandDraft
	TraceID   string
	Canonical json.RawMessage
}

type PlanPreview struct {
	Plan       core.Plan
	ScopeCount int
	Groups     []PlanGroup
	Outliers   []core.Operation
}

type PlanGroup struct {
	Name       string
	Count      int
	Samples    []string
	Operations []core.Operation
}

// FakeBackend keeps model tests deterministic and is also useful to embedders.
type FakeBackend struct {
	Conversations   []core.Conversation
	Details         map[string]ConversationDetail
	Rules           []core.Rule
	Schedules       []core.Schedule
	Plans           []core.Plan
	Interpretation  Interpretation
	PreviewResult   PlanPreview
	Frozen, Applied core.Plan
	Err             error
	SavedLabels     []core.EvalLabel
	Calls           []string
}

func (f *FakeBackend) ListConversations(context.Context, core.CommandDraft) ([]core.Conversation, error) {
	f.Calls = append(f.Calls, "list_conversations")
	return f.Conversations, f.Err
}
func (f *FakeBackend) GetConversation(_ context.Context, id string) (ConversationDetail, error) {
	f.Calls = append(f.Calls, "get_conversation:"+id)
	return f.Details[id], f.Err
}
func (f *FakeBackend) ListRules(context.Context) ([]core.Rule, error) {
	f.Calls = append(f.Calls, "list_rules")
	return f.Rules, f.Err
}
func (f *FakeBackend) ListSchedules(context.Context) ([]core.Schedule, error) {
	f.Calls = append(f.Calls, "list_schedules")
	return f.Schedules, f.Err
}
func (f *FakeBackend) ListPlans(context.Context) ([]core.Plan, error) {
	f.Calls = append(f.Calls, "list_plans")
	return f.Plans, f.Err
}
func (f *FakeBackend) Interpret(context.Context, string, core.TranslationContext) (Interpretation, error) {
	f.Calls = append(f.Calls, "interpret")
	return f.Interpretation, f.Err
}
func (f *FakeBackend) Preview(context.Context, core.CommandDraft) (PlanPreview, error) {
	f.Calls = append(f.Calls, "preview")
	return f.PreviewResult, f.Err
}
func (f *FakeBackend) FreezePlan(context.Context, string) (core.Plan, error) {
	f.Calls = append(f.Calls, "freeze")
	return f.Frozen, f.Err
}
func (f *FakeBackend) ApplyPlan(context.Context, string) (core.Plan, error) {
	f.Calls = append(f.Calls, "apply")
	return f.Applied, f.Err
}
func (f *FakeBackend) SaveEvalLabel(_ context.Context, label core.EvalLabel) error {
	f.Calls = append(f.Calls, "save_eval_label")
	f.SavedLabels = append(f.SavedLabels, label)
	return f.Err
}

var _ Backend = (*FakeBackend)(nil)
