package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	commandIntents = set("find", "view", "explain", "propose", "create_rule", "update_rule", "delete_rule", "create_schedule", "clarify")
	commandTargets = set("message", "conversation", "rule", "plan", "schedule", "task", "event")
	filterFields   = set("kind", "age_days", "read", "starred", "sender", "sender_domain", "label", "folder", "awaiting", "has_deadline", "received_at", "subject", "body")
	filterOps      = set("eq", "ne", "contains", "in", "lt", "lte", "gt", "gte")
	actionKinds    = set("archive", "trash", "mark_read", "mark_unread", "add_label", "remove_label", "add_queue", "create_task", "create_event", "create_rule")
	operationKinds = set("archive", "trash", "restore", "mark_read", "mark_unread", "add_label", "remove_label", "add_queue", "create_task", "create_event", "create_rule")
	operationRisks = set("low", "medium", "high")
	operationState = set("proposed", "approved", "running", "succeeded", "uncertain", "failed", "rejected")
	planStates     = set("draft", "frozen", "running", "completed", "partial", "cancelled")
	routeModes     = set("local_only", "external_only", "fallback", "probe")
	privacyModes   = set("local_only", "external_allowed")
	claimBases     = set("observed", "deterministic", "inferred", "user")
	messageKinds   = set("personal", "work", "transaction", "receipt", "newsletter", "notification", "alert", "other")
)

func set(values ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(values))
	for _, value := range values {
		m[value] = struct{}{}
	}
	return m
}

func requireEnum(field, value string, allowed map[string]struct{}) error {
	if _, ok := allowed[value]; !ok {
		return fmt.Errorf("%s has unsupported value %q", field, value)
	}
	return nil
}

func requireNonempty(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}

func (f Filter) Validate() error {
	if err := requireEnum("filter field", f.Field, filterFields); err != nil {
		return err
	}
	if err := requireEnum("filter operator", f.Operator, filterOps); err != nil {
		return err
	}
	return requireNonempty("filter value", f.Value)
}

func (a Action) Validate() error {
	if err := requireEnum("action kind", a.Kind, actionKinds); err != nil {
		return err
	}
	if (a.Kind == "add_label" || a.Kind == "remove_label" || a.Kind == "add_queue") && strings.TrimSpace(a.Argument) == "" {
		return fmt.Errorf("action %q requires an argument", a.Kind)
	}
	return nil
}

func validateFilters(filters []Filter) error {
	for i, filter := range filters {
		if err := filter.Validate(); err != nil {
			return fmt.Errorf("filter %d: %w", i, err)
		}
	}
	return nil
}

func validateActions(actions []Action) error {
	for i, action := range actions {
		if err := action.Validate(); err != nil {
			return fmt.Errorf("action %d: %w", i, err)
		}
	}
	return nil
}

func (c CommandDraft) Validate() error {
	if err := requireEnum("intent", c.Intent, commandIntents); err != nil {
		return err
	}
	if c.Intent == "clarify" {
		return requireNonempty("clarification", c.Clarification)
	}
	if err := requireEnum("target", c.Target, commandTargets); err != nil {
		return err
	}
	if err := validateFilters(c.Filters); err != nil {
		return err
	}
	if err := validateActions(c.Actions); err != nil {
		return err
	}
	if (c.Intent == "find" || c.Intent == "view" || c.Intent == "explain") && len(c.Actions) != 0 {
		return errors.New("read-only command cannot contain actions")
	}
	return nil
}

func DecodeCommandDraft(data []byte) (CommandDraft, error) {
	var draft CommandDraft
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil {
		return draft, fmt.Errorf("decode command draft: %w", err)
	}
	if decoder.More() {
		return draft, errors.New("decode command draft: trailing JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return draft, errors.New("decode command draft: trailing JSON")
	}
	if err := draft.Validate(); err != nil {
		return draft, err
	}
	return draft, nil
}

func (r Rule) Validate() error {
	if err := requireNonempty("rule id", r.ID); err != nil {
		return err
	}
	if err := requireNonempty("rule name", r.Name); err != nil {
		return err
	}
	if err := validateFilters(r.Conditions); err != nil {
		return fmt.Errorf("conditions: %w", err)
	}
	if err := validateFilters(r.Exceptions); err != nil {
		return fmt.Errorf("exceptions: %w", err)
	}
	if len(r.Actions) == 0 {
		return errors.New("rule requires at least one action")
	}
	return validateActions(r.Actions)
}

func (o Operation) Validate() error {
	for name, value := range map[string]string{"operation id": o.ID, "execution key": o.ExecutionKey, "target type": o.TargetType, "target id": o.TargetID} {
		if err := requireNonempty(name, value); err != nil {
			return err
		}
	}
	if err := requireEnum("operation kind", o.Kind, operationKinds); err != nil {
		return err
	}
	if err := requireEnum("operation risk", o.Risk, operationRisks); err != nil {
		return err
	}
	return requireEnum("operation status", o.Status, operationState)
}

func (p Plan) Validate() error {
	if err := requireNonempty("plan id", p.ID); err != nil {
		return err
	}
	if err := requireNonempty("plan name", p.Name); err != nil {
		return err
	}
	if err := requireEnum("plan status", p.Status, planStates); err != nil {
		return err
	}
	for i, operation := range p.Operations {
		if err := operation.Validate(); err != nil {
			return fmt.Errorf("operation %d: %w", i, err)
		}
	}
	return nil
}

func (r RoutePolicy) Validate() error {
	if err := requireEnum("route mode", r.Mode, routeModes); err != nil {
		return err
	}
	if err := requireEnum("route privacy", r.Privacy, privacyModes); err != nil {
		return err
	}
	if r.MaxExternalCalls < 0 {
		return errors.New("maximum external calls cannot be negative")
	}
	if r.ProbeRate < 0 || r.ProbeRate > 1 {
		return errors.New("probe rate must be between 0 and 1")
	}
	if r.Privacy == "local_only" && (r.Mode == "external_only" || r.Mode == "fallback" || r.Mode == "probe") {
		return errors.New("route mode permits external calls but privacy is local_only")
	}
	return nil
}

func (s Schedule) Validate() error {
	if err := requireNonempty("schedule id", s.ID); err != nil {
		return err
	}
	if err := requireNonempty("schedule name", s.Name); err != nil {
		return err
	}
	if err := requireNonempty("draft plan name", s.DraftPlanName); err != nil {
		return err
	}
	if s.EverySeconds <= 0 {
		return errors.New("schedule interval must be positive")
	}
	return s.Route.Validate()
}

func (o MessageKindOutput) Validate() error {
	if o.Abstain {
		return nil
	}
	return requireEnum("message kind", o.Kind, messageKinds)
}

func (o RequestsDatesOutput) Validate() error {
	if o.Abstain {
		return nil
	}
	for i, request := range o.Requests {
		if err := requireNonempty("request summary", request.Summary); err != nil {
			return fmt.Errorf("request %d: %w", i, err)
		}
		if err := requireNonempty("request evidence message id", request.EvidenceMessageID); err != nil {
			return fmt.Errorf("request %d: %w", i, err)
		}
	}
	return nil
}

func (o SummaryDeltaOutput) Validate() error {
	if o.Abstain {
		return nil
	}
	return requireNonempty("summary", o.Summary)
}
