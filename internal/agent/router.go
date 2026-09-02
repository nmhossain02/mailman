package agent

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	core "github.com/nmhossain02/mailman/internal/domain"
)

type RouteRequest struct {
	Policy            core.RoutePolicy
	Task              Task
	Input             jsonRaw
	TargetID, TraceID string
	// Deterministic is an already-computed, validated result. When non-nil no
	// production model call is needed; probes may still compare it externally.
	Deterministic *TaskResult
}

type jsonRaw = []byte

type RouteResult struct {
	Selected   TaskResult
	Comparison *TaskResult
	Traces     []InferenceTrace
}

type Router struct {
	Local, External Backend
	externalCalls   atomic.Int64
}

func (r *Router) Route(ctx context.Context, request RouteRequest) (RouteResult, error) {
	if err := request.Policy.Validate(); err != nil {
		return RouteResult{}, err
	}
	started := time.Now()
	if request.Deterministic != nil {
		result := RouteResult{Selected: *request.Deterministic}
		result.Traces = append(result.Traces, makeTrace(request, nil, "deterministic", "production", "deterministic_match", true, started, nil))
		if request.Policy.Mode == "probe" && shouldProbe(request.TargetID, request.Policy.ProbeSeed, request.Policy.ProbeRate) {
			comparison, trace, err := r.callExternal(ctx, request, "probe", "stable_probe")
			result.Traces = append(result.Traces, trace)
			if err == nil {
				result.Comparison = &comparison
			}
		}
		return result, nil
	}

	switch request.Policy.Mode {
	case "external_only":
		result, trace, err := r.callExternal(ctx, request, "production", "explicit_external")
		return RouteResult{Selected: result, Traces: []InferenceTrace{trace}}, err
	case "local_only", "fallback", "probe":
		local, localTrace, localErr := call(ctx, r.Local, request, "local", "production", "local_first", true)
		out := RouteResult{Selected: local, Traces: []InferenceTrace{localTrace}}
		validLocal := localErr == nil && local.Outcome != "abstain"
		if request.Policy.Mode == "local_only" {
			return out, localErr
		}
		if request.Policy.Mode == "fallback" && !validLocal {
			out.Traces[0].Selected = false
			external, trace, externalErr := r.callExternal(ctx, request, "fallback", "local_deferred")
			out.Traces = append(out.Traces, trace)
			out.Selected = external
			return out, externalErr
		}
		if request.Policy.Mode == "probe" && validLocal && shouldProbe(request.TargetID, request.Policy.ProbeSeed, request.Policy.ProbeRate) {
			external, trace, externalErr := r.callExternal(ctx, request, "probe", "stable_probe")
			out.Traces = append(out.Traces, trace)
			if externalErr == nil {
				out.Comparison = &external
			}
		}
		return out, localErr
	default:
		return RouteResult{}, errors.New("unsupported route mode")
	}
}

func (r *Router) callExternal(ctx context.Context, request RouteRequest, role, reason string) (TaskResult, InferenceTrace, error) {
	if request.Policy.Privacy != "external_allowed" {
		err := &InferenceError{Kind: "privacy_blocked", SafeMessage: "external inference is blocked by privacy policy"}
		return TaskResult{}, makeTrace(request, r.External, "external", role, reason, role == "production" || role == "fallback", time.Now(), err), err
	}
	if request.Policy.MaxExternalCalls == 0 || r.externalCalls.Add(1) > int64(request.Policy.MaxExternalCalls) {
		if request.Policy.MaxExternalCalls > 0 {
			r.externalCalls.Add(-1)
		}
		err := &InferenceError{Kind: "budget_exhausted", SafeMessage: "external inference budget exhausted"}
		return TaskResult{Outcome: "abstain"}, makeTrace(request, r.External, "external", role, "external_budget_exhausted", false, time.Now(), err), err
	}
	return call(ctx, r.External, request, "external", role, reason, role != "probe")
}

func call(ctx context.Context, backend Backend, request RouteRequest, class, role, reason string, selected bool) (TaskResult, InferenceTrace, error) {
	started := time.Now()
	if backend == nil {
		err := &InferenceError{Kind: "unavailable", SafeMessage: class + " inference backend unavailable"}
		return TaskResult{}, makeTrace(request, nil, class, role, reason, selected, started, err), err
	}
	attempt := 1
	ctx = withAttemptObserver(ctx, func(current int) { attempt = current })
	result, err := RunTask(ctx, backend, request.Task, request.Input, request.TraceID)
	trace := makeTrace(request, backend, class, role, reason, selected, started, err)
	trace.Attempt = attempt
	trace.Model, trace.ModelRevision = result.Raw.Model, result.Raw.ModelRevision
	trace.InputTokens, trace.CachedInputTokens, trace.OutputTokens = result.Raw.InputTokens, result.Raw.CachedInputTokens, result.Raw.OutputTokens
	trace.LoadMS, trace.PromptMS, trace.GenerationMS = result.Raw.LoadMS, result.Raw.PromptMS, result.Raw.GenerationMS
	if result.Raw.WallMS != nil {
		trace.WallMS = result.Raw.WallMS
	}
	if result.Output != nil {
		trace.CanonicalOutput, _ = json.Marshal(result.Output)
	}
	if err == nil {
		trace.Outcome = result.Outcome
	}
	return result, trace, err
}

func makeTrace(request RouteRequest, backend Backend, class, role, reason string, selected bool, started time.Time, err error) InferenceTrace {
	inputHash, _ := InputHash(request.Input)
	trace := InferenceTrace{ID: request.TraceID, TargetID: request.TargetID, TaskName: request.Task.Name, TaskVersion: request.Task.Version, PromptVersion: request.Task.PromptVersion, SchemaVersion: request.Task.SchemaVersion, InputHash: inputHash, InputSnapshot: append([]byte(nil), request.Input...), BackendClass: class, RouteMode: request.Policy.Mode, RouteRole: role, RouteReason: reason, Selected: selected, Attempt: 1, StartedAt: started, CompletedAt: time.Now()}
	if backend != nil {
		trace.BackendID = backend.ID()
	}
	trace.WallMS = durationPtr(trace.CompletedAt.Sub(started))
	if err != nil {
		trace.Outcome = "error"
		var ie *InferenceError
		if errors.As(err, &ie) {
			trace.ErrorKind = ie.Kind
		}
		return trace
	}
	trace.Outcome = "ok"
	return trace
}

func durationPtr(d time.Duration) *int64 { ms := d.Milliseconds(); return &ms }

func shouldProbe(target, seed string, rate float64) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	sum := sha256.Sum256([]byte(seed + "\x00" + target))
	value := float64(binary.BigEndian.Uint64(sum[:8])) / float64(^uint64(0))
	return value < rate
}

func ShouldProbe(target, seed string, rate float64) bool { return shouldProbe(target, seed, rate) }
