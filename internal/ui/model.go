package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/nabeel/mailman/internal/core"
)

type ViewKind int

const (
	ConversationsView ViewKind = iota
	ConversationView
	RulesView
	SchedulesView
	PlansView
)

type palettePhase int

const (
	paletteInput palettePhase = iota
	paletteInterpreted
	palettePreview
)

type Model struct {
	backend Backend
	ctx     context.Context
	width   int
	height  int
	view    ViewKind
	cursor  int

	conversations []core.Conversation
	detail        ConversationDetail
	rules         []core.Rule
	schedules     []core.Schedule
	plans         []core.Plan

	palette        bool
	paletteText    string
	phase          palettePhase
	interpretation Interpretation
	preview        PlanPreview
	selected       map[string]bool
	status         string
	err            error
}

func NewModel(ctx context.Context, backend Backend) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	return Model{backend: backend, ctx: ctx, selected: make(map[string]bool)}
}

func (m Model) Init() tea.Cmd { return m.loadConversations() }

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case conversationsMsg:
		m.conversations, m.err = msg.items, msg.err
		return m, nil
	case detailMsg:
		m.detail, m.err = msg.detail, msg.err
		if msg.err == nil {
			m.view, m.cursor = ConversationView, 0
		}
		return m, nil
	case rulesMsg:
		m.rules, m.err = msg.items, msg.err
		return m, nil
	case schedulesMsg:
		m.schedules, m.err = msg.items, msg.err
		return m, nil
	case plansMsg:
		m.plans, m.err = msg.items, msg.err
		return m, nil
	case interpretationMsg:
		m.interpretation, m.err = msg.result, msg.err
		if msg.err == nil {
			m.phase = paletteInterpreted
		}
		return m, nil
	case previewMsg:
		m.preview, m.err = msg.preview, msg.err
		if msg.err == nil {
			m.phase, m.cursor = palettePreview, 0
			m.selected = make(map[string]bool)
			for _, operation := range m.preview.Plan.Operations {
				m.selected[operation.ID] = true
			}
		}
		return m, nil
	case planMsg:
		m.err = msg.err
		if msg.err == nil {
			m.preview.Plan = msg.plan
			m.status = msg.status
		}
		return m, nil
	case labelSavedMsg:
		m.err = msg.err
		if msg.err == nil {
			m.status = "correction saved as eval label"
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m Model) updateKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.palette {
		return m.updatePalette(key)
	}
	stroke := key.String()
	switch stroke {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "/":
		m.palette, m.phase, m.paletteText, m.status, m.err = true, paletteInput, "", "", nil
	case "1":
		m.view, m.cursor = ConversationsView, 0
		return m, m.loadConversations()
	case "2":
		m.view, m.cursor = RulesView, 0
		return m, m.loadRules()
	case "3":
		m.view, m.cursor = SchedulesView, 0
		return m, m.loadSchedules()
	case "4":
		m.view, m.cursor = PlansView, 0
		return m, m.loadPlans()
	case "esc":
		if m.view == ConversationView {
			m.view, m.cursor = ConversationsView, 0
		}
	case "enter":
		if m.view == ConversationsView && len(m.conversations) > 0 {
			return m, m.loadDetail(m.conversations[m.cursor].ID)
		}
	case "j", "down":
		m.cursor = min(m.cursor+1, m.itemCount()-1)
	case "k", "up":
		m.cursor = max(m.cursor-1, 0)
	case "tab":
		m.view = nextListView(m.view)
		m.cursor = 0
		return m, m.loadCurrent()
	}
	return m, nil
}

