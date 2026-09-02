package application

import (
	"context"
	"encoding/json"

	core "github.com/nmhossain02/mailman/internal/domain"
)

// Workbench is the user-facing application boundary shared by the TUI and CLI.
type Workbench interface {
	ListConversations(context.Context, core.CommandDraft) ([]core.Conversation, error)
	GetConversation(context.Context, string) (ConversationDetail, error)
	ListRules(context.Context) ([]core.Rule, error)
	ListSchedules(context.Context) ([]core.Schedule, error)
	ListPlans(context.Context) ([]core.Plan, error)
	Interpret(context.Context, string, core.TranslationContext) (Interpretation, error)
	Preview(context.Context, core.CommandDraft) (PlanPreview, error)
	FreezePlan(context.Context, string) (core.Plan, error)
	ApplyPlan(context.Context, string) (core.Plan, error)
	SaveCommandCorrection(context.Context, core.CommandCorrection) error
}

// PlanDecisionBackend is an optional extension used by interactive backends to
// persist selective bulk-review decisions immediately before freezing.
type PlanDecisionBackend interface {
	DecidePlan(context.Context, string, []string, []string) (core.Plan, error)
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
type FakeWorkbench struct {
	Conversations    []core.Conversation
	Details          map[string]ConversationDetail
	Rules            []core.Rule
	Schedules        []core.Schedule
	Plans            []core.Plan
	Interpretation   Interpretation
	PreviewResult    PlanPreview
	Frozen, Applied  core.Plan
	Err              error
	SavedCorrections []core.CommandCorrection
	Calls            []string
}

func (f *FakeWorkbench) ListConversations(context.Context, core.CommandDraft) ([]core.Conversation, error) {
	f.Calls = append(f.Calls, "list_conversations")
	return f.Conversations, f.Err
}
func (f *FakeWorkbench) GetConversation(_ context.Context, id string) (ConversationDetail, error) {
	f.Calls = append(f.Calls, "get_conversation:"+id)
	return f.Details[id], f.Err
}
func (f *FakeWorkbench) ListRules(context.Context) ([]core.Rule, error) {
	f.Calls = append(f.Calls, "list_rules")
	return f.Rules, f.Err
}
func (f *FakeWorkbench) ListSchedules(context.Context) ([]core.Schedule, error) {
	f.Calls = append(f.Calls, "list_schedules")
	return f.Schedules, f.Err
}
func (f *FakeWorkbench) ListPlans(context.Context) ([]core.Plan, error) {
	f.Calls = append(f.Calls, "list_plans")
	return f.Plans, f.Err
}
func (f *FakeWorkbench) Interpret(context.Context, string, core.TranslationContext) (Interpretation, error) {
	f.Calls = append(f.Calls, "interpret")
	return f.Interpretation, f.Err
}
func (f *FakeWorkbench) Preview(context.Context, core.CommandDraft) (PlanPreview, error) {
	f.Calls = append(f.Calls, "preview")
	return f.PreviewResult, f.Err
}
func (f *FakeWorkbench) FreezePlan(context.Context, string) (core.Plan, error) {
	f.Calls = append(f.Calls, "freeze")
	return f.Frozen, f.Err
}
func (f *FakeWorkbench) DecidePlan(_ context.Context, _ string, approved, rejected []string) (core.Plan, error) {
	f.Calls = append(f.Calls, "decide")
	return f.Frozen, f.Err
}
func (f *FakeWorkbench) ApplyPlan(context.Context, string) (core.Plan, error) {
	f.Calls = append(f.Calls, "apply")
	return f.Applied, f.Err
}
func (f *FakeWorkbench) SaveCommandCorrection(_ context.Context, label core.CommandCorrection) error {
	f.Calls = append(f.Calls, "save_eval_label")
	f.SavedCorrections = append(f.SavedCorrections, label)
	return f.Err
}

var _ Workbench = (*FakeWorkbench)(nil)
