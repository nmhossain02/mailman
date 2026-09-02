# Mailman architecture

Mailman keeps product concepts in one dependency-free domain package and puts
coordination, interfaces, and implementations around it.

```text
cmd/mailman -> bootstrap -> cli, system, tui, application, agent, adapters

tui -----------------------> application -----------------> automation
 |                                |                              |
 +----------------+               +------------------------------+
                  |                              |
                  v                              v
                domain <----------------------- agent
                  ^                              ^
                  |                              |
application/provider <--- google/outlook     agent/eval
          ^                   |
          |                   v
          +--------------- keyring

sqlite -> application/journal, agent, domain
system/config -> domain
system/health -> application/provider, agent, keyring
```

These are compile-time dependencies. At runtime, bootstrap injects an agent
translator and concrete repositories/providers into `application.Service`; the
TUI calls that service only through `application.Workbench`.

## Package responsibilities

- `internal/domain`: shared mail, conversation, rule, plan, and operation
  records plus their validation. It imports only the Go standard library.
- `internal/application`: user-facing use cases and the ports those use cases
  require. The TUI talks to its `Workbench` interface.
- `internal/agent`: natural-language translation, task execution, inference
  routing, cache and trace behavior, and model contracts. `ollama` and `openai`
  contain model adapters; `eval` owns datasets, grading, metrics, and benchmark
  runs.
- `internal/automation`: rule evaluation and periodic preparation of plans.
- `internal/adapters`: concrete infrastructure implementations: SQLite
  repositories, Gmail and Outlook providers, and the OS-backed credential
  store.
- `internal/tui`: rendering and input handling. It does not construct providers
  or model backends.
- `internal/cli`: exact argument parsing and request classification. It performs
  no runtime wiring.
- `internal/system`: configuration, health checks, and local installation.
- `internal/bootstrap`: the only composition root; it constructs concrete
  implementations and connects them.
- `cmd/mailman`: a thin executable entry point.

`internal/application/provider` defines the provider-neutral contract consumed
by synchronization and plan workflows; Gmail and Outlook implement it. This
lets use cases change without making provider adapters the owners of product
policy. The credential-store interface is currently an explicit exception: it
lives beside its OS-keyring implementation while its longer-term ownership is
tracked in [ISSUES.md](ISSUES.md).

## Enforced dependency rules

`internal/architecture/dependencies_test.go` inspects the production import
graph with `go list`. The test fails when, for example, the TUI imports an
adapter, the agent imports the TUI, or the domain imports another internal
package. Bootstrap is intentionally allowed to import all layers because wiring
them together is its sole responsibility. Tests may use concrete adapters as
fixtures without changing the shipped dependency graph.

Run the boundary check alone with:

```sh
go test ./internal/architecture
```

The full `go test ./...` run includes it automatically.