func (m Model) updatePalette(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	stroke := key.String()
	if stroke == "esc" {
		m.palette, m.err = false, nil
		return m, nil
	}
	switch m.phase {
	case paletteInput:
		switch stroke {
		case "enter":
			if strings.TrimSpace(m.paletteText) != "" {
				return m, m.interpret(m.paletteText)
			}
		case "backspace":
			if chars := []rune(m.paletteText); len(chars) > 0 {
				m.paletteText = string(chars[:len(chars)-1])
			}
		default:
			if key.Text != "" {
				m.paletteText += key.Text
			}
		}
	case paletteInterpreted:
		switch stroke {
		case "e":
			m.phase = paletteInput
		case "p":
			if m.interpretation.Draft.Clarification == "" {
				return m, m.loadPreview(m.interpretation.Draft)
			}
		case "l":
			if m.interpretation.TraceID != "" {
				return m, m.saveLabel()
			}
		}
	case palettePreview:
		switch stroke {
		case "j", "down":
			m.cursor = min(m.cursor+1, len(m.preview.Plan.Operations)-1)
		case "k", "up":
			m.cursor = max(m.cursor-1, 0)
		case " ", "space", "x":
			if operation, ok := m.currentOperation(); ok {
				m.selected[operation.ID] = !m.selected[operation.ID]
			}
		case "f":
			if m.preview.Plan.ID != "" && m.preview.Plan.Status == "draft" {
				return m, m.freezeSelections(m.preview.Plan.ID)
			}
		case "a":
			// Freezing is the explicit approval boundary. Draft and applied plans
			// can never reach the application backend from this key path.
			if m.preview.Plan.ID != "" && m.preview.Plan.Status == "frozen" {
				return m, m.apply(m.preview.Plan.ID)
			}
		}
	}
	return m, nil
}

func nextListView(view ViewKind) ViewKind {
	switch view {
	case ConversationsView, ConversationView:
		return RulesView
	case RulesView:
		return SchedulesView
	case SchedulesView:
		return PlansView
	default:
		return ConversationsView
	}
}

func (m Model) itemCount() int {
	switch m.view {
	case ConversationsView:
		return len(m.conversations)
	case RulesView:
		return len(m.rules)
	case SchedulesView:
		return len(m.schedules)
	case PlansView:
		return len(m.plans)
	default:
		return len(m.detail.Messages)
	}
}

func (m Model) currentOperation() (core.Operation, bool) {
	if m.cursor < 0 || m.cursor >= len(m.preview.Plan.Operations) {
		return core.Operation{}, false
	}
	return m.preview.Plan.Operations[m.cursor], true
}

func (m Model) translationContext() core.TranslationContext {
	result := core.TranslationContext{Now: time.Now()}
	if m.view == ConversationView {
		result.SelectedType, result.SelectedID = "conversation", m.detail.Conversation.ID
	}
	for _, rule := range m.rules {
		result.RuleNames = append(result.RuleNames, rule.Name)
	}
	for _, schedule := range m.schedules {
		result.ScheduleNames = append(result.ScheduleNames, schedule.Name)
	}
	return result
}

type conversationsMsg struct {
	items []core.Conversation
	err   error
}
type detailMsg struct {
	detail ConversationDetail
	err    error
}
type rulesMsg struct {
	items []core.Rule
	err   error
}
type schedulesMsg struct {
	items []core.Schedule
	err   error
}
type plansMsg struct {
	items []core.Plan
	err   error
}
type interpretationMsg struct {
	result Interpretation
	err    error
}
type previewMsg struct {
	preview PlanPreview
	err     error
}
type planMsg struct {
	plan   core.Plan
	status string
	err    error
}
type labelSavedMsg struct{ err error }

