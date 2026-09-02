package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nmhossain02/mailman/internal/application/journal"
	"github.com/nmhossain02/mailman/internal/application/provider"
	policy "github.com/nmhossain02/mailman/internal/automation"
	core "github.com/nmhossain02/mailman/internal/domain"
)

var ErrStaleRevision = errors.New("message changed since the plan was prepared")
var ErrApprovalRequired = errors.New("operation requires explicit approval")

type PlanRepository interface {
	SavePlan(context.Context, core.Plan) error
	Plans(context.Context) ([]core.Plan, error)
	Message(context.Context, string) (core.Message, error)
	BeginOperation(context.Context, string, string, time.Time) (journal.Entry, bool, error)
	FinishOperation(context.Context, string, string, json.RawMessage, time.Time) error
}
type PlanService struct {
	Store     PlanRepository
	Mail      map[string]provider.MailProvider
	Tasks     map[string]provider.TaskTarget
	Calendars map[string]provider.CalendarTarget
	Now       func() time.Time
}

type operationArgs struct {
	Value       string          `json:"value,omitempty"`
	AccountID   string          `json:"account_id,omitempty"`
	BeforeState json.RawMessage `json:"before_state,omitempty"`
}

func (s PlanService) Draft(ctx context.Context, name string, candidates []policy.Candidate) (core.Plan, error) {
	if s.Store == nil {
		return core.Plan{}, fmt.Errorf("plan service is not configured")
	}
	existing, _ := s.Store.Plans(ctx)
	statusByKey := map[string]string{}
	var current *core.Plan
	for i := range existing {
		if existing[i].Name == name && existing[i].Status == "draft" {
			current = &existing[i]
			for _, op := range existing[i].Operations {
				statusByKey[op.ExecutionKey] = op.Status
			}
			break
		}
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	p := core.Plan{ID: stableID("plan", name), Name: name, Status: "draft", CreatedAt: now}
	if current != nil {
		p.ID = current.ID
		p.CreatedAt = current.CreatedAt
	}
	seen := map[string]bool{}
	for _, c := range candidates {
		targetType := c.TargetType
		if c.Action.Kind == "create_task" {
			targetType = "task"
		}
		if c.Action.Kind == "create_event" {
			targetType = "event"
		}
		args, _ := json.Marshal(operationArgs{Value: c.Action.Argument})
		key := stableID("operation", name, targetType, c.TargetID, c.Action.Kind, c.Action.Argument, c.ExpectedRevision)
		if seen[key] {
			continue
		}
		seen[key] = true
		status := "proposed"
		if old := statusByKey[key]; old != "" {
			status = old
		}
		risk := "low"
		if c.Action.Kind == "create_task" || c.Action.Kind == "create_event" || c.Action.Kind == "create_rule" {
			risk = "high"
		}
		p.Operations = append(p.Operations, core.Operation{ID: stableID("op", key), ExecutionKey: key, TargetType: targetType, TargetID: c.TargetID, Kind: c.Action.Kind, Risk: risk, Arguments: args, ExpectedRevision: c.ExpectedRevision, Status: status})
	}
	sort.Slice(p.Operations, func(i, j int) bool { return p.Operations[i].ExecutionKey < p.Operations[j].ExecutionKey })
	if err := p.Validate(); err != nil {
		return core.Plan{}, err
	}
	return p, s.Store.SavePlan(ctx, p)
}

func (s PlanService) Freeze(ctx context.Context, p core.Plan) (core.Plan, error) {
	if p.Status != "draft" {
		return p, fmt.Errorf("only draft plans can be frozen")
	}
	p.Status = "frozen"
	return p, s.Store.SavePlan(ctx, p)
}

func (s PlanService) Decide(ctx context.Context, p core.Plan, approve, reject map[string]bool) (core.Plan, error) {
	if p.Status != "draft" && p.Status != "frozen" {
		return p, fmt.Errorf("plan is not reviewable")
	}
	for i := range p.Operations {
		op := &p.Operations[i]
		if approve[op.ID] {
			op.Status = "approved"
		}
		if reject[op.ID] {
			op.Status = "rejected"
		}
	}
	return p, s.Store.SavePlan(ctx, p)
}

func (s PlanService) Apply(ctx context.Context, p core.Plan) (core.Plan, error) {
	if p.Status != "frozen" {
		return p, fmt.Errorf("plan must be frozen before apply")
	}
	p.Status = "running"
	now := s.now()
	groups := map[string][]int{}
	for i := range p.Operations {
		op := p.Operations[i]
		if op.Status != "approved" {
			continue
		}
		groups[op.TargetType+"\x00"+op.TargetID] = append(groups[op.TargetType+"\x00"+op.TargetID], i)
	}
	var applyErr error
	for _, indexes := range groups {
		kind := p.Operations[indexes[0]].TargetType
		var err error
		switch kind {
		case "message", "conversation":
			err = s.applyMail(ctx, &p, indexes, now)
		case "task":
			err = s.applyTask(ctx, &p, indexes, now)
		case "event":
			err = s.applyEvent(ctx, &p, indexes, now)
		default:
			err = fmt.Errorf("unsupported operation target %q", kind)
		}
		if err != nil {
			applyErr = errors.Join(applyErr, err)
		}
	}
	succeeded, failed := 0, 0
	for _, op := range p.Operations {
		if op.Status == "succeeded" {
			succeeded++
		}
		if op.Status == "failed" || op.Status == "uncertain" {
			failed++
		}
	}
	switch {
	case failed == 0:
		p.Status = "completed"
	case succeeded > 0:
		p.Status = "partial"
	default:
		p.Status = "partial"
	}
	if err := s.Store.SavePlan(ctx, p); err != nil {
		return p, errors.Join(applyErr, err)
	}
	return p, applyErr
}

func (s PlanService) applyMail(ctx context.Context, p *core.Plan, indexes []int, now time.Time) error {
	first := p.Operations[indexes[0]]
	m, err := s.Store.Message(ctx, first.TargetID)
	if err != nil {
		return s.fail(p, indexes, err)
	}
	for _, i := range indexes {
		if p.Operations[i].ExpectedRevision != "" && p.Operations[i].ExpectedRevision != m.Revision {
			return s.fail(p, indexes, ErrStaleRevision)
		}
	}
	d := provider.DesiredMailState{ProviderMessageID: m.ProviderID, ExpectedRevision: m.Revision, ExecutionKey: groupKey(*p, indexes)}
	for _, i := range indexes {
		op := p.Operations[i]
		var a operationArgs
		_ = json.Unmarshal(op.Arguments, &a)
		switch op.Kind {
		case "archive", "trash", "restore":
			d.Disposition = op.Kind
		case "mark_read":
			v := true
			d.Read = &v
		case "mark_unread":
			v := false
			d.Read = &v
		case "add_label":
			d.EnsureTags = append(d.EnsureTags, a.Value)
		case "remove_label":
			d.RemoveTags = append(d.RemoveTags, a.Value)
		default:
			return s.fail(p, indexes, fmt.Errorf("unsupported mail action %q", op.Kind))
		}
	}
	mail := s.Mail[m.AccountID]
	if mail == nil {
		return s.fail(p, indexes, fmt.Errorf("mail provider unavailable for account %s", m.AccountID))
	}
	request, _ := json.Marshal(d)
	journal, exists, err := s.Store.BeginOperation(ctx, d.ExecutionKey, hash(request), now)
	if err != nil {
		return s.fail(p, indexes, err)
	}
	if exists {
		if journal.State == "succeeded" {
			setStatus(p, indexes, journal.State)
			return nil
		}
	}
	results, callErr := mail.Apply(ctx, []provider.DesiredMailState{d})
	if len(results) == 0 {
		state := "failed"
		if callErr != nil {
			state = "uncertain"
		}
		response, _ := json.Marshal(map[string]string{"error": safe(callErr)})
		_ = s.Store.FinishOperation(ctx, d.ExecutionKey, state, response, now)
		setStatus(p, indexes, state)
		return callErr
	}
	r := results[0]
	state := r.Status
	if state == "" {
		state = "failed"
	}
	response, _ := json.Marshal(r)
	_ = s.Store.FinishOperation(ctx, d.ExecutionKey, state, response, now)
	setStatus(p, indexes, state)
	if state != "succeeded" {
		return fmt.Errorf("provider operation %s: %s", state, r.SafeMessage)
	}
	return callErr
}

func (s PlanService) applyTask(ctx context.Context, p *core.Plan, indexes []int, now time.Time) error {
	op := p.Operations[indexes[0]]
	if op.Status != "approved" {
		return ErrApprovalRequired
	}
	var a operationArgs
	if err := json.Unmarshal(op.Arguments, &a); err != nil {
		return s.fail(p, indexes, err)
	}
	var d provider.TaskDraft
	if err := json.Unmarshal([]byte(a.Value), &d); err != nil {
		d.Title = a.Value
	}
	target := s.Tasks[a.AccountID]
	if target == nil {
		if source, e := s.Store.Message(ctx, op.TargetID); e == nil {
			target = s.Tasks[source.AccountID]
		}
	}
	if target == nil && len(s.Tasks) == 1 {
		for _, v := range s.Tasks {
			target = v
		}
	}
	if target == nil {
		return s.fail(p, indexes, fmt.Errorf("task target unavailable"))
	}
	request, _ := json.Marshal(d)
	journal, exists, err := s.Store.BeginOperation(ctx, op.ExecutionKey, hash(request), now)
	if err != nil {
		return s.fail(p, indexes, err)
	}
	if exists && journal.State == "succeeded" {
		setStatus(p, indexes, journal.State)
		return nil
	}
	receipt, err := target.EnsureTask(ctx, d, op.ExecutionKey)
	return s.finishTarget(ctx, p, indexes, receipt, err, now)
}
func (s PlanService) applyEvent(ctx context.Context, p *core.Plan, indexes []int, now time.Time) error {
	op := p.Operations[indexes[0]]
	if op.Status != "approved" {
		return ErrApprovalRequired
	}
	var a operationArgs
	if err := json.Unmarshal(op.Arguments, &a); err != nil {
		return s.fail(p, indexes, err)
	}
	var d provider.EventDraft
	if err := json.Unmarshal([]byte(a.Value), &d); err != nil {
		d.Title = a.Value
	}
	target := s.Calendars[a.AccountID]
	if target == nil {
		if source, e := s.Store.Message(ctx, op.TargetID); e == nil {
			target = s.Calendars[source.AccountID]
		}
	}
	if target == nil && len(s.Calendars) == 1 {
		for _, v := range s.Calendars {
			target = v
		}
	}
	if target == nil {
		return s.fail(p, indexes, fmt.Errorf("calendar target unavailable"))
	}
	request, _ := json.Marshal(d)
	journal, exists, err := s.Store.BeginOperation(ctx, op.ExecutionKey, hash(request), now)
	if err != nil {
		return s.fail(p, indexes, err)
	}
	if exists && journal.State == "succeeded" {
		setStatus(p, indexes, journal.State)
		return nil
	}
	receipt, err := target.EnsureEvent(ctx, d, op.ExecutionKey)
	return s.finishTarget(ctx, p, indexes, receipt, err, now)
}
func (s PlanService) finishTarget(ctx context.Context, p *core.Plan, indexes []int, r provider.TargetReceipt, callErr error, now time.Time) error {
	state := r.Status
	if state == "" {
		if callErr != nil {
			state = "uncertain"
		} else {
			state = "succeeded"
		}
	}
	response, _ := json.Marshal(r)
	_ = s.Store.FinishOperation(ctx, p.Operations[indexes[0]].ExecutionKey, state, response, now)
	setStatus(p, indexes, state)
	return callErr
}
func (s PlanService) fail(p *core.Plan, indexes []int, err error) error {
	setStatus(p, indexes, "failed")
	return err
}
func (s PlanService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func setStatus(p *core.Plan, indexes []int, status string) {
	for _, i := range indexes {
		p.Operations[i].Status = status
	}
}
func groupKey(p core.Plan, indexes []int) string {
	v := p.ID
	for _, i := range indexes {
		v += "\x00" + p.Operations[i].ExecutionKey
	}
	return stableID("compound", v)
}
func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func stableID(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
func safe(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Undo creates a reviewable inverse plan. Restore is used instead of permanent
// provider deletion; external task/event creation is intentionally not undone
// without an explicit future reconciliation UI.
func (s PlanService) Undo(ctx context.Context, p core.Plan) (core.Plan, error) {
	var candidates []policy.Candidate
	for _, op := range p.Operations {
		if op.Status != "succeeded" {
			continue
		}
		inverse := ""
		switch op.Kind {
		case "archive", "trash":
			inverse = "restore"
		case "mark_read":
			inverse = "mark_unread"
		case "mark_unread":
			inverse = "mark_read"
		case "add_label":
			inverse = "remove_label"
		case "remove_label":
			inverse = "add_label"
		}
		if inverse != "" {
			var a operationArgs
			_ = json.Unmarshal(op.Arguments, &a)
			candidates = append(candidates, policy.Candidate{TargetType: op.TargetType, TargetID: op.TargetID, ExpectedRevision: "", Source: "user", Action: core.Action{Kind: inverse, Argument: a.Value}})
		}
	}
	return s.Draft(ctx, "undo "+p.Name, candidates)
}
