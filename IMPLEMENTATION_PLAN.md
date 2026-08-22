# Mailman v1 implementation plan

Status: researched design, ready for implementation

This file is the execution brief for implementation agents. Do not redesign the
product while completing a task. If a frozen contract is insufficient, report
the smallest missing field or method to the coordinator instead of adding a
framework or a second abstraction.

## 1. Outcome

Build a local-first email manager that:

- synchronizes Gmail and Outlook incrementally;
- displays conversations, native/local rules, schedules, and draft plans in a
  terminal UI;
- translates natural-language requests into validated, typed core commands;
- performs semantic extraction with a local model by default;
- can use an external model only for explicit fallback or evaluation probes;
- periodically prepares reviewable actions but never applies them in the
  background;
- applies approved compound actions such as archive + mark read + label;
- creates approved Google Tasks and Google Calendar events;
- records enough trace and label data to compare local-only, external-only, and
  hybrid routing configurations.

The first useful vertical slice is:

```text
sync one account
  -> browse conversations and rules
  -> type a natural request
  -> inspect a draft plan
  -> approve and apply reversible actions
  -> inspect the trace/eval record
```

## 2. Locked technical choices

Use these choices unless a task explicitly says otherwise.

| Need | Choice |
|---|---|
| Language | Go 1.27, one executable, `CGO_ENABLED=0` |
| Database | SQLite through `database/sql` and pinned `modernc.org/sqlite`; FTS5 for message search |
| TUI | Bubble Tea v2, Bubbles v2, Lip Gloss v2 |
| Exact CLI | Standard-library `flag.FlagSet`; no Cobra/Viper |
| HTTP/JSON | `net/http`, `encoding/json`; direct REST clients, no provider SDKs |
| OAuth | `golang.org/x/oauth2`, browser + loopback callback + PKCE S256 |
| Secrets | OS credential store through `github.com/zalando/go-keyring`; no silent plaintext fallback |
| Concurrency | `context` and a small bounded `errgroup`; default local-model concurrency is 1 |
| Local inference | Ollama native `/api/chat` with JSON Schema |
| External inference | OpenAI Responses API with `store:false` and strict Structured Outputs |
| Scheduling | One idempotent `schedule run` command invoked by cron, launchd, systemd, or Windows Task Scheduler |
| Logging | `log/slog`; durable decision/inference traces live in SQLite |
| Tests | Standard `testing`, `httptest`, checked-in fixtures; no mocking/assertion framework |

Default state lives under `os.UserConfigDir()/mailman`; allow an explicit
`MAILMAN_DATA_DIR` override for portable/test installations. Keep one JSON config
file and one SQLite database there. OAuth refresh tokens remain in the OS
credential store, not in that directory.

Initial pins researched on 2026-08-22: Go 1.27.0, `modernc.org/sqlite`
v1.57.0 with its required `modernc.org/libc` v1.74.4, Bubble Tea v2.0.9,
Bubbles v2.2.0, Lip Gloss v2.0.6, `x/oauth2` v0.36.0, `x/sync` v0.22.0,
and go-keyring v0.2.8. F0 may move a pin only when these versions cannot resolve
together; record the reason in the commit.

Pin all direct dependencies in `go.mod` and commit `go.sum`. Pin the
`modernc.org/libc` version required by the selected `modernc.org/sqlite` release;
do not upgrade it independently.

Do not add an ORM, `sqlc`, a migration framework, a web server, a message queue,
a vector database, a DI container, an agent framework, OpenTelemetry, Docker as
a runtime requirement, or an internal cron library.

## 3. Deliberately excluded from v1

- Sending mail or forwarding mail
- Permanent deletion; "delete" means recoverable trash
- Automatic execution by recurring jobs
- Full attachment download, OCR, or semantic attachment analysis
- Cross-account conversation merging or semantic thread reconstruction
- Calendar-wide or Tasks-wide synchronization; reconcile only Mailman-created
  targets
- Gmail push/Pub/Sub and Microsoft change webhooks
- Embeddings or vector retrieval
- Fine-tuning or a learned router
- Bundling or managing Ollama/llama.cpp models
- A `codex exec` adapter; direct Responses calls are smaller and more measurable
- A daemon/service installer; the portable one-shot schedule command is enough

## 4. Package ownership

Agents must stay inside their task's owned paths.

```text
cmd/mailman/                    W3 composition root only
internal/core/                  domain records, command IR, validation
internal/store/                 SQLite, migrations, queries, operation journal
internal/secret/                token-store interface and OS-keyring implementation
internal/provider/              shared provider contracts
internal/provider/google/       Gmail, Google Tasks, Google Calendar, Google auth
internal/provider/outlook/      Graph mail/rules/categories, Microsoft auth
internal/inference/             shared inference types, routing, cache keys
internal/inference/ollama/      Ollama adapter
internal/inference/openai/      Responses adapter
internal/policy/                deterministic rule evaluation/conflict handling
internal/app/                   use cases and orchestration
internal/schedule/              one-shot recurring-run logic
internal/ui/                    Bubble Tea models and views
internal/cli/                   exact argument parsing; no composition root
internal/config/                W3 JSON config loading and validation
internal/eval/                  datasets, runners, metrics
internal/store/migrations/      embedded ordered SQL
internal/inference/prompts/     embedded prompts and JSON Schemas
testdata/                       sanitized HTTP and eval fixtures
cmd/mailman/main.go             composition root, created only in W3
```