func (m Model) loadConversations() tea.Cmd {
	return func() tea.Msg {
		v, e := m.backend.ListConversations(m.ctx, core.CommandDraft{})
		return conversationsMsg{v, e}
	}
}
func (m Model) loadDetail(id string) tea.Cmd {
	return func() tea.Msg { v, e := m.backend.GetConversation(m.ctx, id); return detailMsg{v, e} }
}
func (m Model) loadRules() tea.Cmd {
	return func() tea.Msg { v, e := m.backend.ListRules(m.ctx); return rulesMsg{v, e} }
}
func (m Model) loadSchedules() tea.Cmd {
	return func() tea.Msg { v, e := m.backend.ListSchedules(m.ctx); return schedulesMsg{v, e} }
}
func (m Model) loadPlans() tea.Cmd {
	return func() tea.Msg { v, e := m.backend.ListPlans(m.ctx); return plansMsg{v, e} }
}
func (m Model) interpret(text string) tea.Cmd {
	ctx := m.translationContext()
	return func() tea.Msg { v, e := m.backend.Interpret(m.ctx, text, ctx); return interpretationMsg{v, e} }
}
func (m Model) loadPreview(draft core.CommandDraft) tea.Cmd {
	return func() tea.Msg { v, e := m.backend.Preview(m.ctx, draft); return previewMsg{v, e} }
}
func (m Model) freeze(id string) tea.Cmd {
	return func() tea.Msg {
		v, e := m.backend.FreezePlan(m.ctx, id)
		return planMsg{v, "plan frozen and approved", e}
	}
}
func (m Model) freezeSelections(id string) tea.Cmd {
	approved, rejected := []string{}, []string{}
	for _, op := range m.preview.Plan.Operations {
		if m.selected[op.ID] {
			approved = append(approved, op.ID)
		} else {
			rejected = append(rejected, op.ID)
		}
	}
	return func() tea.Msg {
		if decisions, ok := m.backend.(PlanDecisionBackend); ok {
			if _, err := decisions.DecidePlan(m.ctx, id, approved, rejected); err != nil {
				return planMsg{err: err}
			}
		}
		v, e := m.backend.FreezePlan(m.ctx, id)
		return planMsg{v, "plan frozen with reviewed selections", e}
	}
}
func (m Model) apply(id string) tea.Cmd {
	return func() tea.Msg { v, e := m.backend.ApplyPlan(m.ctx, id); return planMsg{v, "plan applied", e} }
}

func (m Model) saveLabel() tea.Cmd {
	expected := m.interpretation.Canonical
	if len(expected) == 0 {
		expected, _ = json.Marshal(m.interpretation.Draft)
	}
	label := core.EvalLabel{CaseID: "command:" + m.interpretation.TraceID, TraceID: m.interpretation.TraceID, Source: "user_correction", ExpectedJSON: expected, CreatedAt: time.Now()}
	return func() tea.Msg { return labelSavedMsg{m.backend.SaveEvalLabel(m.ctx, label)} }
}

func (m Model) loadCurrent() tea.Cmd {
	switch m.view {
	case RulesView:
		return m.loadRules()
	case SchedulesView:
		return m.loadSchedules()
	case PlansView:
		return m.loadPlans()
	default:
		return m.loadConversations()
	}
}

func (m Model) View() tea.View {
	var body string
	switch m.view {
	case ConversationView:
		body = m.renderConversation()
	case RulesView:
		body = m.renderRules()
	case SchedulesView:
		body = m.renderSchedules()
	case PlansView:
		body = m.renderPlans()
	default:
		body = m.renderConversations()
	}
	content := "Mailman  [1] Conversations  [2] Rules  [3] Schedules  [4] Plans\n" + body
	if m.palette {
		content += "\n\n" + m.renderPalette()
	} else {
		content += "\n\n/ command  tab next  j/k move  q quit"
	}
	if m.err != nil {
		content += "\nError: " + m.err.Error()
	}
	if m.status != "" {
		content += "\n" + m.status
	}
	if m.width > 0 {
		content = clampLines(content, m.width, m.height)
	}
	return tea.NewView(content)
}

