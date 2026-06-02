# Operations and Troubleshooting

## Monitor a Running Round

`start_review_round` returns a `monitor_command`. Run it in a terminal:

```sh
mercurius monitor --config /abs/path/to/mercurius.yaml --session s_... --wait
```

`--wait` polls `status.json` once per second and exits when the active round clears (i.e., the round completed or failed). A successfully completed round prints something like:

```text
round 1 completed
log: '/abs/path/to/reviews/s_.../round-01/_round.md'
next: ask the design agent to call collect_round for session 's_...' round 1
```

Without `--wait`, monitor prints the current status snapshot and exits:

```sh
mercurius monitor --config /abs/path/to/mercurius.yaml --session s_...
```

List known sessions:

```sh
mercurius monitor --config /abs/path/to/mercurius.yaml --all
```

## Iterating on `review_context` / `review_focus`

`mercurius.yaml` is the only source for these fields. To preview how the assembled prompt looks before running a real round against the configured reviewer, configure a `dummy` reviewer in `mercurius.yaml`, open a session, and run a round — the dummy reviewer returns instantly, and the assembled prompt is written to `<session>/round-01/_prompt.md` for inspection.

## Long Reviews and MCP Timeouts

Real reviewer runs can outlive an MCP client's tool timeout. Use `start_review_round` and the returned monitor command rather than expecting a synchronous response. If `collect_round` returns the `conflict` code with details indicating the round is still running, the design agent should pause, tell the user to keep monitoring, and collect later.

## Status File

Each session directory contains a `status.json` file with the latest session state, active round (if any), last error, and round history. The broker writes this atomically on every state change.

## Session Synopsis

When `close_session` succeeds, the broker writes `<session>/_synopsis.md`: one durable, human-readable summary of the entire session. It opens with a skimmable summary (round count, latest verdict, decision tallies) and continues with a round-by-round detail block that restates the reviewer's findings, decisions recorded via `record_round_notes`, and any commentary. The synopsis is rendered fresh from in-memory session state at close time; per-round `_round.md` and `_notes.md` files remain the immutable record of each round's raw reviewer output and human notes.

## Error Codes

Five stable codes cover the operational surface:

- `user_error` — caller-fixable: artifact validation, decision validation, bad log destination.
- `not_found` — addressed entity does not exist (unknown session, unknown round, unknown ref).
- `conflict` — operation can't run in the current state (session closed, round already running, session already closed).
- `reviewer_failed` — reviewer subprocess or output validation failure. Retryable.
- `internal_error` — broker bug.

The `details` map on each error response carries context (session id, round number, status path, raw reviewer output, etc.). The agent reads `code` for branching and `message` for human-readable guidance.

## Cleanup

Mercurius does not delete successful session directories. They are the audit trail. Delete old session directories manually when you no longer need the logs or snapshots.

Failed rounds are cleaned up atomically by Mercurius: the round directory (including any partial snapshot, prompt log, or round log) is removed; the round counter does not advance.
