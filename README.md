# Mailman

Mailman helps you clean up a large Gmail or Outlook inbox from a local,
review-first TUI. Email is indexed in a local SQLite database, natural-language
requests run through Ollama by default, and nothing changes in your mailbox
until you review and apply a frozen plan.

## Get your inbox into Mailman

The quickest path is: install Mailman, start Ollama, connect one email account,
sync, then open the TUI.

### 1. Install Mailman and start a local model

On macOS or Linux, the installer downloads the matching release binary, verifies
its SHA-256 checksum, and puts it in `$HOME/.local/bin`:

```sh
curl -fsSLO https://raw.githubusercontent.com/nmhossain02/mailman/main/scripts/install.sh
sh install.sh
export PATH="$HOME/.local/bin:$PATH"
mailman version
```

If you already have Go 1.27+, you can instead use Go's versioned installer:

```sh
go install github.com/nmhossain02/mailman/cmd/mailman@latest
```

Install [Ollama](https://ollama.com/) and download the default local model:

```sh
ollama pull qwen3:8b
```

Ollama normally starts when its desktop application opens. If needed, start it
from a terminal with `ollama serve`.

Choose a predictable data directory for the first run:

```sh
export MAILMAN_DATA_DIR="$HOME/.mailman"
mkdir -p "$MAILMAN_DATA_DIR"
```

Mailman stores its SQLite database and configuration there. OAuth tokens remain
in your operating system's credential store, never in this directory.

### 2. Connect Gmail

In Google Cloud:

1. Create or select a project.
2. Enable the Gmail API.
3. Configure the OAuth consent screen and add yourself as a test user if the
   application is in Testing mode.
4. Create an OAuth client with application type **Desktop app**.

Create `$MAILMAN_DATA_DIR/config.json`:

```json
{
  "Core": {
    "Google": {
      "ClientID": "YOUR_GOOGLE_CLIENT_ID",
      "ClientSecret": "YOUR_GOOGLE_CLIENT_SECRET"
    }
  },
  "Accounts": [
    {
      "ID": "personal",
      "Name": "Personal Gmail",
      "Provider": "gmail",
      "TokenKey": "google.personal",
      "Enabled": true
    }
  ]
}
```

Authorize in the browser, verify the connection, and download your mailbox:

```sh
mailman auth personal
mailman doctor
mailman sync
```

Google may show an unverified-app warning for your own Testing-mode project.
Testing-mode external projects can also issue short-lived refresh tokens.
Mailman requests only `gmail.modify` and `gmail.settings.basic`; it cannot send
mail and does not request broad `mail.google.com` access.

Skip to [Use the inbox](#use-the-inbox) once sync completes.

### 2b. Connect Outlook instead

In Microsoft Entra:

1. Register an application that supports organizational and personal Microsoft
   accounts.
2. Enable public-client/desktop flows.
3. Add `http://127.0.0.1:53682/oauth/callback` as a redirect URI.
4. Do not create a client secret.

Use this config instead:

```json
{
  "Core": {
    "Microsoft": {
      "ClientID": "YOUR_MICROSOFT_CLIENT_ID",
      "Tenant": "common"
    }
  },
  "Accounts": [
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

Then run:

```sh
mailman auth work
mailman doctor
mailman sync
```

Outlook requests identity/offline access, `Mail.ReadWrite`, and
`MailboxSettings.ReadWrite`. Mailman does not request permission to send mail.

## Use the inbox

Open the TUI:

```sh
mailman
```

The main keys are:

- `1`–`4`: conversations, rules, schedules, and plans
- `j`/`k` or arrow keys: move
- `Enter`: open a conversation
- `/`: open the natural-command palette
- `q`: quit

Try a command such as:

```text
Find newsletters older than 90 days and archive them
```

The review flow is deliberately explicit:

1. Type the request and press `Enter`.
2. Inspect the compiled filters and actions.
3. Press `p` to preview the target count and operation groups.
4. Use `Space` to exclude or restore individual operations.
5. Press `f` to persist your selections and freeze the plan.
6. Press `a` to apply the frozen plan.

Until the last step, Mailman only reads email and writes local draft state.
Revision checks prevent a stale plan from silently changing a message that has
changed since the preview.

You can also compile a request without entering the TUI:

```sh
mailman find old newsletters and archive them
```

This prints the canonical command for inspection; it does not apply it.

## Add Google Tasks and Calendar

Enable the Google Tasks and Calendar APIs in the same Google project, then add
the integrations to the Gmail account:

```json
{
  "ID": "personal",
  "Name": "Personal Gmail",
  "Provider": "gmail",
  "TokenKey": "google.personal",
  "Enabled": true,
  "Integrations": ["google_tasks", "google_calendar"],
  "TaskListID": "@default",
  "CalendarID": "primary"
}
```

Reauthorize so the new scopes are granted:

```sh
mailman auth personal
mailman doctor
```

Task and event creation remains a high-risk reviewed operation. Mailman does not
invite calendar attendees in v1.

## Keep the inbox updated

Run sync again whenever you want to fetch new mail. Mailman resumes from the
provider checkpoint rather than downloading the whole mailbox again:

```sh
mailman doctor
mailman sync --json
```

The application also contains a one-shot schedule runner for saved schedules.
It performs incremental sync and local processing, updates a rolling draft, and
exits without applying mailbox changes. Run scheduled invocations as the
logged-in user so the OS credential store is available and unlocked.

## Troubleshooting

Start with:

```sh
mailman doctor
```

It checks the database, OS keyring, Ollama, provider refresh/profile access, and
separately enabled Tasks and Calendar integrations.

Common failures:

- **Ollama unavailable:** open Ollama or run `ollama serve`, then confirm
  `ollama list` contains `qwen3:8b`.
- **Keyring unavailable:** run Mailman in the logged-in desktop session, not as
  another user or from a locked headless session.
- **OAuth redirect error:** make the configured Outlook redirect URI exactly
  match the Entra registration. Google must use a Desktop OAuth client.
- **No enabled mail accounts:** check the account's `Enabled`, `Provider`, and
  `TokenKey` values, then run `auth` again.
- **Google refresh expires repeatedly:** publish the OAuth app or move it out of
  external Testing mode when appropriate.

Set `MAILMAN_DATA_DIR` in every terminal or scheduler invocation. Without it,
Mailman uses the operating system's user-config directory under `mailman`.

## Optional external evaluation

Normal inbox processing is local-only. OpenAI is optional and is used only for
an explicitly allowed fallback or benchmark probe.

Store the API key as `openai.api_key` in the `mailman` OS-keyring service, enable
the External model in `config.json`, and place the benchmark dataset at
`$MAILMAN_DATA_DIR/eval.jsonl`.

```sh
mailman eval run --json
mailman eval run --allow-external --max-external-calls 20
```

External execution requires both the allow flag and a positive visible cap.
Probe output never replaces the local production decision.

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

Run the offline test suite:

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

See [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) for the domain contracts and
provider semantics.