func (m Model) renderConversations() string {
	lines := []string{"Conversations"}
	for index, conversation := range m.conversations {
		lines = append(lines, row(index == m.cursor, fmt.Sprintf("%s  %d messages  %s", conversation.Subject, len(conversation.MessageIDs), conversation.LastMessageAt.Format("2006-01-02"))))
	}
	if len(m.conversations) == 0 {
		lines = append(lines, "No conversations")
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderConversation() string {
	lines := []string{"Conversation: " + m.detail.Conversation.Subject}
	for index, message := range m.detail.Messages {
		lines = append(lines, row(index == m.cursor, fmt.Sprintf("%s: %s", message.Sender, message.Subject)))
	}
	for _, claim := range m.detail.Claims {
		lines = append(lines, "claim "+claim.Name+": "+string(claim.Value))
	}
	for _, receipt := range m.detail.Receipts {
		lines = append(lines, fmt.Sprintf("%s receipt: %s [%s]", receipt.Kind, receipt.Summary, receipt.Status))
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderRules() string {
	lines := []string{"Effective rules"}
	for index, rule := range m.rules {
		state := "enabled"
		if !rule.Enabled {
			state = "disabled"
		}
		origin := rule.Source
		if rule.ReadOnly {
			origin += ", read-only"
		}
		lines = append(lines, row(index == m.cursor, fmt.Sprintf("%s [%s; %s] → %s", rule.Name, origin, state, actionsText(rule.Actions))))
	}
	if len(m.rules) == 0 {
		lines = append(lines, "No rules")
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderSchedules() string {
	lines := []string{"Schedules"}
	for index, schedule := range m.schedules {
		state := "disabled"
		if schedule.Enabled {
			state = "enabled"
		}
		last := "never"
		if schedule.LastRunAt != nil {
			last = schedule.LastRunAt.Format(time.RFC3339)
		}
		lines = append(lines, row(index == m.cursor, fmt.Sprintf("%s [%s] every %s; last %s", schedule.Name, state, (time.Duration(schedule.EverySeconds)*time.Second).String(), last)))
	}
	if len(m.schedules) == 0 {
		lines = append(lines, "No schedules")
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderPlans() string {
	lines := []string{"Plans"}
	for index, plan := range m.plans {
		lines = append(lines, row(index == m.cursor, fmt.Sprintf("%s [%s] %d operations", plan.Name, plan.Status, len(plan.Operations))))
	}
	if len(m.plans) == 0 {
		lines = append(lines, "No plans")
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderPalette() string {
	lines := []string{"Command: " + m.paletteText}
	switch m.phase {
	case paletteInput:
		lines = append(lines, "Type a natural command, then Enter")
	case paletteInterpreted:
		draft := m.interpretation.Draft
		lines = append(lines, "Compiled: "+draft.Intent+" "+draft.Target, filtersText(draft.Filters), "Actions: "+actionsText(draft.Actions))
		if draft.Clarification != "" {
			lines = append(lines, "Clarification required: "+draft.Clarification, "[e] correct")
		} else {
			lines = append(lines, "[p] preview scope  [e] correct  [l] save correction as eval label")
		}
	case palettePreview:
		lines = append(lines, fmt.Sprintf("Scope: %d targets (shown before plan creation)", m.preview.ScopeCount))
		for _, group := range m.preview.Groups {
			lines = append(lines, fmt.Sprintf("%s: %d; samples: %s", group.Name, group.Count, strings.Join(group.Samples, ", ")))
		}
		if len(m.preview.Outliers) > 0 {
			lines = append(lines, fmt.Sprintf("Outliers: %d", len(m.preview.Outliers)))
		}
		for index, operation := range m.preview.Plan.Operations {
			mark := " "
			if m.selected[operation.ID] {
				mark = "x"
			}
			lines = append(lines, row(index == m.cursor, fmt.Sprintf("[%s] %s %s", mark, operation.Kind, operation.TargetID)))
		}
		if m.preview.Plan.Status == "draft" {
			lines = append(lines, "[space] approve/reject item  [f] freeze and approve plan")
		}
		if m.preview.Plan.Status == "frozen" {
			lines = append(lines, "Plan is frozen and approved. [a] apply")
		}
	}
	return strings.Join(lines, "\n")
}

func row(selected bool, value string) string {
	if selected {
		return "> " + value
	}
	return "  " + value
}
func actionsText(actions []core.Action) string {
	if len(actions) == 0 {
		return "none"
	}
	values := make([]string, 0, len(actions))
	for _, action := range actions {
		value := action.Kind
		if action.Argument != "" {
			value += "(" + action.Argument + ")"
		}
		values = append(values, value)
	}
	return strings.Join(values, ", ")
}
func filtersText(filters []core.Filter) string {
	if len(filters) == 0 {
		return "Filters: none"
	}
	values := make([]string, 0, len(filters))
	for _, filter := range filters {
		values = append(values, filter.Field+" "+filter.Operator+" "+filter.Value)
	}
	return "Filters: " + strings.Join(values, " AND ")
}

func clampLines(value string, width, height int) string {
	lines := strings.Split(value, "\n")
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	for index, line := range lines {
		if width > 1 && len([]rune(line)) > width {
			chars := []rune(line)
			lines[index] = string(chars[:width-1]) + "…"
		}
	}
	return strings.Join(lines, "\n")
}
