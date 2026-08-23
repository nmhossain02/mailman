package provider

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nabeel/mailman/internal/core"
)

type OpaqueCursor json.RawMessage

type SyncPage struct {
	Upserts      []ProviderMessage
	DeletedIDs   []string
	Continuation OpaqueCursor
	Checkpoint   OpaqueCursor
	Done         bool
}

type Capabilities struct {
	RuleCreate, RuleUpdate, RuleDisable, RuleDelete bool
	RuleOrder, RuleStopProcessing                   bool
	BatchApply, Restore                             bool
}

type ProviderAccount struct{ ID, Address, DisplayName string }
type ProviderCollection struct{ ID, Name, Kind, ParentID string }
type ProviderContent struct {
	MessageID, PlainText string
	Raw                  json.RawMessage
}

type ProviderMessage struct {
	StableID, ConversationKey, Revision, FolderID string
	InternetMessageID, Subject, Sender            string
	Recipients, TagIDs                            []string
	ReceivedAt                                    time.Time
	Read                                          bool
	ContentLoaded                                 bool
	Raw                                           json.RawMessage
}

type ProviderRule struct {
	ID, Name, Source       string
	Enabled, ReadOnly      bool
	Sequence               *int
	Conditions, Exceptions []core.Filter
	Actions                []core.Action
	Raw                    json.RawMessage
}

type ProviderRuleDraft struct {
	Name                   string
	Enabled                bool
	Sequence               *int
	Conditions, Exceptions []core.Filter
	Actions                []core.Action
}

type RuleCompilation struct {
	Status         string
	Draft          ProviderRuleDraft
	LocalRemainder *core.Rule
	Reason         string
}

type RuleReceipt struct {
	ProviderID, ExecutionKey, Status string
	Raw                              json.RawMessage
}

type DesiredMailState struct {
	ProviderMessageID, ExecutionKey, ExpectedRevision string
	Read                                              *bool
	Disposition                                       string
	EnsureTags, RemoveTags                            []string
	DestinationCollectionID                           string
}

type OperationResult struct {
	ExecutionKey, Status, RemoteID, NewRevision string
	BeforeState, AfterState                     json.RawMessage
	ErrKind, SafeMessage                        string
}

type MailProvider interface {
	ID() string
	Capabilities(context.Context) (Capabilities, error)
	Account(context.Context) (ProviderAccount, error)
	ListCollections(context.Context) ([]ProviderCollection, error)
	Sync(context.Context, OpaqueCursor) (SyncPage, error)
	FetchContent(context.Context, []string) ([]ProviderContent, error)
	ListRules(context.Context) ([]ProviderRule, error)
	CompileRule(core.Rule) RuleCompilation
	CreateRule(context.Context, ProviderRuleDraft, string) (RuleReceipt, error)
	UpdateRule(context.Context, string, ProviderRuleDraft, string) (RuleReceipt, error)
	DeleteRule(context.Context, string) error
	Apply(context.Context, []DesiredMailState) ([]OperationResult, error)
}

type TaskDraft struct{ ListID, Title, Notes, DueDate string }
type TaskPatch struct{ Title, Notes, DueDate, Status string }
type EventDraft struct {
	CalendarID, Title, Description, Start, End, Timezone, Location string
	AllDay                                                         bool
}
type EventPatch EventDraft
type TaskList struct{ ID, Name string }
type Calendar struct {
	ID, Name string
	Writable bool
}
type TargetReceipt struct {
	ProviderID, ExecutionKey, Status string
	Raw                              json.RawMessage
}

type TaskTarget interface {
	ListTaskLists(context.Context) ([]TaskList, error)
	EnsureTask(context.Context, TaskDraft, string) (TargetReceipt, error)
	UpdateTask(context.Context, string, TaskPatch) (TargetReceipt, error)
	DeleteTask(context.Context, string) error
}

type CalendarTarget interface {
	ListCalendars(context.Context) ([]Calendar, error)
	EnsureEvent(context.Context, EventDraft, string) (TargetReceipt, error)
	UpdateEvent(context.Context, string, EventPatch) (TargetReceipt, error)
	DeleteEvent(context.Context, string) error
}

// FakeMailProvider is a configurable in-memory boundary fake. Nil functions
// return zero values so a test only needs to configure the behavior it uses.
type FakeMailProvider struct {
	ProviderID       string
	CapabilitiesFunc func(context.Context) (Capabilities, error)
	AccountFunc      func(context.Context) (ProviderAccount, error)
	CollectionsFunc  func(context.Context) ([]ProviderCollection, error)
	SyncFunc         func(context.Context, OpaqueCursor) (SyncPage, error)
	ContentFunc      func(context.Context, []string) ([]ProviderContent, error)
	RulesFunc        func(context.Context) ([]ProviderRule, error)
	CompileRuleFunc  func(core.Rule) RuleCompilation
	CreateRuleFunc   func(context.Context, ProviderRuleDraft, string) (RuleReceipt, error)
	UpdateRuleFunc   func(context.Context, string, ProviderRuleDraft, string) (RuleReceipt, error)
	DeleteRuleFunc   func(context.Context, string) error
	ApplyFunc        func(context.Context, []DesiredMailState) ([]OperationResult, error)
}

