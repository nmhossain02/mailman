package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	core "github.com/nmhossain02/mailman/internal/domain"
)

func TestUpdateNavigationSelectionAndDetail(t *testing.T) {
	backend := fixtureBackend()
	model := NewModel(context.Background(), backend)
	model.conversations = backend.Conversations

	model, _ = update(model, key('j', "j"))
	if model.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", model.cursor)
	}
	model, cmd := update(model, key(tea.KeyEnter, ""))
	if cmd == nil {
		t.Fatal("enter should load conversation detail")
	}
	model, _ = update(model, cmd())
	if model.view != ConversationView || model.detail.Conversation.ID != "c2" {
		t.Fatalf("wrong detail: %#v", model.detail)
	}
	model, _ = update(model, key(tea.KeyEscape, ""))
	if model.view != ConversationsView {
		t.Fatalf("escape view = %v", model.view)
	}

	model, cmd = update(model, key('2', "2"))
	model, _ = update(model, cmd())
	if model.view != RulesView || len(model.rules) != 2 {
		t.Fatalf("rules not loaded: %#v", model.rules)
	}
}

func TestViewsRenderUsefulDomainSummaries(t *testing.T) {
	backend := fixtureBackend()
	model := NewModel(context.Background(), backend)
	model.conversations, model.rules, model.schedules, model.plans = backend.Conversations, backend.Rules, backend.Schedules, backend.Plans

	checks := []struct {
		view  ViewKind
		words []string
	}{
		{ConversationsView, []string{"Conversations", "Quarterly alert", "1 messages"}},
		{RulesView, []string{"Effective rules", "Provider newsletter", "native, read-only", "archive"}},
		{SchedulesView, []string{"Schedules", "morning", "enabled"}},
		{PlansView, []string{"Plans", "cleanup", "draft", "2 operations"}},
	}
	for _, check := range checks {
		model.view = check.view
		content := model.View().Content
		for _, word := range check.words {
			if !strings.Contains(content, word) {
				t.Errorf("view %v missing %q:\n%s", check.view, word, content)
			}
		}
	}

	model.view = ConversationView
	model.detail = backend.Details["c1"]
	detail := model.View().Content
	for _, word := range []string{"Conversation: Quarterly alert", "claim kind", "task receipt", "created"} {
		if !strings.Contains(detail, word) {
			t.Errorf("detail missing %q:\n%s", word, detail)
		}
	}
}

func TestResizeClampsRenderedOutput(t *testing.T) {
	model := NewModel(context.Background(), fixtureBackend())
	model.conversations = fixtureBackend().Conversations
	model, _ = update(model, tea.WindowSizeMsg{Width: 24, Height: 4})
	lines := strings.Split(model.View().Content, "\n")
	if len(lines) > 4 {
		t.Fatalf("got %d lines, want at most 4", len(lines))
	}
	for _, line := range lines {
		if len([]rune(line)) > 24 {
			t.Fatalf("line exceeds width: %q", line)
		}
	}
}

func TestPaletteShowsClarificationAndBlocksPreview(t *testing.T) {
	model := NewModel(context.Background(), fixtureBackend())
	model.palette = true
	model.paletteText = "archive it"
	model, _ = update(model, interpretationMsg{result: Interpretation{Draft: core.CommandDraft{Intent: "clarify", Target: "conversation", Clarification: "Which conversation?"}}})
	if !strings.Contains(model.View().Content, "Clarification required: Which conversation?") {
		t.Fatalf("clarification missing:\n%s", model.View().Content)
	}
	_, cmd := update(model, key('p', "p"))
	if cmd != nil {
		t.Fatal("preview must be blocked while clarification is required")
	}
}

func TestPreviewShowsScopeGroupsOutliersAndSelection(t *testing.T) {
	model := NewModel(context.Background(), fixtureBackend())
	model.palette = true
	preview := fixtureBackend().PreviewResult
	model, _ = update(model, previewMsg{preview: preview})
	content := model.View().Content
	for _, word := range []string{"Scope: 42 targets", "newsletters: 40", "samples: Daily News", "Outliers: 1", "freeze and approve"} {
		if !strings.Contains(content, word) {
			t.Errorf("preview missing %q:\n%s", word, content)
		}
	}
	firstID := preview.Plan.Operations[0].ID
	if !model.selected[firstID] {
		t.Fatal("operations should start approved")
	}
	model, _ = update(model, key(tea.KeySpace, " "))
	if model.selected[firstID] {
		t.Fatal("space should reject selected operation")
	}
}

