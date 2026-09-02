# P0: Pressing `/` freezes the TUI

## Priority

Investigate and fix this before beginning the broader sync reliability, storage, or provider-SDK work in `ISSUES.md`.

## Observed behavior

Pressing `/` in the TUI should open the natural-command palette. Instead, the application appears to freeze and no longer responds normally.

The `/` key branch in `internal/tui/model.go` only changes the model to palette input mode; it does not intentionally call the database, a mail provider, or a model. The existing model tests exercise key handling directly, but there is no test that sends `/` through a running Bubble Tea program and verifies that the next frame renders and accepts input. The cause should therefore be established before changing the palette implementation.

## First investigation

1. Reproduce from a clean launch and record:
   - active TUI view;
   - whether initial conversation loading has completed;
   - terminal and operating-system details;
   - whether `Esc`, `Ctrl+C`, or typed text still produces events;
   - whether the process is idle, consuming CPU, or blocked.
2. Add temporary timing around Bubble Tea `Update` and `View`, then capture goroutine stacks while frozen.
3. Run the same interaction with an empty database and with the current large local database. This separates palette input/rendering problems from work proportional to mailbox size.
4. Add a program-level regression test that sends `/`, waits for the palette frame, types text, and closes it with `Esc` under a timeout.
5. Fix the demonstrated blocking path without moving provider, database, or model work into the UI update loop.

## Acceptance criteria

- `/` renders the command palette promptly from every main view.
- The user can type, backspace, submit, and press `Esc` without the TUI becoming unresponsive.
- Opening the palette performs no mail-provider or model call.
- Slow command interpretation is represented as an explicit busy state and remains cancellable.
- A program-level timeout test reproduces the original failure before the fix and passes afterward.
- Behavior is verified on macOS and Linux terminals.

## Scale and follow-up context

The latest real sync pulled approximately 20,000 messages. That is the target inbox scale, not an edge case. After the TUI freeze is resolved, sync work should prioritize mailbox context management rather than assuming every invocation should traverse the whole mailbox.

The next design should cover:

- filters for account, folder or label, date range, read state, and an explicit provider query where supported;
- a dry-run estimate showing the expected scope before a large initial sync;
- saved sync contexts such as `recent`, `unread`, or selected folders;
- TUI creation, inspection, selection, and resumption of those contexts;
- durable progress so an interrupted 20,000-message run does not restart from the beginning;
- clear distinction between complete mailbox metadata coverage and selectively cached message bodies.

These sync concerns are intentionally tabled until the `/` freeze is understood and fixed.