No package may import `internal/ui`. Provider and inference packages must not
import `internal/store` or `internal/app`.

## 5. Frozen core semantics

### 5.1 Domain records

Use string IDs, UTC timestamps, and `json.RawMessage` only for provider-specific
data that the core does not interpret.

```go
type Message struct {
    ID, AccountID, ProviderID, ConversationID string
    Revision, InternetMessageID                string
    Subject, Sender, NormalizedBody            string
    Recipients                                 []string
    ReceivedAt                                 time.Time
    Read                                       bool
    FolderID                                   string
    TagIDs                                     []string
}

type Conversation struct {
    ID, AccountID, ProviderKey string
    Subject                    string
    MessageIDs                 []string
    LastMessageAt              time.Time
}

type Claim struct {
    ID, TargetType, TargetID, Name string
    Value                           json.RawMessage
    Basis                           string // observed|deterministic|inferred|user
    Evidence                        json.RawMessage
    Confidence                      *float64
    DerivationVersion               string
    CreatedAt                       time.Time
}

type Operation struct {
    ID, ExecutionKey, TargetType, TargetID string
    Kind, Risk                              string
    Arguments                               json.RawMessage
    ExpectedRevision                        string
    Status                                  string // proposed|approved|running|succeeded|uncertain|failed|rejected
}

type Plan struct {
    ID, Name, Status string // draft|frozen|running|completed|partial|cancelled
    Operations       []Operation
    CreatedAt        time.Time
}

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
    Mode             string // local_only|external_only|fallback|probe
    Privacy          string // local_only|external_allowed
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
    ID, AccountID, Kind, TokenKey string // kind: gmail|google_tasks|google_calendar|outlook
    GrantedScopes                 []string
    Enabled                       bool
}
```

Rules and schedules are JSON-backed but validated core records. Preserve raw
provider rule JSON alongside the normalized subset.

The JSON config freezes only deployment inputs:

```go
type Config struct {
    DataDir   string
    Google    OAuthClientConfig // client ID and optional desktop-app client secret
    Microsoft OAuthClientConfig // client ID, tenant defaults to "common", no secret
    Local     ModelConfig       // backend, base URL, model, timeouts
    External  ModelConfig       // backend, base URL, model, enabled; API key is in secret.Store
    Routing   RoutePolicy
}

type OAuthClientConfig struct {
    ClientID, ClientSecret, Tenant string
}

type ModelConfig struct {
    Backend, BaseURL, Model string
    Enabled                 bool
    HealthTimeoutSeconds, InteractiveTimeoutSeconds int
    BackgroundTimeoutSeconds, ExternalTimeoutSeconds int
}
```

Do not hardcode a model name. Config loading belongs to W3; F0 freezes these
records and W1-A persists `IntegrationGrant` records.

### 5.2 Natural-command IR

The model produces a draft, never SQL and never an executable provider request.

```go
type CommandDraft struct {
    Intent        string // find|view|explain|propose|create_rule|update_rule|delete_rule|create_schedule|clarify
    Target        string // message|conversation|rule|plan|schedule|task|event
    Filters       []Filter
    Actions       []Action
    GroupBy       string
    SortBy        string
    Reference     string
    Clarification string
}

type Filter struct { Field, Operator, Value string }
type Action struct { Kind string; Argument string }

type MessageKindOutput struct {
    Kind string // personal|work|transaction|receipt|newsletter|notification|alert|other
    EvidenceMessageIDs []string
    Abstain bool
}

type ExtractedRequest struct {
    Summary, EvidenceMessageID, EvidenceQuote string
    DueISO, Timezone string // empty when absent
    RequiresReply bool
}

type RequestsDatesOutput struct {
    Requests []ExtractedRequest
    Abstain bool
}

type SummaryDeltaOutput struct {
    Summary string
    OpenQuestions, Commitments []string
    EvidenceMessageIDs []string
    Abstain bool
}
```

Core validation uses allowlists. Unknown fields, actions, labels, rules, and UI
references become `clarify`. "Delete" compiles to `trash`; permanent deletion is
not a v1 action. Read-only commands can run immediately. Mutations always create
a draft plan.

In v1, filters are ANDed. Rule conditions are all-required and any exception
suppresses the rule. Allowed operators are `eq|ne|contains|in|lt|lte|gt|gte`.
Start with fields `kind`, `age_days`, `read`, `starred`, `sender`,
`sender_domain`, `label`, `folder`, `awaiting`, `has_deadline`, `received_at`,
`subject`, and `body`. Allowed action kinds are `archive`, `trash`, `mark_read`,
`mark_unread`, `add_label`, `remove_label`, `add_queue`, `create_task`,
`create_event`, and `create_rule`. Extend these lists only through an explicit
core change and fixture.

### 5.3 Provider boundary