func (f *FakeMailProvider) ID() string { return f.ProviderID }
func (f *FakeMailProvider) Capabilities(ctx context.Context) (Capabilities, error) {
	if f.CapabilitiesFunc == nil {
		return Capabilities{}, nil
	}
	return f.CapabilitiesFunc(ctx)
}
func (f *FakeMailProvider) Account(ctx context.Context) (ProviderAccount, error) {
	if f.AccountFunc == nil {
		return ProviderAccount{}, nil
	}
	return f.AccountFunc(ctx)
}
func (f *FakeMailProvider) ListCollections(ctx context.Context) ([]ProviderCollection, error) {
	if f.CollectionsFunc == nil {
		return nil, nil
	}
	return f.CollectionsFunc(ctx)
}
func (f *FakeMailProvider) Sync(ctx context.Context, cursor OpaqueCursor) (SyncPage, error) {
	if f.SyncFunc == nil {
		return SyncPage{Done: true}, nil
	}
	return f.SyncFunc(ctx, cursor)
}
func (f *FakeMailProvider) FetchContent(ctx context.Context, ids []string) ([]ProviderContent, error) {
	if f.ContentFunc == nil {
		return nil, nil
	}
	return f.ContentFunc(ctx, ids)
}
func (f *FakeMailProvider) ListRules(ctx context.Context) ([]ProviderRule, error) {
	if f.RulesFunc == nil {
		return nil, nil
	}
	return f.RulesFunc(ctx)
}
func (f *FakeMailProvider) CompileRule(rule core.Rule) RuleCompilation {
	if f.CompileRuleFunc == nil {
		return RuleCompilation{Status: "unsupported"}
	}
	return f.CompileRuleFunc(rule)
}
func (f *FakeMailProvider) CreateRule(ctx context.Context, draft ProviderRuleDraft, key string) (RuleReceipt, error) {
	if f.CreateRuleFunc == nil {
		return RuleReceipt{}, nil
	}
	return f.CreateRuleFunc(ctx, draft, key)
}
func (f *FakeMailProvider) UpdateRule(ctx context.Context, id string, draft ProviderRuleDraft, key string) (RuleReceipt, error) {
	if f.UpdateRuleFunc == nil {
		return RuleReceipt{}, nil
	}
	return f.UpdateRuleFunc(ctx, id, draft, key)
}
func (f *FakeMailProvider) DeleteRule(ctx context.Context, id string) error {
	if f.DeleteRuleFunc == nil {
		return nil
	}
	return f.DeleteRuleFunc(ctx, id)
}
func (f *FakeMailProvider) Apply(ctx context.Context, states []DesiredMailState) ([]OperationResult, error) {
	if f.ApplyFunc == nil {
		return nil, nil
	}
	return f.ApplyFunc(ctx, states)
}

var _ MailProvider = (*FakeMailProvider)(nil)

type FakeTaskTarget struct {
	ListFunc   func(context.Context) ([]TaskList, error)
	EnsureFunc func(context.Context, TaskDraft, string) (TargetReceipt, error)
	UpdateFunc func(context.Context, string, TaskPatch) (TargetReceipt, error)
	DeleteFunc func(context.Context, string) error
}

func (f *FakeTaskTarget) ListTaskLists(ctx context.Context) ([]TaskList, error) {
	if f.ListFunc == nil {
		return nil, nil
	}
	return f.ListFunc(ctx)
}
func (f *FakeTaskTarget) EnsureTask(ctx context.Context, draft TaskDraft, key string) (TargetReceipt, error) {
	if f.EnsureFunc == nil {
		return TargetReceipt{}, nil
	}
	return f.EnsureFunc(ctx, draft, key)
}
func (f *FakeTaskTarget) UpdateTask(ctx context.Context, id string, patch TaskPatch) (TargetReceipt, error) {
	if f.UpdateFunc == nil {
		return TargetReceipt{}, nil
	}
	return f.UpdateFunc(ctx, id, patch)
}
func (f *FakeTaskTarget) DeleteTask(ctx context.Context, id string) error {
	if f.DeleteFunc == nil {
		return nil
	}
	return f.DeleteFunc(ctx, id)
}

type FakeCalendarTarget struct {
	ListFunc   func(context.Context) ([]Calendar, error)
	EnsureFunc func(context.Context, EventDraft, string) (TargetReceipt, error)
	UpdateFunc func(context.Context, string, EventPatch) (TargetReceipt, error)
	DeleteFunc func(context.Context, string) error
}

func (f *FakeCalendarTarget) ListCalendars(ctx context.Context) ([]Calendar, error) {
	if f.ListFunc == nil {
		return nil, nil
	}
	return f.ListFunc(ctx)
}
func (f *FakeCalendarTarget) EnsureEvent(ctx context.Context, draft EventDraft, key string) (TargetReceipt, error) {
	if f.EnsureFunc == nil {
		return TargetReceipt{}, nil
	}
	return f.EnsureFunc(ctx, draft, key)
}
func (f *FakeCalendarTarget) UpdateEvent(ctx context.Context, id string, patch EventPatch) (TargetReceipt, error) {
	if f.UpdateFunc == nil {
		return TargetReceipt{}, nil
	}
	return f.UpdateFunc(ctx, id, patch)
}
func (f *FakeCalendarTarget) DeleteEvent(ctx context.Context, id string) error {
	if f.DeleteFunc == nil {
		return nil
	}
	return f.DeleteFunc(ctx, id)
}

var _ TaskTarget = (*FakeTaskTarget)(nil)
var _ CalendarTarget = (*FakeCalendarTarget)(nil)
