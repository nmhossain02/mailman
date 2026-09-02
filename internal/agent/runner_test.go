package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	core "github.com/nmhossain02/mailman/internal/domain"
)

func TestRunTaskStrictOutput(t *testing.T) {
	task, err := BuiltinTask("message_kind", "tiny")
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"Kind":"newsletter","EvidenceMessageIDs":["m1"],"Abstain":false}`
	tests := []struct {
		name, output string
		wantErr      bool
	}{
		{"valid", valid, false},
		{"malformed", `{`, true},
		{"missing required field", `{"Kind":"newsletter","EvidenceMessageIDs":[]}`, true},
		{"missing required semantic field", `{"Kind":"","EvidenceMessageIDs":[],"Abstain":false}`, true},
		{"extra field", `{"Kind":"newsletter","EvidenceMessageIDs":[],"Abstain":false,"oops":1}`, true},
		{"trailing JSON", valid + ` {}`, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := &FakeBackend{BackendID: "fake", InferFunc: func(context.Context, Request) (ProviderResult, error) {
				return ProviderResult{RawOutput: json.RawMessage(test.output)}, nil
			}}
			_, err := RunTask(context.Background(), backend, task, json.RawMessage(`{}`), "trace")
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestRouterModesAndProbe(t *testing.T) {
	task, _ := BuiltinTask("message_kind", "tiny")
	result := `{"Kind":"newsletter","EvidenceMessageIDs":[],"Abstain":false}`
	localCalls, externalCalls := 0, 0
	backend := func(id string, calls *int) Backend {
		return &FakeBackend{BackendID: id, InferFunc: func(context.Context, Request) (ProviderResult, error) {
			*calls++
			return ProviderResult{RawOutput: json.RawMessage(result)}, nil
		}}
	}
	router := &Router{Local: backend("local", &localCalls), External: backend("external", &externalCalls)}
	policy := core.RoutePolicy{Mode: "fallback", Privacy: "external_allowed", MaxExternalCalls: 2}
	got, err := router.Route(context.Background(), RouteRequest{Policy: policy, Task: task, Input: []byte(`{}`), TargetID: "x"})
	if err != nil || got.Selected.Outcome != "ok" || localCalls != 1 || externalCalls != 0 {
		t.Fatalf("local success route = %#v, %v, calls %d/%d", got, err, localCalls, externalCalls)
	}

	probePolicy := core.RoutePolicy{Mode: "probe", Privacy: "external_allowed", MaxExternalCalls: 2, ProbeRate: 1, ProbeSeed: "fixed"}
	got, err = router.Route(context.Background(), RouteRequest{Policy: probePolicy, Task: task, Input: []byte(`{}`), TargetID: "x"})
	if err != nil || got.Comparison == nil || got.Selected.Raw.RawOutput == nil {
		t.Fatalf("probe = %#v, %v", got, err)
	}
	if localCalls != 2 || externalCalls != 1 {
		t.Fatalf("calls %d/%d", localCalls, externalCalls)
	}
}

func TestRouterFallbackBudgetAndPrivacy(t *testing.T) {
	task, _ := BuiltinTask("message_kind", "tiny")
	local := &FakeBackend{BackendID: "local", InferFunc: func(context.Context, Request) (ProviderResult, error) {
		return ProviderResult{}, &InferenceError{Kind: "invalid_output"}
	}}
	externalCalls := 0
	external := &FakeBackend{BackendID: "external", InferFunc: func(context.Context, Request) (ProviderResult, error) { externalCalls++; return ProviderResult{}, nil }}
	router := &Router{Local: local, External: external}
	_, err := router.Route(context.Background(), RouteRequest{Policy: core.RoutePolicy{Mode: "fallback", Privacy: "external_allowed", MaxExternalCalls: 0}, Task: task, Input: []byte(`{}`)})
	var ie *InferenceError
	if !errors.As(err, &ie) || ie.Kind != "budget_exhausted" || externalCalls != 0 {
		t.Fatalf("err=%v calls=%d", err, externalCalls)
	}
	_, err = router.Route(context.Background(), RouteRequest{Policy: core.RoutePolicy{Mode: "external_only", Privacy: "local_only", MaxExternalCalls: 1}, Task: task, Input: []byte(`{}`)})
	if err == nil || externalCalls != 0 {
		t.Fatalf("privacy err=%v calls=%d", err, externalCalls)
	}
}

func TestCacheKeyStabilityAndInvalidation(t *testing.T) {
	task, _ := BuiltinTask("message_kind", "m")
	a, _ := CacheKey("ollama", "digest", task, json.RawMessage(`{"b":2,"a":1}`))
	b, _ := CacheKey("ollama", "digest", task, json.RawMessage(`{ "a":1, "b":2 }`))
	if a != b {
		t.Fatal("equivalent JSON produced different keys")
	}
	task.PromptVersion = "2"
	c, _ := CacheKey("ollama", "digest", task, json.RawMessage(`{"a":1,"b":2}`))
	if a == c {
		t.Fatal("prompt version did not invalidate key")
	}
	d, _ := CacheKey("ollama", "other", task, json.RawMessage(`{"a":1,"b":2}`))
	if c == d {
		t.Fatal("model revision did not invalidate key")
	}
}

func TestStableProbe(t *testing.T) {
	first := ShouldProbe("case-1", "seed", .4)
	for i := 0; i < 100; i++ {
		if ShouldProbe("case-1", "seed", .4) != first {
			t.Fatal("probe choice changed")
		}
	}
	if ShouldProbe("x", "s", 0) || !ShouldProbe("x", "s", 1) {
		t.Fatal("probe boundary rates")
	}
}

func TestDeterministicResultSkipsProductionModels(t *testing.T) {
	calls := 0
	backend := &FakeBackend{BackendID: "never", InferFunc: func(context.Context, Request) (ProviderResult, error) {
		calls++
		return ProviderResult{}, nil
	}}
	task, _ := BuiltinTask("message_kind", "tiny")
	want := TaskResult{Outcome: "ok", Output: MessageKindResult{Kind: "newsletter"}}
	got, err := (&Router{Local: backend, External: backend}).Route(context.Background(), RouteRequest{
		Policy: core.RoutePolicy{Mode: "fallback", Privacy: "external_allowed", MaxExternalCalls: 1}, Task: task, Input: []byte(`{}`), Deterministic: &want,
	})
	if err != nil || calls != 0 || got.Selected.Outcome != "ok" {
		t.Fatalf("got %#v err=%v calls=%d", got, err, calls)
	}
}