```go
type OpaqueCursor json.RawMessage

type SyncPage struct {
    Upserts       []ProviderMessage
    DeletedIDs    []string
    Continuation  OpaqueCursor // next page; never a durable checkpoint
    Checkpoint    OpaqueCursor // set only on the final successfully consumed page
    Done          bool
}

type Capabilities struct {
    RuleCreate, RuleUpdate, RuleDisable, RuleDelete bool
    RuleOrder, RuleStopProcessing                   bool
    BatchApply, Restore                             bool
}

type ProviderAccount struct { ID, Address, DisplayName string }
type ProviderCollection struct { ID, Name, Kind, ParentID string }
type ProviderContent struct { MessageID, PlainText string; Raw json.RawMessage }

type ProviderMessage struct {
    StableID, ConversationKey, Revision, FolderID string
    InternetMessageID, Subject, Sender             string
    Recipients, TagIDs                             []string
    ReceivedAt                                     time.Time
    Read                                           bool
    ContentLoaded                                  bool
    Raw                                            json.RawMessage
}

type ProviderRule struct {
    ID, Name, Source string
    Enabled, ReadOnly bool
    Sequence          *int
    Conditions, Exceptions []core.Filter
    Actions                []core.Action
    Raw                    json.RawMessage
}

type ProviderRuleDraft struct {
    Name string
    Enabled bool
    Sequence *int
    Conditions, Exceptions []core.Filter
    Actions []core.Action
}

type RuleCompilation struct {
    Status string // supported|partial|unsupported
    Draft ProviderRuleDraft
    LocalRemainder *core.Rule
    Reason string
}

type RuleReceipt struct { ProviderID, ExecutionKey, Status string; Raw json.RawMessage }

type DesiredMailState struct {
    ProviderMessageID, ExecutionKey, ExpectedRevision string
    Read                                               *bool
    Disposition                                        string // empty|archive|trash|restore
    EnsureTags, RemoveTags                             []string
    DestinationCollectionID                            string
}

type OperationResult struct {
    ExecutionKey, Status, RemoteID, NewRevision string
    BeforeState, AfterState                     json.RawMessage
    ErrKind, SafeMessage                        string
}

type MailProvider interface {
    ID() string
    Capabilities(context.Context) (Capabilities, error)
    Account(context.Context) (ProviderAccount, error)
    ListCollections(context.Context) ([]ProviderCollection, error)
    Sync(context.Context, OpaqueCursor) (SyncPage, error)
    FetchContent(context.Context, []string) ([]ProviderContent, error)
    ListRules(context.Context) ([]ProviderRule, error)
    CompileRule(core.Rule) RuleCompilation
    CreateRule(context.Context, ProviderRuleDraft, string) (RuleReceipt, error)
    UpdateRule(context.Context, string, ProviderRuleDraft, string) (RuleReceipt, error)
    DeleteRule(context.Context, string) error
    Apply(context.Context, []DesiredMailState) ([]OperationResult, error)
}
```

Google action targets use two smaller interfaces rather than pretending they are
mail providers:

```go
type TaskDraft struct { ListID, Title, Notes, DueDate string }
type TaskPatch struct { Title, Notes, DueDate, Status string }
type EventDraft struct {
    CalendarID, Title, Description, Start, End, Timezone, Location string
    AllDay bool
    // Attendees are intentionally absent in v1.
}
type EventPatch EventDraft
type TaskList struct { ID, Name string }
type Calendar struct { ID, Name string; Writable bool }
type TargetReceipt struct { ProviderID, ExecutionKey, Status string; Raw json.RawMessage }

type TaskTarget interface {
    ListTaskLists(context.Context) ([]TaskList, error)
    EnsureTask(context.Context, TaskDraft, string) (TargetReceipt, error)
    UpdateTask(context.Context, string, TaskPatch) (TargetReceipt, error)
    DeleteTask(context.Context, string) error
}

type CalendarTarget interface {
    ListCalendars(context.Context) ([]Calendar, error)
    EnsureEvent(context.Context, EventDraft, string) (TargetReceipt, error)
    UpdateEvent(context.Context, string, EventPatch) (TargetReceipt, error)
    DeleteEvent(context.Context, string) error
}
```

Every v1 calendar adapter rejects attendee data if it appears in raw input. This
is a core capability omission, not only a UI approval check.

Provider cursors are opaque to the core. Pagination state and durable checkpoint
state are different: consume `Continuation` until `Done`, commit every page, and
promote `Checkpoint` atomically only after the final page commits. A crash during
pagination must leave the previous checkpoint usable. Capability snapshots make
Gmail rule update/disable/order false and Outlook equivalents true.

Conversation grouping uses Gmail `threadId` or Outlook `conversationId`.
Outlook ordering uses `conversationIndex` plus time. Do not implement weighted
thread clustering.

### 5.4 Inference boundary

```go
type Backend interface {
    ID() string
    Health(context.Context) Health
    Infer(context.Context, Request) (ProviderResult, error)
}

type Health struct {
    Ready bool
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
    RawOutput, ProviderMetadata json.RawMessage
    ProviderResponseID, Model, ModelRevision, FinishReason string
    InputTokens, CachedInputTokens, OutputTokens *int64
    LoadMS, PromptMS, GenerationMS, WallMS       *int64
}

type TaskResult struct {
    Outcome string // ok|abstain|refused|incomplete
    Output  any    // one task-specific validated Go output type
    Raw     ProviderResult
}

type InferenceError struct {
    Kind string // unavailable|timeout|overloaded|authentication|invalid_request|invalid_output|cancelled
    Retriable bool
    ProviderStatus int
    SafeMessage string
}
```

