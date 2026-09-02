package agent

import (
	"encoding/json"
	"time"
)

// InferenceTrace captures one measurable model invocation or cache result.
type InferenceTrace struct {
	ID, ComparisonGroupID, TargetID, TaskName, TaskVersion string
	PromptVersion, SchemaVersion, InputHash                string
	InputSnapshot, CanonicalOutput                         json.RawMessage
	BackendID, BackendClass, Model, ModelRevision          string
	RouteMode, RouteRole, RouteReason, Outcome, ErrorKind  string
	Selected, CacheHit                                     bool
	Attempt                                                int
	InputTokens, CachedInputTokens, OutputTokens           *int64
	WallMS, LoadMS, PromptMS, GenerationMS                 *int64
	StartedAt, CompletedAt                                 time.Time
}

type EvalCase struct {
	ID, Dataset, TaskName, TaskVersion string
	InputJSON                          json.RawMessage
	InputHash                          string
}

type EvalRunConfig struct {
	ID, Dataset, RouteMode, ProbeSeed string
	LocalBackend, LocalModel          string
	ExternalBackend, ExternalModel    string
	Concurrency, MaxExternalCalls     int
	CacheEnabled, Warmup              bool
	PricingSnapshotDate               string
	Pricing                           json.RawMessage
}
