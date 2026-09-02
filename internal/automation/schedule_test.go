package automation

import (
	"context"
	"testing"
	"time"

	core "github.com/nmhossain02/mailman/internal/domain"
)

type memoryStore struct{ s []core.Schedule }

func (m *memoryStore) Schedules(context.Context) ([]core.Schedule, error) {
	return append([]core.Schedule(nil), m.s...), nil
}
func (m *memoryStore) SaveSchedule(_ context.Context, s core.Schedule) error { m.s[0] = s; return nil }

type prep struct {
	calls int
	route core.RoutePolicy
}

func (p *prep) Prepare(_ context.Context, s core.Schedule) (core.Plan, error) {
	p.calls++
	p.route = s.Route
	return core.Plan{ID: "p", Name: s.DraftPlanName, Status: "draft", CreatedAt: time.Now()}, nil
}

func TestRunIsLocalOnlyAndDeduplicatesInterval(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st := &memoryStore{s: []core.Schedule{{ID: "s", Name: "daily", DraftPlanName: "inbox", Enabled: true, EverySeconds: 3600, Route: core.RoutePolicy{Mode: "fallback", Privacy: "external_allowed", MaxExternalCalls: 5}}}}
	p := &prep{}
	r := Runner{Store: st, Preparer: p, Now: func() time.Time { return now }}
	got, err := r.Run(context.Background(), "daily")
	if err != nil || got.Skipped {
		t.Fatalf("first run: %#v %v", got, err)
	}
	if p.route.Mode != "local_only" || p.route.MaxExternalCalls != 0 {
		t.Fatalf("background route leaked external: %#v", p.route)
	}
	got, err = r.Run(context.Background(), "daily")
	if err != nil || !got.Skipped || p.calls != 1 {
		t.Fatalf("repeat not skipped: %#v calls=%d err=%v", got, p.calls, err)
	}
}