The adapter returns provider output, not a validated semantic result. W1-D owns a
shared task runner that strict-decodes one task-specific Go output struct using
`DisallowUnknownFields`, rejects trailing JSON, calls its `Validate()`, and then
returns `TaskResult`. The request deadline exists only on `context.Context`; a
retry or fallback shares the original context deadline.

All adapters use a conservative JSON Schema subset: objects, arrays, primitive
values, enums, all properties required, optional values encoded with JSON Schema
`"type":["string","null"]` (never OpenAPI `nullable:true`), and
`additionalProperties:false`. Check in one canonical example for other agents to
copy. The schema constrains generation; typed decoding is the local correctness
boundary.

F0 places these reproducibility/context records in `internal/core`; W1-A persists
them and W1-D fills them:

```go
type InferenceTrace struct {
    ID, ComparisonGroupID, TargetID, TaskName, TaskVersion string
    PromptVersion, SchemaVersion, InputHash                 string
    InputSnapshot, CanonicalOutput                          json.RawMessage
    BackendID, BackendClass, Model, ModelRevision           string
    RouteMode, RouteRole, RouteReason, Outcome, ErrorKind   string
    Selected, CacheHit                                      bool
    Attempt                                                 int
    InputTokens, CachedInputTokens, OutputTokens            *int64
    WallMS, LoadMS, PromptMS, GenerationMS                  *int64
    StartedAt, CompletedAt                                  time.Time
}

type EvalCase struct {
    ID, Dataset, TaskName, TaskVersion string
    InputJSON                          json.RawMessage // self-contained, immutable
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
    SelectedType, SelectedID string
    AccountNames, LabelNames, QueueNames, RuleNames, ScheduleNames []string
    Now time.Time
    Timezone string
}
```

Background routing is:

```text
cache -> local -> validate -> accept or defer to review
```

External inference is permitted only for an explicit user enhancement, an
allowed fallback after abstention/invalid output, or an eval probe. Do not route
on uncalibrated self-reported confidence. Probe outputs never replace the local
production output.

## 6. Data and performance rules

At SQLite startup enable foreign keys, WAL, and a 5-second busy timeout. Use one
writer and short transactions. Maintain FTS5 rows explicitly in the same
transaction as message upserts.

Use coarse-to-fine processing:

```text
provider metadata -> deterministic filtering -> campaign/conversation grouping
-> lazy body fetch -> local inference -> per-item safety check -> draft plan
```

Cache inference by backend, model revision, task version, prompt version, schema
version, and normalized input hash. One `(target, primitive)` pair is one request;
a conversation normally invokes several separately benchmarkable primitives.
Deduplicate repetitive campaigns before inference instead of putting many
messages into one prompt. W1-D computes keys, W1-A stores entries, and W2-A owns
cache reads/writes and trace persistence. Resolve the configured Ollama tag to a
digest during health/startup and include the digest in the key. Eval cache is off
by default; external outputs are not cached in v1.

Default deadlines:

- health: 2 seconds
- interactive command translation: 30 seconds
- background local inference: 120 seconds
- external fallback: 90 seconds

For inference HTTP only, retry once for connection failures, HTTP 429, or HTTP
503 when the original context deadline allows it. Never retry authentication,
invalid schema, invalid output, refusal, or truncation. Cap inference response
bodies at 2 MiB and stored diagnostic bodies at 8 KiB.

Provider retries are stricter: reads may retry transient 500/502/503/504 and 429
with `Retry-After`; naturally idempotent desired-state writes may retry. A lost
response from Tasks insert, rule creation, or Graph move becomes `uncertain` and
must reconcile before any retry.

## 7. Provider behavior that must not be generalized away

### Gmail

- Request only `gmail.modify` and `gmail.settings.basic`; explicitly prohibit
  `mail.google.com` and `gmail.send`.
- Full sync lists messages; incremental sync uses `historyId`.
- History HTTP 404 means the cursor expired and a full sync is required.
- `threadId` is the conversation key.
- Archive removes `INBOX`; read removes `UNREAD`; unread adds `UNREAD`.
- Use `messages.batchModify` for read/archive/labels, at most 1,000 IDs.
- Trash/untrash are per-message calls; chunk Gmail HTTP batches at 50.
- Filters support list/get/create/delete, but not update, disable, ordering, or
  stop-processing. A requested disable is delete plus a retained local snapshot.
- Filter creation does not backfill historical mail; create a separate plan.
- Existing forwarding or permanent-delete filter actions remain visible in raw
  provider state but cannot compile into a v1-created rule.

### Outlook

- Send `Prefer: IdType="ImmutableId"` on every mail request, saved-link request,
  and individual JSON batch subrequest; the outer batch header is insufficient.
- Delta is per folder. Store each returned delta URL verbatim.
- Synchronize Inbox, Sent, Archive, Deleted Items, and visible user folders.
- A message has one folder and zero or more categories; keep these distinct.
- Archive/trash use move; read/categories use PATCH. Category PATCH replaces the
  whole list, so preserve unrelated categories and send the union/difference.
- For a compound action, combine read/categories in one PATCH and perform the
  move afterward (or express the dependency with `dependsOn`); record partial
  results rather than sending unordered PATCH and move requests.
- Graph JSON batches contain at most 20 operations. Inspect every subresponse and
  retry individual throttled operations using `Retry-After`.
- Rules may have ordering, exceptions, stop-processing, enabled/error/read-only
  state. Never mutate a read-only rule.
