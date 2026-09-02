// Package automation implements portable, review-first rules, plans, and schedules. It
// owns no timer and never applies a plan.
package automation

import (
	"context"
	"errors"
	"fmt"
	"time"

	core "github.com/nmhossain02/mailman/internal/domain"
)

var ErrNotFound = errors.New("schedule not found")

type Store interface {
	Schedules(context.Context) ([]core.Schedule, error)
	SaveSchedule(context.Context, core.Schedule) error
}
type Preparer interface {
	Prepare(context.Context, core.Schedule) (core.Plan, error)
}
type Result struct {
	Schedule core.Schedule
	Plan     core.Plan
	Skipped  bool
}
type Runner struct {
	Store    Store
	Preparer Preparer
	Now      func() time.Time
}

// Run prepares a rolling draft and returns. It intentionally has no provider
// Apply dependency, making background mutation impossible by construction.
func (r Runner) Run(ctx context.Context, name string) (Result, error) {
	if r.Store == nil || r.Preparer == nil {
		return Result{}, fmt.Errorf("schedule runner is not configured")
	}
	schedules, err := r.Store.Schedules(ctx)
	if err != nil {
		return Result{}, err
	}
	var found *core.Schedule
	for i := range schedules {
		if schedules[i].Name == name || schedules[i].ID == name {
			found = &schedules[i]
			break
		}
	}
	if found == nil {
		return Result{}, ErrNotFound
	}
	s := *found
	if !s.Enabled {
		return Result{Schedule: s, Skipped: true}, nil
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	if s.LastRunAt != nil && now.Sub(*s.LastRunAt) < time.Duration(s.EverySeconds)*time.Second {
		return Result{Schedule: s, Skipped: true}, nil
	}
	// Recurring preparation is local-only unless the user explicitly stored a
	// stricter local-only policy. External fallback/probe never leaks into cron.
	s.Route = core.RoutePolicy{Mode: "local_only", Privacy: "local_only", MaxExternalCalls: 0}
	plan, err := r.Preparer.Prepare(ctx, s)
	if err != nil {
		return Result{Schedule: s}, err
	}
	s.LastRunAt = &now
	if err = r.Store.SaveSchedule(ctx, s); err != nil {
		return Result{Schedule: s, Plan: plan}, err
	}
	return Result{Schedule: s, Plan: plan}, nil
}
