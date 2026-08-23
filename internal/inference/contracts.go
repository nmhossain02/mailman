package inference

import (
	"context"
	"encoding/json"
	"fmt"
)

type Backend interface {
	ID() string
	Health(context.Context) Health
	Infer(context.Context, Request) (ProviderResult, error)
}

type Health struct {
	Ready                      bool
	ModelRevision, SafeMessage string
}

type Request struct {
	TaskName, TaskVersion, PromptVersion, SchemaVersion string
	Instructions, Model                                 string
	InputJSON, OutputSchema                             json.RawMessage
	MaxOutputTokens                                     int
	TraceID                                             string
}

type ProviderResult struct {
	RawOutput, ProviderMetadata                            json.RawMessage
	ProviderResponseID, Model, ModelRevision, FinishReason string
	InputTokens, CachedInputTokens, OutputTokens           *int64
	LoadMS, PromptMS, GenerationMS, WallMS                 *int64
}

type TaskResult struct {
	Outcome string
	Output  any
	Raw     ProviderResult
}

type InferenceError struct {
	Kind           string
	Retriable      bool
	ProviderStatus int
	SafeMessage    string
}

func (e *InferenceError) Error() string {
	if e.SafeMessage != "" {
		return e.SafeMessage
	}
	return fmt.Sprintf("inference failed: %s", e.Kind)
}

type FakeBackend struct {
	BackendID  string
	HealthFunc func(context.Context) Health
	InferFunc  func(context.Context, Request) (ProviderResult, error)
}

func (f *FakeBackend) ID() string { return f.BackendID }

func (f *FakeBackend) Health(ctx context.Context) Health {
	if f.HealthFunc == nil {
		return Health{Ready: true}
	}
	return f.HealthFunc(ctx)
}

func (f *FakeBackend) Infer(ctx context.Context, request Request) (ProviderResult, error) {
	if f.InferFunc == nil {
		return ProviderResult{}, nil
	}
	return f.InferFunc(ctx, request)
}

var _ Backend = (*FakeBackend)(nil)