- Existing forwarding, sending, or permanent-delete actions remain visible in
  raw provider state but cannot compile into a v1-created rule.

### Google Calendar

- This is an action target, not a fully synchronized calendar client.
- Generate a stable lowercase base32hex event ID from the execution key.
- Store execution/source IDs in private extended properties.
- No attendees without item-level approval; no conference creation.
- Require a timezone for timed events; use all-day events for date-only evidence.

### Google Tasks

- This is an action target, not a fully synchronized task client.
- The API discards due time and preserves only the date.
- Task IDs are server-assigned. Put `mailman:<execution-key>` in notes.
- On an uncertain insert, search recent tasks for the marker before retrying.

## 8. Execution graph

The plan assumes four concurrent implementation slots. Complete foundation task
F0 first. Then Wave 1 tasks can run in parallel. Wave 2 tasks can start only after
the Wave 1 contract gate. W3 is the final integration gate.

Four slots means coordinator plus three child agents. During Wave 1 the
coordinator implements W1-A locally while three children take W1-B/C/D, or runs
one packet in a second dispatch. Never attempt four children plus coordinator.

For each dispatch, give a smaller agent only: sections 1–7 of this file, its own
task packet, the frozen contract files, and the current failing/passing test
output. Do not include the other task packets or ask it to reconsider technology
choices. Its completion report must list changed paths, tests run, and any exact
contract gap; it must not edit outside its owned paths.

```text
F0 Foundation
 |
 +--> W1-A Store/secrets -----------+
 +--> W1-B Google integrations -----+--> Wave 1 contract gate
 +--> W1-C Outlook integration -----+
 +--> W1-D Inference/command -------+
                                      |
             +------------------------+---------------------+
             v                        v                     v
        W2-A App/policy/schedule  W2-B TUI/CLI          W2-C Eval runner
             +------------------------+---------------------+
                                      v
                           W3 Vertical integration and QA
```

## 9. Agent task packets

### F0 — scaffold and freeze shared contracts

**Runs alone. Owns:** `go.mod`, `go.sum`, `internal/core/**`,
`internal/provider/contracts.go`, `internal/inference/contracts.go`,
`internal/secret/contracts.go`, top-level test helpers.

**Build:**

1. Initialize the Go module and pin the locked dependencies.
2. Add exactly the domain, command, provider pagination/checkpoint, capability,
   inference outcome/error/route/trace/eval, config, and clock types described
   above. Define `secret.Store` as `Get/Set/Delete(context, key, bytes)`.
3. Add the task/calendar target and secret-store interfaces.
4. Add the default data-directory resolver with `MAILMAN_DATA_DIR` override; do
   not implement config loading yet.
5. Add `Validate()` methods for commands, rules, operations, and schedules.
6. Add simple fake provider and fake inference backend types for later tests.
7. Add no-op package tests proving invalid enum values and unknown command fields
   are rejected.

**Acceptance:** `go test ./...` and `go vet ./...` pass. No network, database,
provider, model, or UI behavior is implemented.

**F0 contract gate:** the coordinator checks that every type named in the frozen
interfaces compiles, fakes implement the interfaces, and no Wave 1 agent needs to
invent a shared field. After this gate, the three contract files are
coordinator-owned and parallel agents may not edit them.

**Do not:** create generic repositories, an event bus, plugin loading, or an app
service abstraction.

### W1-A — SQLite store, migrations, search, and secret storage

**Depends:** F0. **Owns:** `internal/store/**`,
`internal/secret/**` except `contracts.go`, and store fixtures. Migrations live
under `internal/store/migrations/` so `go:embed` has no parent-directory path;
fixtures live under `testdata/store/`.

**Build:**

- Embedded checksum-verified migrations for accounts, cursors, messages,
  conversations, claims, collections, rules, plans/operations, external-operation
  journal, schedules, inference traces, eval labels, and eval runs.
- Inference cache rows keyed by the frozen composite key, with lookup/write
  methods used only by W2-A.
- Upsert/query methods needed by the frozen records; no generic repository.
- FTS5 over sender, subject, and normalized body.
- Operation journal rules: unique execution key, request-hash mismatch rejection,
  and `pending|succeeded|uncertain|failed` state.
- OS keyring token store and an in-memory test implementation. Return a clear
  error when the OS keyring is unavailable; never write plaintext tokens.

**Required tests:** empty migration, repeated migration, checksum mismatch, FTS
insert/update/delete/search, conversation ordering, trace round-trip, operation
idempotency, request-hash mismatch, and keyring error propagation.

**Acceptance:** tests use temporary databases and finish without network access.

### W1-B — Google OAuth, Gmail, Tasks, and Calendar

**Depends:** F0 and the `secret.Store` interface only. **Owns:**
`internal/provider/google/**`, `testdata/google/**`.

Complete and test three checkpoints in order: **B1 auth + Gmail sync**, **B2
Gmail actions + filters**, **B3 Tasks + Calendar targets**. Do not implement all
surfaces before running the earlier checkpoint tests.

**Build:**

- Desktop OAuth with system browser, `127.0.0.1:0`, random state, PKCE S256,
  offline access, and separately consented Gmail/Tasks/Calendar integration
  records.
