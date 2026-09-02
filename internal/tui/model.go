package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	core "github.com/nmhossain02/mailman/internal/domain"
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
	renderVersion  uint64
	renderCache    *viewCache
}

type viewCache struct {
	version uint64
	view    tea.View
	builds  uint64
}

func NewModel(ctx context.Context, backend Backend) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	return Model{backend: backend, ctx: ctx, selected: make(map[string]bool), renderVersion: 1, renderCache: &viewCache{}}
}

func (m Model) Init() tea.Cmd { return m.loadConversations() }

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		if m.width == msg.Width && m.height == msg.Height {
			return m, nil
		}
		m.width, m.height = msg.Width, msg.Height
		return m.visualChange(), nil
	case conversationsMsg:
		m.conversations, m.err = msg.items, msg.err
		return m.visualChange(), nil
	case detailMsg:
		m.detail, m.err = msg.detail, msg.err
		if msg.err == nil {
			m.view, m.cursor = ConversationView, 0
		}
		return m.visualChange(), nil
	case rulesMsg:
		m.rules, m.err = msg.items, msg.err
		return m.visualChange(), nil
	case schedulesMsg:
		m.schedules, m.err = msg.items, msg.err
		return m.visualChange(), nil
	case plansMsg:
		m.plans, m.err = msg.items, msg.err
		return m.visualChange(), nil
	case interpretationMsg:
		m.interpretation, m.err = msg.result, msg.err
		if msg.err == nil {
			m.phase = paletteInterpreted
		}
		return m.visualChange(), nil
	case previewMsg:
		m.preview, m.err = msg.preview, msg.err
		if msg.err == nil {
			m.phase, m.cursor = palettePreview, 0
			m.selected = make(map[string]bool)
			for _, operation := range m.preview.Plan.Operations {
				m.selected[operation.ID] = true
			}
		}
		return m.visualChange(), nil
	case planMsg:
		m.err = msg.err
		if msg.err == nil {
			m.preview.Plan = msg.plan
			m.status = msg.status
		}
		return m.visualChange(), nil
	case labelSavedMsg:
		m.err = msg.err
		if msg.err == nil {
			m.status = "correction saved as eval label"
		}
		return m.visualChange(), nil
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m Model) visualChange() Model {
	m.renderVersion++
	return m
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
		return m.visualChange(), nil
	case "1":
		m.view, m.cursor = ConversationsView, 0
		return m.visualChange(), m.loadConversations()
	case "2":
		m.view, m.cursor = RulesView, 0
		return m.visualChange(), m.loadRules()
	case "3":
		m.view, m.cursor = SchedulesView, 0
		return m.visualChange(), m.loadSchedules()
	case "4":
		m.view, m.cursor = PlansView, 0
		return m.visualChange(), m.loadPlans()
	case "esc":
		if m.view == ConversationView {
			m.view, m.cursor = ConversationsView, 0
			return m.visualChange(), nil
		}
	case "enter":
		if m.view == ConversationsView && len(m.conversations) > 0 {
			return m, m.loadDetail(m.conversations[m.cursor].ID)
		}
	case "j", "down":
		next := boundedCursor(m.cursor+1, m.itemCount())
		if next != m.cursor {
			m.cursor = next
			return m.visualChange(), nil
		}
	case "k", "up":
		next := boundedCursor(m.cursor-1, m.itemCount())
		if next != m.cursor {
			m.cursor = next
			return m.visualChange(), nil
		}
	case "tab":
		m.view = nextListView(m.view)
		m.cursor = 0
		return m.visualChange(), m.loadCurrent()
	}
	return m, nil
}

