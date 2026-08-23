package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nabeel/mailman/internal/core"
	"github.com/nabeel/mailman/internal/inference"
)

type InferenceStore interface {
	Cache(context.Context, string) (json.RawMessage, bool, error)
	PutCache(context.Context, string, json.RawMessage, time.Time) error
	PutTrace(context.Context, core.InferenceTrace) error
}

type PrimitiveRunner struct {
	Router                    *inference.Router
	Store                     InferenceStore
	LocalModel, LocalRevision string
	Now                       func() time.Time
}

func (r PrimitiveRunner) Run(ctx context.Context, taskName, targetID string, input json.RawMessage, route core.RoutePolicy) (inference.TaskResult, error) {
	if r.Router == nil || r.Store == nil {
		return inference.TaskResult{}, fmt.Errorf("primitive runner is not configured")
	}
	if r.Router.Local == nil && route.Mode != "external_only" {
		return inference.TaskResult{}, fmt.Errorf("local inference backend is not configured")
	}
	task, err := inference.BuiltinTask(taskName, r.LocalModel)
	if err != nil {
		return inference.TaskResult{}, err
	}
	key, err := inference.CacheKey(r.Router.Local.ID(), r.LocalRevision, task, input)
	if err != nil {
		return inference.TaskResult{}, err
	}
	if route.Mode != "external_only" {
		cached, ok, e := r.Store.Cache(ctx, key)
		if e != nil {
			return inference.TaskResult{}, e
		}
		if ok {
			decoded, e := task.Decode(cached)
			if e == nil {
				now := time.Now().UTC()
				if r.Now != nil {
					now = r.Now().UTC()
				}
				inputHash, _ := inference.InputHash(input)
				trace := core.InferenceTrace{ID: stableID("trace", "cache", taskName, targetID, key), TargetID: targetID, TaskName: task.Name, TaskVersion: task.Version, PromptVersion: task.PromptVersion, SchemaVersion: task.SchemaVersion, InputHash: inputHash, InputSnapshot: append(json.RawMessage(nil), input...), CanonicalOutput: append(json.RawMessage(nil), cached...), BackendID: r.Router.Local.ID(), BackendClass: "local", Model: r.LocalModel, ModelRevision: r.LocalRevision, RouteMode: route.Mode, RouteRole: "production", RouteReason: "cache_hit", Outcome: "ok", Selected: true, CacheHit: true, Attempt: 0, StartedAt: now, CompletedAt: now}
				if e = r.Store.PutTrace(ctx, trace); e != nil {
					return inference.TaskResult{}, e
				}
				return inference.TaskResult{Outcome: "ok", Output: decoded, Raw: inference.ProviderResult{RawOutput: cached, ModelRevision: r.LocalRevision}}, nil
			}
		}
	}
	traceID := stableID("trace", taskName, targetID, key)
	routed, err := r.Router.Route(ctx, inference.RouteRequest{Policy: route, Task: task, Input: input, TargetID: targetID, TraceID: traceID})
	for i, t := range routed.Traces {
		t.ID = fmt.Sprintf("%s-%d", traceID, i)
		if e := r.Store.PutTrace(ctx, t); e != nil && err == nil {
			err = e
		}
	}
	if err == nil && routed.Selected.Raw.RawOutput != nil && routed.Selected.Raw.ModelRevision == r.LocalRevision && route.Mode != "external_only" {
		now := time.Now()
		if r.Now != nil {
			now = r.Now()
		}
		if e := r.Store.PutCache(ctx, key, routed.Selected.Raw.RawOutput, now); e != nil {
			return inference.TaskResult{}, e
		}
	}
	return routed.Selected, err
}

// ProcessConversation runs separately benchmarkable primitives rather than a
// single opaque mega-prompt. Command translation is invoked interactively and
// is intentionally not part of background conversation processing.
type SemanticProcessor struct{ Runner PrimitiveRunner }

func (s SemanticProcessor) ProcessConversation(ctx context.Context, c core.Conversation, messages []core.Message, route core.RoutePolicy) error {
	input, _ := json.Marshal(struct {
		Conversation core.Conversation `json:"conversation"`
		Messages     []core.Message    `json:"messages"`
	}{c, messages})
	for _, name := range []string{"message_kind", "requests_dates", "summary_delta"} {
		if _, err := s.Runner.Run(ctx, name, c.ID, input, route); err != nil {
			return err
		}
	}
	return nil
}
func stableID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
