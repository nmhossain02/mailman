package core

import (
	"encoding/json"
	"time"
)

type Message struct {
	ID, AccountID, ProviderID, ConversationID string
	Revision, InternetMessageID               string
	Subject, Sender, NormalizedBody           string
	Recipients                                []string
	ReceivedAt                                time.Time
	Read                                      bool
	FolderID                                  string
	TagIDs                                    []string
}

type Conversation struct {
	ID, AccountID, ProviderKey string
	Subject                    string
	MessageIDs                 []string
	LastMessageAt              time.Time
}

type Claim struct {
	ID, TargetType, TargetID, Name string
	Value                          json.RawMessage
	Basis                          string
	Evidence                       json.RawMessage
	Confidence                     *float64
	DerivationVersion              string
	CreatedAt                      time.Time
}

type Operation struct {
	ID, ExecutionKey, TargetType, TargetID string
	Kind, Risk                             string
	Arguments                              json.RawMessage
	ExpectedRevision                       string
	Status                                 string
}

type Plan struct {
	ID, Name, Status string
	Operations       []Operation
	CreatedAt        time.Time
}

type Filter struct{ Field, Operator, Value string }
type Action struct{ Kind, Argument string }

type Rule struct {
	ID, AccountID, Source, ProviderID, Name string
	Enabled, ReadOnly                       bool
	Sequence                                *int
	Conditions, Exceptions                  []Filter
	Actions                                 []Action
	RawProvider                             json.RawMessage
	CanonicalHash                           string
}

type RoutePolicy struct {
	Mode             string
	Privacy          string
	MaxExternalCalls int
	ProbeRate        float64
	ProbeSeed        string
}

type Schedule struct {
	ID, Name, DraftPlanName string
	Enabled                 bool
	EverySeconds            int64
	AccountIDs, RuleIDs     []string
	Route                   RoutePolicy
	LastRunAt               *time.Time
}

type IntegrationGrant struct {
	ID, AccountID, Kind, TokenKey string
	GrantedScopes                 []string
	Enabled                       bool
}

type Config struct {
	DataDir   string
	Google    OAuthClientConfig
	Microsoft OAuthClientConfig
	Local     ModelConfig
	External  ModelConfig
	Routing   RoutePolicy
}

type OAuthClientConfig struct{ ClientID, ClientSecret, Tenant string }

type ModelConfig struct {
	Backend, BaseURL, Model                          string
	Enabled                                          bool
	HealthTimeoutSeconds, InteractiveTimeoutSeconds  int
	BackgroundTimeoutSeconds, ExternalTimeoutSeconds int
}

type CommandDraft struct {
	Intent        string
	Target        string
	Filters       []Filter
	Actions       []Action
	GroupBy       string
	SortBy        string
	Reference     string
	Clarification string
}

type MessageKindOutput struct {
	Kind               string
	EvidenceMessageIDs []string
	Abstain            bool
}

type ExtractedRequest struct {
	Summary, EvidenceMessageID, EvidenceQuote string
	DueISO, Timezone                          string
	RequiresReply                             bool
}

type RequestsDatesOutput struct {
	Requests []ExtractedRequest
	Abstain  bool
}

type SummaryDeltaOutput struct {
	Summary            string
	OpenQuestions      []string
	Commitments        []string
	EvidenceMessageIDs []string
	Abstain            bool
}

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

type EvalLabel struct {
	CaseID, TraceID, Source string
	ExpectedJSON            json.RawMessage
	CreatedAt               time.Time
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

type TranslationContext struct {
	SelectedType, SelectedID                                       string
	AccountNames, LabelNames, QueueNames, RuleNames, ScheduleNames []string
	Now                                                            time.Time
	Timezone                                                       string
}
