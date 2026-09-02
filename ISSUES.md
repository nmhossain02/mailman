# Backlog

- Decide whether the credential-store interface and in-memory fake should move
  out of `internal/adapters/keyring` to a consumer-owned package. Keep the OS
  keyring implementation in the adapter.
- progress reporter is done in-house; is there some tqdm equivalent that we could use instead?
- How are we interfacing with mail APIs currently? Are we leveraging any first party SDKs from providers when possible? This is of great importance
- Sync time estimate; right now, it seems we go on gathering every message without checking at all how long the sync might take
- storage offloading: right now, it seems we need to have equal amount of local storage available as is taken up in the mailbox. Ideally, we are able to have a sort of "paging" system that ensures efficient usage of local disk storage. Depending on what kind of processing we do on old messages, it may not be necessary to have the entirety of mailbox be downloaded at one given moment. This issue requires discussion of overall management/parsing strategy
- resync efficiency - syncing a large mailbox takes forever; it would be sad to have to restart this process from scratch if I were to run "sync" again. We need a dedicated TUI for mailbox context management
- Investigate recovery when a provider returns an unfinished page without a
  continuation cursor (`internal/application/sync.go:114`). The current guard
  stops the sync; define whether retry or resumable recovery is appropriate.

- feature: "context" based workflow where we manage which emails we have context on at a given time before we run processing commands
