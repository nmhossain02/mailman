# Mailman

Mailman is a local, review-first TUI for managing Gmail and Outlook in bulk.
Mailbox changes are previewed before they are applied.

## 1. Requirements

- macOS or Linux
- Git
- Go 1.27 or newer
- [Ollama](https://ollama.com/)
- OAuth credentials for Gmail or Outlook

For Gmail, create a **Desktop app** OAuth client in Google Cloud and enable the
Gmail API. Keep its client ID and client secret ready.

For Outlook, create a public-client application in Microsoft Entra and add this
redirect URI:

```text
http://127.0.0.1:53682/oauth/callback
```

Keep its application client ID ready. Outlook does not need a client secret.

## 2. Set up from source

```sh
git clone https://github.com/nmhossain02/mailman.git
cd mailman
ollama pull qwen3:8b
go run ./cmd/mailman setup
```

The setup command asks for the provider credentials and creates Mailman's local
configuration. It prints the account name to use in the next commands. The
defaults are `personal` for Gmail and `work` for Outlook.

For Gmail:

```sh
go run ./cmd/mailman auth personal
go run ./cmd/mailman doctor
go run ./cmd/mailman sync
go run ./cmd/mailman
```

For Outlook, replace `personal` with `work`.

Inside the TUI:

- `/` opens the natural-language command prompt.
- `1`–`4` switch between conversations, rules, schedules, and plans.
- `p` previews a plan, `f` freezes it, and `a` applies it.
- `q` quits.

Example command:

```text
Find newsletters older than 90 days and archive them
```

## 3. Run, install, or uninstall locally

Run directly from the repository without installing:

```sh
go run ./cmd/mailman
```

Install the current source build for your user:

```sh
go run ./cmd/mailman install
```

Mailman installs to `$HOME/.local/bin/mailman`. If that directory is not on
`PATH`, the command prints the line to add it. You can then run:

```sh
mailman
```

Uninstall the local copy:

```sh
mailman uninstall
```

The equivalent source command is:

```sh
go run ./cmd/mailman uninstall
```

Run tests with `go test ./...`. See [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md)
for the domain and provider contracts.
