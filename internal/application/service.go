package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	policy "github.com/nmhossain02/mailman/internal/automation"
	core "github.com/nmhossain02/mailman/internal/domain"
)

type WorkbenchRepository interface {
	Conversations(context.Context, string) ([]core.Conversation, error)
	Conversation(context.Context, string) (core.Conversation, error)
	ConversationMessages(context.Context, string) ([]core.Message, error)
	Claims(context.Context, string, string) ([]core.Claim, error)
	Rules(context.Context) ([]core.Rule, error)
	Schedules(context.Context) ([]core.Schedule, error)
	Plans(context.Context) ([]core.Plan, error)
	SaveCommandCorrection(context.Context, core.CommandCorrection) error
}

type CommandTranslator interface {
	Translate(context.Context, string, core.TranslationContext) (core.CommandDraft, error)
}

type Service struct {
	Store      WorkbenchRepository
	Plans      PlanService
	Translator CommandTranslator
	Now        func() time.Time
}

func (s *Service) ListConversations(ctx context.Context, _ core.CommandDraft) ([]core.Conversation, error) {
	return s.Store.Conversations(ctx, "")
}

func (s *Service) GetConversation(ctx context.Context, id string) (ConversationDetail, error) {
	conversation, err := s.Store.Conversation(ctx, id)
	if err != nil {
		return ConversationDetail{}, err
	}
	messages, err := s.Store.ConversationMessages(ctx, id)
	if err != nil {
		return ConversationDetail{}, err
	}
	claims, err := s.Store.Claims(ctx, "conversation", id)
	return ConversationDetail{Conversation: conversation, Messages: messages, Claims: claims}, err
}

func (s *Service) ListRules(ctx context.Context) ([]core.Rule, error) {
	return s.Store.Rules(ctx)
}

func (s *Service) ListSchedules(ctx context.Context) ([]core.Schedule, error) {
	return s.Store.Schedules(ctx)
}

func (s *Service) ListPlans(ctx context.Context) ([]core.Plan, error) {
	return s.Store.Plans(ctx)
}

func (s *Service) CommandContext(ctx context.Context) core.TranslationContext {
	commandContext := core.TranslationContext{Now: s.now(), Timezone: s.now().Location().String()}
	if rules, err := s.Store.Rules(ctx); err == nil {
		for _, rule := range rules {
			commandContext.RuleNames = append(commandContext.RuleNames, rule.Name)
		}
	}
	if schedules, err := s.Store.Schedules(ctx); err == nil {
		for _, schedule := range schedules {
			commandContext.ScheduleNames = append(commandContext.ScheduleNames, schedule.Name)
		}
	}
	return commandContext
}

func (s *Service) Interpret(ctx context.Context, text string, commandContext core.TranslationContext) (Interpretation, error) {
	if s.Translator == nil {
		return Interpretation{}, fmt.Errorf("command translator is not configured")
	}
	draft, err := s.Translator.Translate(ctx, text, commandContext)
	if err != nil {
		return Interpretation{}, err
	}
	canonical, _ := json.Marshal(draft)
	return Interpretation{Draft: draft, TraceID: commandTraceID(text), Canonical: canonical}, nil
}

func (s *Service) Preview(ctx context.Context, draft core.CommandDraft) (PlanPreview, error) {
	conversations, err := s.Store.Conversations(ctx, "")
	if err != nil {
		return PlanPreview{}, err
	}
	rule := core.Rule{ID: "interactive", Name: "interactive", Enabled: true, Conditions: draft.Filters, Actions: draft.Actions}
	var candidates []policy.Candidate
	scope := 0
	for _, conversation := range conversations {
		if draft.Reference != "" && draft.Reference != conversation.ID {
			continue
		}
		messages, readErr := s.Store.ConversationMessages(ctx, conversation.ID)
		if readErr != nil {
			return PlanPreview{}, readErr
		}
		derived := policy.DeriveContext(messages, "", s.now(), false)
		for _, message := range messages {
			derived.Message = message
			matched := policy.Evaluate([]core.Rule{rule}, derived)
			if len(matched) > 0 {
				scope++
			}
			candidates = append(candidates, matched...)
		}
	}
	plan, err := s.Plans.Draft(ctx, "interactive review", policy.Resolve(candidates, nil))
	if err != nil {
		return PlanPreview{}, err
	}
	return PlanPreview{Plan: plan, ScopeCount: scope, Groups: groupOperations(plan.Operations)}, nil
}

func (s *Service) FreezePlan(ctx context.Context, id string) (core.Plan, error) {
	plan, err := s.findPlan(ctx, id)
	if err != nil {
		return plan, err
	}
	return s.Plans.Freeze(ctx, plan)
}

func (s *Service) DecidePlan(ctx context.Context, id string, approved, rejected []string) (core.Plan, error) {
	plan, err := s.findPlan(ctx, id)
	if err != nil {
		return plan, err
	}
	approve, reject := map[string]bool{}, map[string]bool{}
	for _, value := range approved {
		approve[value] = true
	}
	for _, value := range rejected {
		reject[value] = true
	}
	return s.Plans.Decide(ctx, plan, approve, reject)
}

func (s *Service) ApplyPlan(ctx context.Context, id string) (core.Plan, error) {
	plan, err := s.findPlan(ctx, id)
	if err != nil {
		return plan, err
	}
	return s.Plans.Apply(ctx, plan)
}

func (s *Service) SaveCommandCorrection(ctx context.Context, label core.CommandCorrection) error {
	return s.Store.SaveCommandCorrection(ctx, label)
}

func (s *Service) findPlan(ctx context.Context, id string) (core.Plan, error) {
	plans, err := s.Store.Plans(ctx)
	if err != nil {
		return core.Plan{}, err
	}
	for _, plan := range plans {
		if plan.ID == id {
			return plan, nil
		}
	}
	return core.Plan{}, fmt.Errorf("plan %q not found", id)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func groupOperations(operations []core.Operation) []PlanGroup {
	grouped := map[string][]core.Operation{}
	for _, operation := range operations {
		grouped[operation.Kind] = append(grouped[operation.Kind], operation)
	}
	names := make([]string, 0, len(grouped))
	for name := range grouped {
		names = append(names, name)
	}
	sort.Strings(names)
	groups := make([]PlanGroup, 0, len(names))
	for _, name := range names {
		group := PlanGroup{Name: name, Count: len(grouped[name]), Operations: grouped[name]}
		for index := 0; index < len(grouped[name]) && index < 3; index++ {
			group.Samples = append(group.Samples, grouped[name][index].TargetID)
		}
		groups = append(groups, group)
	}
	return groups
}

func commandTraceID(text string) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("command-%x", sum[:8])
}

var _ Workbench = (*Service)(nil)
var _ PlanDecisionBackend = (*Service)(nil)