func (m Model) updatePalette(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	stroke := key.String()
	if stroke == "esc" {
		m.palette, m.err = false, nil
		return m.visualChange(), nil
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
				return m.visualChange(), nil
			}
		default:
			if key.Text != "" {
				m.paletteText += key.Text
				return m.visualChange(), nil
			}
		}
	case paletteInterpreted:
		switch stroke {
		case "e":
			m.phase = paletteInput
			return m.visualChange(), nil
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
			next := boundedCursor(m.cursor+1, len(m.preview.Plan.Operations))
			if next != m.cursor {
				m.cursor = next
				return m.visualChange(), nil
			}
		case "k", "up":
			next := boundedCursor(m.cursor-1, len(m.preview.Plan.Operations))
			if next != m.cursor {
				m.cursor = next
				return m.visualChange(), nil
			}
		case " ", "space", "x":
			if operation, ok := m.currentOperation(); ok {
				m.selected[operation.ID] = !m.selected[operation.ID]
				return m.visualChange(), nil
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
	label := core.CommandCorrection{CaseID: "command:" + m.interpretation.TraceID, TraceID: m.interpretation.TraceID, Source: "user_correction", ExpectedJSON: expected, CreatedAt: time.Now()}
	return func() tea.Msg { return labelSavedMsg{m.backend.SaveCommandCorrection(m.ctx, label)} }
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
	if m.renderCache != nil && m.renderCache.version == m.renderVersion {
		return m.renderCache.view
	}
	view := m.buildView()
	if m.renderCache != nil {
		m.renderCache.version = m.renderVersion
		m.renderCache.view = view
		m.renderCache.builds++
	}
	return view
}

func (m Model) buildView() tea.View {
	height := m.height
	if height <= 0 {
		height = 24
	}
	extraLines := 0
	if m.err != nil {
		extraLines++
	}
	if m.status != "" {
		extraLines++
	}
	bodyLines := max(1, height-3-extraLines)
	controls := "/ command  tab next  j/k move  q quit"
	if m.palette {
		// The palette is the active surface. Keep a one-line body landmark and
		// give the remaining terminal height to the palette so it cannot be
		// appended below an off-screen conversation list.
		bodyLines = 1
		controls = m.renderPalette(max(1, height-3-extraLines))
	}
	body := m.renderCurrent(bodyLines)
	content := "Mailman  [1] Conversations  [2] Rules  [3] Schedules  [4] Plans\n" + body
	content += "\n\n" + controls
	if m.err != nil {
		content += "\nError: " + m.err.Error()
	}
	if m.status != "" {
		content += "\n" + m.status
	}
	content = clampLines(content, m.width, height)
	return tea.NewView(content)
}

func (m Model) renderCurrent(maxLines int) string {
	var body string
	switch m.view {
	case ConversationView:
		body = m.renderConversation(maxLines)
	case RulesView:
		body = m.renderRules(maxLines)
	case SchedulesView:
		body = m.renderSchedules(maxLines)
	case PlansView:
		body = m.renderPlans(maxLines)
	default:
		body = m.renderConversations(maxLines)
	}
	return body
}

func (m Model) renderConversations(maxLines int) string {
	lines := []string{"Conversations"}
	start, end := visibleBounds(len(m.conversations), m.cursor, maxLines-1)
	for index := start; index < end; index++ {
		conversation := m.conversations[index]
		count := conversation.MessageCount
		if count == 0 {
			count = len(conversation.MessageIDs)
		}
		lines = append(lines, row(index == m.cursor, fmt.Sprintf("%s  %d messages  %s", conversation.Subject, count, conversation.LastMessageAt.Format("2006-01-02"))))
	}
	if len(m.conversations) == 0 && maxLines > 1 {
		lines = append(lines, "No conversations")
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderConversation(maxLines int) string {
	lines := []string{"Conversation: " + m.detail.Conversation.Subject}
	start, end := visibleBounds(len(m.detail.Messages), m.cursor, maxLines-1)
	for index := start; index < end; index++ {
		message := m.detail.Messages[index]
		lines = append(lines, row(index == m.cursor, fmt.Sprintf("%s: %s", message.Sender, message.Subject)))
	}
	for _, claim := range m.detail.Claims {
		if len(lines) >= maxLines {
			break
		}
		lines = append(lines, "claim "+claim.Name+": "+string(claim.Value))
	}
	for _, receipt := range m.detail.Receipts {
		if len(lines) >= maxLines {
			break
		}
		lines = append(lines, fmt.Sprintf("%s receipt: %s [%s]", receipt.Kind, receipt.Summary, receipt.Status))
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderRules(maxLines int) string {
	lines := []string{"Effective rules"}
	start, end := visibleBounds(len(m.rules), m.cursor, maxLines-1)
	for index := start; index < end; index++ {
		rule := m.rules[index]
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
	if len(m.rules) == 0 && maxLines > 1 {
		lines = append(lines, "No rules")
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderSchedules(maxLines int) string {
	lines := []string{"Schedules"}
	start, end := visibleBounds(len(m.schedules), m.cursor, maxLines-1)
	for index := start; index < end; index++ {
		schedule := m.schedules[index]
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
	if len(m.schedules) == 0 && maxLines > 1 {
		lines = append(lines, "No schedules")
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderPlans(maxLines int) string {
	lines := []string{"Plans"}
	start, end := visibleBounds(len(m.plans), m.cursor, maxLines-1)
	for index := start; index < end; index++ {
		plan := m.plans[index]
		lines = append(lines, row(index == m.cursor, fmt.Sprintf("%s [%s] %d operations", plan.Name, plan.Status, len(plan.Operations))))
	}
	if len(m.plans) == 0 && maxLines > 1 {
		lines = append(lines, "No plans")
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderPalette(maxLines int) string {
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
			if len(lines) >= maxLines-2 {
				break
			}
			lines = append(lines, fmt.Sprintf("%s: %d; samples: %s", group.Name, group.Count, strings.Join(group.Samples, ", ")))
		}
		if len(m.preview.Outliers) > 0 && len(lines) < maxLines-1 {
			lines = append(lines, fmt.Sprintf("Outliers: %d", len(m.preview.Outliers)))
		}
		operationLines := max(0, maxLines-len(lines)-1)
		start, end := visibleBounds(len(m.preview.Plan.Operations), m.cursor, operationLines)
		for index := start; index < end; index++ {
			operation := m.preview.Plan.Operations[index]
			mark := " "
			if m.selected[operation.ID] {
				mark = "x"
			}
			lines = append(lines, row(index == m.cursor, fmt.Sprintf("[%s] %s %s", mark, operation.Kind, operation.TargetID)))
		}
		if m.preview.Plan.Status == "draft" && len(lines) < maxLines {
			lines = append(lines, "[space] approve/reject item  [f] freeze and approve plan")
		}
		if m.preview.Plan.Status == "frozen" && len(lines) < maxLines {
			lines = append(lines, "Plan is frozen and approved. [a] apply")
		}
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}

func visibleBounds(total, cursor, limit int) (int, int) {
	if total <= 0 || limit <= 0 {
		return 0, 0
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= total {
		cursor = total - 1
	}
	start := 0
	if cursor >= limit {
		start = cursor - limit + 1
	}
	end := min(total, start+limit)
	return start, end
}

func boundedCursor(cursor, total int) int {
	if total <= 0 || cursor < 0 {
		return 0
	}
	return min(cursor, total-1)
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