- Exact scopes: `gmail.modify`, `gmail.settings.basic`, `calendar.events`,
  `calendar.calendarlist.readonly`, and `tasks`. Request only the union required
  by enabled integrations; retain an existing refresh token when an exchange
  omits one and persist rotated tokens. Never request `mail.google.com`,
  `gmail.send`, or broad `calendar`.
- Gmail full sync, incremental history sync, lazy content fetch, labels,
  filter import/create/delete, batch desired-state operations, trash/untrash.
- Google Tasks list selection, ensure/update/delete with operation marker and
  uncertain-result reconciliation.
- Google Calendar list selection, ensure/update/delete with deterministic event
  ID and private marker.
- Direct REST with injected base URLs and `*http.Client`.
- Provider-local browser/loopback code is acceptable; do not create a shared
  OAuth package. If browser launch fails, print the authorization URL. Setup docs
  must say to enable Gmail, Calendar, and Tasks APIs, create a Desktop OAuth
  client/consent screen/test users, and note that external projects left in
  Google "Testing" can issue short-lived refresh tokens.

**Required fixture tests:** Gmail pagination, duplicate history IDs, expired
history cursor, crash before final checkpoint promotion, batch chunk limits,
rule canonicalization and duplicate-create
avoidance, OAuth state/PKCE/refresh, Calendar deterministic retry, attendee gate,
Task date-only due value, and uncertain Task reconciliation.

Uncertain Task reconciliation must paginate through matching recent tasks, not
inspect only the first page.

**Do not:** send mail, permanently delete mail, forward, invite attendees in a
smoke test, or synchronize whole task lists/calendars.

### W1-C — Microsoft OAuth and Outlook/Graph

**Depends:** F0 and the `secret.Store` interface only. **Owns:**
`internal/provider/outlook/**`, `testdata/outlook/**`.

Complete and test three checkpoints in order: **C1 auth + folders/delta**, **C2
mail actions/categories/batching**, **C3 native rules**.

**Build:**

- Public desktop authorization-code OAuth with browser, loopback, PKCE, refresh
  token cache, and scopes `openid profile offline_access Mail.ReadWrite
  MailboxSettings.ReadWrite`.
- The app registration supports organizational and personal Microsoft accounts,
  is a public desktop client, uses the chosen loopback redirect, and has no
  client secret. Persist rotated refresh tokens. If browser launch fails, print
  the authorization URL; keep the implementation provider-local.
- Visible-folder discovery and one delta cursor per folder.
- Message normalization with immutable IDs, conversation ID/index, folder, read
  state, and categories.
- Lazy content fetch; archive/trash move; read/category PATCH; JSON batching.
- Master category list/create as needed.
- Rule import/create/update/delete with full raw preservation and explicit
  `supported|partial|unsupported` compilation.

**Required fixture tests:** folder traversal, next/delta links, move expressed as
delete+upsert for one immutable ID, crash before checkpoint promotion, immutable
ID header on saved links and each batch member, unrelated-category preservation,
ordered compound PATCH+move, mixed-success batch, per-item 429 retry, read-only
rule rejection, partial rule compilation, OAuth state/PKCE/refresh.

**Do not:** send mail, call message DELETE for trash, assume batch HTTP 200 means
success, or flatten folder and categories into one field.

### W1-D — inference adapters and natural-command translation

**Depends:** F0. **Owns:** `internal/inference/**` except `contracts.go`, plus
`testdata/inference/**`. Prompts and schemas live under
`internal/inference/prompts/` for valid embedding. Do not edit frozen contracts.

Complete and test three checkpoints in order: **D1 adapters + strict task
runner**, **D2 router + tracing/cache keys**, **D3 natural translator + three
email primitives**.

**Build:**

- Ollama native `/api/chat` adapter using `stream:false` and JSON Schema.
- OpenAI Responses adapter using non-streaming `/v1/responses`, `store:false`, no
  tools, bounded output, and strict structured output.
- Local-only, external-only, explicit-fallback, and deterministic stable-probe
  routing modes. Select probes by hashing target/case ID plus `ProbeSeed`.
- Task-specific input/output structs, prompts, schemas, and validators for
  message kind, requests/dates, conversation summary delta, and natural-command
  translation.
- Command translator producing `CommandDraft`, deterministic reference
  resolution, relative-date parsing with an injected time/timezone, strict JSON
  decoding with unknown fields rejected, and stable cache keys.
- A bounded relative-date grammar: ISO dates, today/tomorrow, next weekday, and
  `in N days/weeks`; other date language becomes clarification.
- Trace normalization for tokens, timings, route role/reason, errors, attempts,
  and validation outcomes. Never record headers, keys, or environment dumps.

**Required tests:** exact request fields, valid/malformed/schema-invalid output,
missing required fields, extra fields, trailing JSON, oversized response,
multiple Responses output items, OpenAI refusal/failed/incomplete/truncated
terminal states, Ollama truncation, 400/401/429/500/503 mapping, cancellation
during retry, one bounded retry, local success never
calling external, zero-budget abstention deferring, privacy blocking external,
probe retaining local output, cache invalidation, ambiguous references becoming
clarification, and at least 15 natural-command fixtures.

**Do not:** add llama.cpp, `codex exec`, model SDKs, an agent framework, model
tool calls, or model-driven execution.

### Wave 1 contract gate

The coordinator runs all unit tests and reviews only these shared facts:

