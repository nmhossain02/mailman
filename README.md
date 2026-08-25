# Mailman

Mailman helps you clean up a large Gmail or Outlook inbox from a local,
review-first TUI. Email is indexed in local SQLite, natural-language requests
use Ollama by default, and nothing changes in the mailbox until you review and
apply a frozen plan.

## Run Mailman from source

This is the shortest development path. It does not build, copy, or add a binary
to `PATH`.

Requirements: Git, Go 1.27+, and [Ollama](https://ollama.com/).

```sh
git clone https://github.com/nmhossain02/mailman.git
cd mailman
ollama pull qwen3:8b
go run ./cmd/mailman
```

The first run opens a small setup wizard. Choose Gmail or Outlook and paste the
provider's OAuth client ID when prompted. Mailman creates its own private
application directory in the standard macOS or Linux user-config location; do
not create a data directory or JSON file.

After the wizard, authorize and sync the account name it shows. For example:

```sh
go run ./cmd/mailman auth personal
go run ./cmd/mailman doctor
go run ./cmd/mailman sync
go run ./cmd/mailman
```

Ollama normally starts with its desktop application. If it is not running, use
`ollama serve` in another terminal.

## Prepare an email provider

Mailman needs an OAuth application because it operates on your behalf without
sharing a central Mailman credential. This is the only provider-side setup.

### Gmail

In Google Cloud:

1. Create or select a project and enable the Gmail API.
2. Configure the OAuth consent screen. Add yourself as a test user if the app is
   in Testing mode.
3. Create an OAuth client with application type **Desktop app**.
4. Copy its client ID and client secret into Mailman's setup wizard.

The wizard can also enable Google Tasks and Calendar without editing config.
Enable their APIs in the same project if you select them.

Mailman requests `gmail.modify` and `gmail.settings.basic`; it cannot send mail
and does not request broad `mail.google.com` access. Optional Tasks and Calendar
permissions are requested only when selected. Google may show an unverified-app
warning for your own Testing-mode project.

### Outlook

In Microsoft Entra:

1. Register an application that supports organizational and personal Microsoft
   accounts.
2. Enable public-client/desktop flows.
3. Add `http://127.0.0.1:53682/oauth/callback` as a redirect URI.
4. Copy the application (client) ID into Mailman's setup wizard. Do not create a
   client secret.

Outlook requests identity/offline access, `Mail.ReadWrite`, and
`MailboxSettings.ReadWrite`. Mailman does not request permission to send mail.

Run the wizard explicitly at any time before initial configuration with:

```sh
go run ./cmd/mailman setup
```

It will not overwrite an existing configuration. OAuth tokens are stored in the
operating system credential store, not in the application directory.

## Install a release instead

People who are not developing Mailman can install the macOS or Linux release:

```sh
curl -fsSLO https://raw.githubusercontent.com/nmhossain02/mailman/main/scripts/install.sh
sh install.sh
```

The installer verifies the release checksum and installs `mailman` under
`$HOME/.local/bin`. If that directory is not already on `PATH`, the installer
prints the one shell-profile line needed to add it. Then use the same flow with
`mailman` in place of `go run ./cmd/mailman`:

```sh
mailman
mailman auth personal
mailman doctor
mailman sync
mailman
```

## Use the inbox

The main keys are:

- `1`–`4`: conversations, rules, schedules, and plans
- `j`/`k` or arrow keys: move
- `Enter`: open a conversation
- `/`: open the natural-command palette
- `q`: quit

Try this in the command palette:

```text
Find newsletters older than 90 days and archive them
```

Mailman compiles the request into filters and actions. Press `p` to preview,
`Space` to exclude or restore individual operations, `f` to freeze the reviewed
plan, and `a` to apply it. Until `a`, Mailman only reads email and writes local
draft state. Revision checks prevent a stale plan from silently changing a
message that changed after preview.

Natural commands also work outside the TUI:

```sh
go run ./cmd/mailman find old newsletters and archive them
```

This prints the canonical command for inspection; it does not apply it.

## Keep the inbox updated

`sync` resumes from the provider checkpoint rather than downloading the whole
mailbox again:

```sh
go run ./cmd/mailman sync --json
```

The one-shot schedule runner performs incremental sync and local processing,
updates a rolling draft, and exits without applying mailbox changes. Run it as
the logged-in user so the OS credential store is available and unlocked.

## Troubleshooting

Start with:

```sh
go run ./cmd/mailman doctor
```

Common failures:

- **Ollama unavailable:** open Ollama or run `ollama serve`, then confirm
  `ollama list` contains `qwen3:8b`.
- **Keyring unavailable:** run Mailman in the logged-in desktop session, not as
  another user or from a locked headless session.
- **OAuth redirect error:** the Outlook redirect URI must exactly match the
  Entra registration. Google must use a Desktop OAuth client.
- **Google refresh expires repeatedly:** publish the OAuth app or move it out of
  external Testing mode when appropriate.

Mailman chooses the platform config location automatically. `MAILMAN_DATA_DIR`
is available only as an advanced override for development and migration; normal
installation and scheduled runs do not need it.

## Optional external evaluation

Normal inbox processing is local-only. OpenAI is optional and used only for an
explicitly allowed fallback or benchmark probe. External execution requires
both an allow flag and a positive visible cap:

```sh
go run ./cmd/mailman eval run --json
go run ./cmd/mailman eval run --allow-external --max-external-calls 20
```

Probe output never replaces the local production decision. External model and
evaluation configuration is an advanced use case; the generated config remains
at the path printed by setup.

## Safety boundaries

- Plans are desired-state, revision-checked, and explicitly reviewed.
- Trash is recoverable. Permanent deletion, sending, forwarding, and attendee
  invitations are not implemented.
- Gmail uses provider `threadId`; Outlook uses `conversationId`. Mailman does not
  guess cross-provider threads.
- Existing native provider rules are imported and visible alongside local rules.
- Lost responses from non-idempotent remote creates are recorded as `uncertain`
  and reconciled instead of blindly retried.
- Outlook folders and categories stay distinct, and unrelated categories are
  preserved.

## Development and architecture

Run the offline checks directly—no build wrapper is required:

```sh
go test ./...
go vet ./...
go test -race ./...
```

Live smoke tests are separately gated by provider so paid or mutating checks are
never enabled by one global switch. See `integration/live_smoke_test.go` for the
required environment variables.

The processing path is:

```text
provider page -> atomic SQLite page/checkpoint -> conversations
-> deterministic filtering/grouping -> lazy content -> local primitives
-> policy precedence -> draft plan -> human decisions -> frozen plan -> apply
```

Every model primitive has a versioned schema and persisted trace. Eval cases use
immutable input JSON and hashes; reports keep local and external quality,
latency, tokens, and frozen costs separately attributable.

See [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) for domain contracts and
provider semantics.
