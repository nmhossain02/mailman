# Mailman

Mailman is a local-first email management system for bulk inbox cleanup. It
syncs Gmail or Outlook into a portable SQLite database, interprets plain-English
requests with a local Ollama model, prepares reviewable plans, and only changes
remote state after explicit approval. Scheduled runs prepare rolling drafts;
they cannot apply them.

The implementation is intentionally small: one Go binary, one SQLite file, OS
keyring secrets, raw provider REST clients, a Bubble Tea TUI, and optional
OpenAI Responses calls for explicitly capped fallbacks or evaluation probes.

## Build and run

Go 1.27 or newer is required.

```sh
go build -o mailman ./cmd/mailman
./mailman doctor
./mailman
```

With no arguments Mailman opens the TUI. Press `/` for the natural-command
palette. The palette always shows the compiled command and plan scope before a
reviewer selects operations, freezes the plan, and applies it.

The data directory is the platform user-config directory under `mailman`. Set
`MAILMAN_DATA_DIR` to move the database and config together. Tokens and API keys
never enter this directory; they remain in the logged-in user's OS keyring.

## Configuration

Create `$MAILMAN_DATA_DIR/config.json` (or use the platform default directory):

```json
{
  "Core": {
    "Google": {"ClientID": "...", "ClientSecret": "..."},
    "Microsoft": {"ClientID": "...", "Tenant": "common"},
    "Local": {
      "Backend": "ollama",
      "BaseURL": "http://127.0.0.1:11434",
      "Model": "qwen3:8b",
      "Enabled": true,
      "HealthTimeoutSeconds": 2,
      "InteractiveTimeoutSeconds": 30,
      "BackgroundTimeoutSeconds": 120
    },
    "External": {
      "Backend": "openai",
      "BaseURL": "https://api.openai.com",
      "Model": "gpt-5-mini",
      "Enabled": false,
      "ExternalTimeoutSeconds": 90
    },
    "Routing": {"Mode": "local_only", "Privacy": "local_only"}
  },
  "Accounts": [
    {
      "ID": "personal",
      "Name": "Personal Gmail",
      "Provider": "gmail",
      "TokenKey": "google.personal",
      "Enabled": true,
      "Integrations": ["google_tasks", "google_calendar"],
      "TaskListID": "@default",
      "CalendarID": "primary"
    },
    {
      "ID": "work",
      "Name": "Work Outlook",
      "Provider": "outlook",
      "TokenKey": "microsoft.work",
      "RedirectURL": "http://127.0.0.1:53682/oauth/callback",
      "Enabled": true
    }
  ]
}
```

Unknown fields and unsafe routing combinations fail at startup.

For Google, enable the Gmail, Calendar, and Tasks APIs; configure a consent
screen and test users; then create a Desktop OAuth client. Projects left in
Google's external “Testing” state may receive short-lived refresh tokens. For
Microsoft, register a public desktop client supporting organizational and
personal accounts, add the configured loopback redirect, and do not create a
client secret.

Authorize and sync:

```sh
mailman auth personal
mailman auth work
mailman doctor
mailman sync
```

Google requests only the scopes implied by enabled integrations:
`gmail.modify`, `gmail.settings.basic`, `calendar.events`,
`calendar.calendarlist.readonly`, and `tasks`. It never requests send or broad
mail access. Outlook requests `Mail.ReadWrite` and
`MailboxSettings.ReadWrite`, plus identity/offline scopes.

To enable explicit external evals, store the OpenAI key under
`openai.api_key` in the `mailman` OS-keyring service and enable External in the
config. There is deliberately no plaintext or environment fallback in normal
application execution.

## Human and automation workflows

Natural text after the binary is compiled locally and printed for review:

```sh
mailman find newsletters older than 90 days and archive them
mailman show alerts from last month
```

Exact commands stay narrow:

```sh
mailman sync --json
mailman schedule run inbox-maintenance
mailman doctor --json
mailman eval run --json
mailman eval run --allow-external --max-external-calls 20
```

An eval run reads `$MAILMAN_DATA_DIR/eval.jsonl`. External execution requires
both the allow flag and a positive visible cap. Probe output never replaces the
local production decision.

Use the host scheduler (cron, launchd, or Task Scheduler) to invoke `mailman
schedule run <name>`. Run it as the logged-in user while their credential store
is unlocked. Each invocation is one-shot: incremental sync, local processing,
rolling draft preparation, then exit—no daemon and no background mutation.

## Safety and provider differences

- Mail actions are desired-state and revision-checked. Trash is recoverable;
  permanent deletion, sending, forwarding, and attendee invitations are absent.
- Gmail uses provider `threadId`; Outlook uses `conversationId`. There is no
  guessed cross-provider thread clustering.
- Gmail filters support create/delete rather than update/disable/order. Existing
  provider rules are imported so local decisions remain aware of them.
- Outlook categories remain separate from folders and unrelated categories are
  preserved during changes.
- Lost responses from non-idempotent creates are recorded as `uncertain` and
  reconciled rather than blindly retried.

## Tests and optional smoke checks

```sh
go test ./...
go vet ./...
go test -race ./...
```

Default tests are offline and use temporary databases and `httptest` fixtures.
Live checks are separately gated so enabling one provider cannot accidentally
enable paid or mutating checks for another:

- `MAILMAN_SMOKE_GMAIL=1` and `MAILMAN_SMOKE_GMAIL_TOKEN`
- `MAILMAN_SMOKE_OUTLOOK=1` and `MAILMAN_SMOKE_OUTLOOK_TOKEN`
- `MAILMAN_SMOKE_TASKS=1` and `MAILMAN_SMOKE_TASKS_TOKEN`
- `MAILMAN_SMOKE_CALENDAR=1` and `MAILMAN_SMOKE_CALENDAR_TOKEN`
- `MAILMAN_SMOKE_OLLAMA=1` and `MAILMAN_SMOKE_OLLAMA_MODEL`
- `MAILMAN_SMOKE_OPENAI=1`, `OPENAI_API_KEY`, and
  `MAILMAN_SMOKE_OPENAI_MODEL` (incurs cost)

The checked-in live provider smoke tests are read-only. Fixture suites exercise
safe label/category, move, rule, task, and event mutation/reconciliation paths.

## Architecture

The central record flow is:

```text
provider page -> atomic SQLite page/checkpoint -> conversations
-> deterministic filtering/grouping -> lazy content -> local primitives
-> policy precedence -> draft plan -> human decisions -> frozen plan -> apply
```

Every model primitive has a versioned prompt/schema and a persisted trace. Eval
cases use immutable input JSON and hashes, and reports keep local/external
quality, latency, tokens, and optional frozen costs separately attributable.
See [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) for domain contracts,
provider semantics, and task ownership.
