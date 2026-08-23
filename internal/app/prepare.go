package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/nmhossain02/mailman/internal/core"
	"github.com/nmhossain02/mailman/internal/policy"
	"github.com/nmhossain02/mailman/internal/provider"
)

type PreparationRepository interface {
	Rules(context.Context) ([]core.Rule, error)
	Conversations(context.Context, string) ([]core.Conversation, error)
	ConversationMessages(context.Context, string) ([]core.Message, error)
}

// ScheduledPreparer is the application half of `schedule run`: synchronize
// incrementally, evaluate cheap deterministic rules, and upsert one rolling
// review plan. It exposes no Apply call.
type ScheduledPreparer struct {
	Sync  SyncService
	Plans PlanService
	Store PreparationRepository
	Mail  map[string]provider.MailProvider
	Now   func() time.Time
}

func (p ScheduledPreparer) Prepare(ctx context.Context, s core.Schedule) (core.Plan, error) {
	if p.Store == nil {
		return core.Plan{}, fmt.Errorf("scheduled preparer is not configured")
	}
	for _, account := range s.AccountIDs {
		mail := p.Mail[account]
		if mail == nil {
			return core.Plan{}, fmt.Errorf("mail provider unavailable for account %s", account)
		}
		if _, err := p.Sync.Sync(ctx, account, "mail", mail, s.Route); err != nil {
			return core.Plan{}, err
		}
	}
	rules, err := p.Store.Rules(ctx)
	if err != nil {
		return core.Plan{}, err
	}
	if len(s.RuleIDs) > 0 {
		rules = slices.DeleteFunc(rules, func(r core.Rule) bool { return !slices.Contains(s.RuleIDs, r.ID) })
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	var candidates []policy.Candidate
	for _, account := range s.AccountIDs {
		conversations, e := p.Store.Conversations(ctx, account)
		if e != nil {
			return core.Plan{}, e
		}
		for _, c := range conversations {
			messages, e := p.Store.ConversationMessages(ctx, c.ID)
			if e != nil {
				return core.Plan{}, e
			}
			for _, m := range messages {
				derived := policy.DeriveContext(messages, "", now, false)
				derived.Message = m
				candidates = append(candidates, policy.Evaluate(rules, derived)...)
			}
		}
	}
	return p.Plans.Draft(ctx, s.DraftPlanName, policy.Resolve(candidates, nil))
}