- provider capability snapshots are explicit;
- cursors remain opaque;
- inference results normalize usage/timing without discarding provider metadata;
- database records can persist every shared record;
- no provider or model code can execute a core command directly.

Fix contract gaps here. Do not defer shared-type churn into Wave 2.

### W2-A — application pipeline, policies, plans, and one-shot schedules

**Depends:** all Wave 1 tasks. **Owns:** `internal/app/**`, `internal/policy/**`,
`internal/schedule/**`.

Complete and test three checkpoints in order: **A1 sync + primitive
orchestration**, **A2 policy + plans + apply/undo**, **A3 schedule preparation**.

**Build:**

- Sync orchestration: persist pages/cursors, fetch selected bodies, maintain
  provider conversations, synchronize native rules, and enqueue changed targets.
- Invoke the four task runners delivered by W1-D. W2-A owns selection,
  cache read/write, trace persistence, and no prompt/schema definitions.
- Deterministic contextual state for `awaiting_me`, staleness, and safe
  disposition.
- Local rules with conditions/exceptions and compound effects.
- Conflict precedence: safety/retention, explicit user rules, native-state
  preconditions, learned/inferred recommendation, defaults.
- Draft-plan upsert/deduplication, freeze, selective approval/rejection, apply,
  per-item receipt, stale-precondition rejection, and recoverable undo.
- `schedule run <name>`: incremental sync, local-only processing by default, and
  rolling draft-plan preparation. It never calls Apply.
- Task/event operation compilation and approval gates.

**Required tests:** one changed message only recomputes its conversation,
compound operation plan, conflict resolution, stale revision, repeated schedule
run deduplication, zero external calls in default schedule, provider partial
failure, uncertain external operation reconciliation, task/event approval gates,
and no background mutation.

**Do not:** introduce workers, queues, event sourcing, or a long-running daemon.

### W2-B — human TUI and minimal exact CLI

**Depends:** Wave 1 types; may work against an in-memory fake backend while W2-A
runs. **Owns:** `internal/ui/**` and `internal/cli/**`. Define the consumer-owned
`ui.Backend` interface inside `internal/ui`; W3 supplies an adapter to W2-A. Do
not create or edit `cmd/mailman/main.go`.

Use this narrow backend shape; UI-specific list/detail view models may wrap core
records, but no business decisions live here:

```go
type Backend interface {
    ListConversations(context.Context, core.CommandDraft) ([]core.Conversation, error)
    GetConversation(context.Context, string) (ConversationDetail, error)
    ListRules(context.Context) ([]core.Rule, error)
    ListSchedules(context.Context) ([]core.Schedule, error)
    ListPlans(context.Context) ([]core.Plan, error)
    Interpret(context.Context, string, core.TranslationContext) (Interpretation, error)
    Preview(context.Context, core.CommandDraft) (PlanPreview, error)
    FreezePlan(context.Context, string) (core.Plan, error)
    ApplyPlan(context.Context, string) (core.Plan, error)
    SaveEvalLabel(context.Context, core.EvalLabel) error
}
```

**Build:**

- No arguments opens the TUI. A known exact subcommand uses `flag.FlagSet`.
  Otherwise the argument text is a natural command.
- Views: conversations, conversation detail/messages/claims, effective native
  and local rules, schedules/status, draft/frozen plans, and Mailman-created task
  or event receipts.
- Persistent command palette with visible compiled interpretation before a
  mutation becomes a plan.
- A user can correct or confirm a translated command and explicitly save the
  corrected canonical command as an `EvalLabel` linked to its trace. Approval,
  rejection, undo, or ordinary execution must not silently become a label.
- Grouped bulk-plan review with counts, samples, outliers, and selective
  approve/reject.
- Minimal exact commands: `sync`, `schedule run`, `doctor`, `eval run`, and a
  JSON-output escape hatch for scripting.
- External eval execution requires both `--allow-external` and a displayed,
  positive case/call cap. The OpenAI API key is retrieved from `secret.Store` and
  injected into the adapter; it never appears in JSON config.

**Required tests:** pure Bubble Tea `Update` tests for navigation and selection,
conversation/rule/plan rendering, resize handling, command palette clarification,
scope count shown before plan creation, and Apply impossible without a frozen
approved plan. Avoid brittle full-screen golden snapshots.

**Do not:** build a web UI, duplicate business rules in UI code, or expose every
core field as a flag.

### W2-C — eval runner and lightweight benchmark reports

**Depends:** W1-A and W1-D. **Owns:** `internal/eval/**`, `testdata/eval/**`.

**Build:**

- JSONL dataset reader/writer for input references and expected structured output.
- Config snapshots for local-only, external-only, local-first fallback, probe,
  and retrospective oracle runs. Algorithms are fixed: local/external run every
  case on that backend; fallback follows the frozen router; probe runs paired
  outputs but selects local; oracle chooses the higher labeled score and breaks
  ties toward local.
- An optional dated pricing snapshot in each run configuration. Convert tokens to
  estimated cost from that frozen input; never fetch prices during a run.
- Exact/canonical command grading plus field-level grading for semantic
  primitives.
- Metrics: valid-output, abstention, escalation, timeout/schema failure, local
  resolution, external lift, avoidable/missed escalation, routing regret, tokens,
  wall time, and provider-native tokens/second when available.