func TestApplyImpossibleUntilFrozenApproved(t *testing.T) {
	backend := fixtureBackend()
	model := NewModel(context.Background(), backend)
	model.palette, model.phase, model.preview = true, palettePreview, backend.PreviewResult

	_, cmd := update(model, key('a', "a"))
	if cmd != nil || containsCall(backend.Calls, "apply") {
		t.Fatal("draft plan reached ApplyPlan")
	}

	model.preview.Plan.Status = "frozen"
	model, cmd = update(model, key('a', "a"))
	if cmd == nil {
		t.Fatal("frozen plan should be applicable")
	}
	model, _ = update(model, cmd())
	if !containsCall(backend.Calls, "apply") || model.status != "plan applied" {
		t.Fatalf("apply result missing: calls=%v status=%q", backend.Calls, model.status)
	}
}

func TestSavingEvalLabelIsExplicit(t *testing.T) {
	backend := fixtureBackend()
	model := NewModel(context.Background(), backend)
	model.palette, model.phase = true, paletteInterpreted
	model.interpretation = Interpretation{TraceID: "trace-1", Draft: core.CommandDraft{Intent: "find", Target: "conversation"}, Canonical: json.RawMessage(`{"intent":"find"}`)}

	model, _ = update(model, key('e', "e"))
	if len(backend.SavedCorrections) != 0 {
		t.Fatal("ordinary editing silently saved a label")
	}
	model.phase = paletteInterpreted
	model, cmd := update(model, key('l', "l"))
	if cmd == nil {
		t.Fatal("explicit label action returned no command")
	}
	model, _ = update(model, cmd())
	if len(backend.SavedCorrections) != 1 || backend.SavedCorrections[0].TraceID != "trace-1" {
		t.Fatalf("labels = %#v", backend.SavedCorrections)
	}
}

func update(model Model, msg tea.Msg) (Model, tea.Cmd) {
	updated, cmd := model.Update(msg)
	return updated.(Model), cmd
}

func key(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Text: text})
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

func fixtureBackend() *FakeBackend {
	now := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)
	operations := []core.Operation{{ID: "op1", Kind: "archive", TargetID: "c1"}, {ID: "op2", Kind: "mark_read", TargetID: "c2"}}
	return &FakeBackend{
		Conversations: []core.Conversation{
			{ID: "c1", Subject: "Quarterly alert", MessageIDs: []string{"m1"}, LastMessageAt: now},
			{ID: "c2", Subject: "Daily News", MessageIDs: []string{"m2", "m3"}, LastMessageAt: now},
		},
		Details: map[string]ConversationDetail{
			"c1": {Conversation: core.Conversation{ID: "c1", Subject: "Quarterly alert"}, Messages: []core.Message{{ID: "m1", Sender: "alerts@example.com", Subject: "Quarterly alert"}}, Claims: []core.Claim{{Name: "kind", Value: json.RawMessage(`"alert"`)}}, Receipts: []Receipt{{Kind: "task", Summary: "Review alert", Status: "created"}}},
			"c2": {Conversation: core.Conversation{ID: "c2", Subject: "Daily News"}},
		},
		Rules:         []core.Rule{{ID: "r1", Name: "Provider newsletter", Source: "native", ReadOnly: true, Enabled: true, Actions: []core.Action{{Kind: "archive"}}}, {ID: "r2", Name: "Local alerts", Source: "local", Enabled: true}},
		Schedules:     []core.Schedule{{ID: "s1", Name: "morning", Enabled: true, EverySeconds: 3600}},
		Plans:         []core.Plan{{ID: "p1", Name: "cleanup", Status: "draft", Operations: operations}},
		PreviewResult: PlanPreview{Plan: core.Plan{ID: "p1", Name: "cleanup", Status: "draft", Operations: operations}, ScopeCount: 42, Groups: []PlanGroup{{Name: "newsletters", Count: 40, Samples: []string{"Daily News"}}}, Outliers: []core.Operation{{ID: "out1", Kind: "trash"}}},
		Frozen:        core.Plan{ID: "p1", Name: "cleanup", Status: "frozen", Operations: operations},
		Applied:       core.Plan{ID: "p1", Name: "cleanup", Status: "applied", Operations: operations},
	}
}