- Cache defaults off for evals; warmup and concurrency are recorded in the run
  snapshot. External lift, avoidable/missed escalation, and routing regret apply
  only to labeled paired cases. Unlabeled probes report disagreement, never treat
  the external output as truth, and render unsupported metrics as `N/A`.
- Human-readable table plus JSON output. No plotting dependency.

**Required tests:** canonical equality, partial field score, zero-denominator
metrics, probe grouping, oracle selection, deterministic report ordering, and
external usage/cost remaining separately attributable.

**Do not:** add an evaluator model, dataframe library, telemetry backend, or one
opaque aggregate score.

### W3 — vertical integration, smoke commands, portability, and QA

**Runs after W2. Owns:** `cmd/mailman/main.go`, `internal/config/**`,
`internal/doctor/**`, `integration/**`, README setup/run instructions, and only
coordinator-approved minimal edits elsewhere.

**Build and verify:**

1. Wire config, keyring, store, providers, model router, app, exact CLI, and TUI.
2. Add `doctor` checks for database, keyring, provider refresh, local model, and
   separately authorized Tasks/Calendar targets. Explain that scheduled commands
   must run as the logged-in user with access to the unlocked OS credential
   store; diagnose this explicitly.
3. Add opt-in smoke suites using dedicated accounts. Each must be individually
   gated and skipped by default.
4. Add one fixture-backed end-to-end test:
   sync -> translate request -> draft compound plan -> approve -> fake apply ->
   persist receipt/trace -> eval report.
5. Cross-build with CGO disabled for Linux amd64/arm64, Darwin amd64/arm64, and
   Windows amd64.

Live smoke behavior uses dedicated test accounts and explicit environment-provided
message, calendar, and task-list IDs. Assert a required subject/marker before any
message mutation, snapshot exact state, register cleanup immediately after every
created resource, and restore state even after an intermediate failure.

- Gmail: profile, full/incremental sync completion, temporary label apply/remove,
  explicitly selected seeded-message trash/untrash with exact label/read/inbox
  restoration, and impossible-to-match filter using the temporary label as its
  action, deleted before the label.
- Outlook: folder/delta completion, temporary category apply/remove, move a seeded
  message between dedicated folders and back, disabled random-token rule
  with `max(sequence)+1` and the temporary category action, then update/delete.
- Calendar: private no-attendee event create/get/patch/delete in an explicitly
  selected dedicated calendar; generate a unique key for every smoke run.
- Tasks: marked task create/get/patch/delete in an explicitly selected dedicated
  task list.
- Ollama/OpenAI: one tiny schema request. OpenAI smoke is opt-in to prevent cost.

Never run live permanent deletion, sending, forwarding, or attendee invitations.
Do not require a literal zero-change incremental result; provider-generated
changes are allowed if the checkpoint and normalized local state remain valid.

## 10. Test commands and merge gates

Default tests must be offline and fast.

```text
go test ./...
go vet ./...
```

Run `go test -race ./...` only in a native CI lane with CGO/toolchain support.
Ordinary tests and cross-build lanes use `CGO_ENABLED=0`; the race detector is
not a universal cross-platform or CGO-free gate.

Before v1 acceptance, also cross-build with `CGO_ENABLED=0` for the target matrix.
Integration tests use separate, explicit environment gates; one global
`INTEGRATION=1` flag is not sufficient because paid and mutating integrations
must be independently selected.

Reject a task at review if it:

- adds a dependency without explaining why the standard library or locked stack
  is insufficient;
- performs real network I/O in default tests;
- lets model output bypass command validation or planning;
- lets recurring processing apply mutations;
- hides provider capability differences;
- stores tokens outside the OS keyring;
- retries an uncertain non-idempotent operation without reconciliation;
- introduces a new abstraction for a single implementation.

## 11. Primary implementation references

- [Go downloads and supported binaries](https://go.dev/dl/)
- [Bubble Tea](https://pkg.go.dev/charm.land/bubbletea/v2)
- [modernc SQLite driver](https://pkg.go.dev/modernc.org/sqlite)
- [SQLite WAL](https://www.sqlite.org/wal.html) and [FTS5](https://www.sqlite.org/fts5.html)
- [Google desktop OAuth](https://developers.google.com/identity/protocols/oauth2/native-app)
- [Gmail synchronization](https://developers.google.com/workspace/gmail/api/guides/sync)
- [Gmail labels](https://developers.google.com/workspace/gmail/api/guides/labels)
- [Gmail filters](https://developers.google.com/workspace/gmail/api/guides/filter_settings)
- [Microsoft authorization-code flow](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-auth-code-flow)
- [Outlook immutable IDs](https://learn.microsoft.com/en-us/graph/outlook-immutable-id)
- [Graph message delta](https://learn.microsoft.com/en-us/graph/delta-query-messages)
- [Graph JSON batching](https://learn.microsoft.com/en-us/graph/json-batching)
- [Outlook message rules](https://learn.microsoft.com/en-us/graph/api/resources/messagerule?view=graph-rest-1.0)
- [Google Calendar event creation](https://developers.google.com/workspace/calendar/api/guides/create-events)
- [Google Tasks API](https://developers.google.com/workspace/tasks/reference/rest)
- [Ollama structured outputs](https://docs.ollama.com/capabilities/structured-outputs) and [chat API](https://docs.ollama.com/api/chat)
- [OpenAI Responses API](https://developers.openai.com/api/reference/cli/resources/responses/methods/create)
