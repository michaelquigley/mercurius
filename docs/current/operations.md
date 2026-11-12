# Operations and Troubleshooting

## Monitor a Running Round

`start_review_round` returns a `monitor_command`. Run it in a terminal:

```sh
mercurius monitor --config /abs/path/to/mercurius.yaml --session s_... --wait
```

`--wait` polls the session's latest round's `_state.json` once per second and exits when the round completes or fails, or when the monitor finds it lost. A successfully completed round prints something like:

```text
round 1 completed
log: '/abs/path/to/reviews/s_.../round-01/_round.md'
next: ask the design agent to call collect_round for session 's_...' round 1
```

Without `--wait`, monitor prints the session and every round in it, with each round's state and reviewer, and exits:

```sh
mercurius monitor --config /abs/path/to/mercurius.yaml --session s_...
```

List known sessions:

```sh
mercurius monitor --config /abs/path/to/mercurius.yaml --all
```

The monitor only reads. It reads `session.json` and the round states directly, never writes, and never creates the log destination or the lock file, so it is safe against a read-only log destination. A round marked running whose runner is gone is printed as `lost`; the monitor does not record it, and the next server call on that session does.

## Iterating on `review_context` / `review_focus`

`mercurius.yaml` is the only source for these fields. To preview how the assembled prompt looks before running a real round against the configured reviewer, configure a `dummy` reviewer in `mercurius.yaml`, open a session, and run a round. The dummy reviewer returns instantly, and the assembled prompt is written to `<session>/round-01/_prompt.md` for inspection.

## Long Reviews and MCP Timeouts

Real reviewer runs can outlive an MCP client's tool timeout. Use `start_review_round` and the returned monitor command rather than expecting a synchronous response. If `collect_round` returns the `conflict` code with details indicating the round is still running, the design agent should pause, tell the user to keep monitoring, and collect later.

Every round is bounded by `reviewer.timeout` (default `20m`; see [Configuration](configuration.md)). A round whose reviewer is still running at the deadline is killed and recorded as failed with `details.reason: timeout`, unless the reviewer had already produced a valid review, in which case the round completes with it.

## Server Restarts

Sessions live on disk, so a server restart loses nothing that was recorded. A harness that restarts the server, or shuts it down while idle, during triage leaves the session intact: the next tool call, in a fresh process, reads the session directory and carries on. A session can be opened under one harness and triaged and closed under another.

Rounds currently run inside the server process. If the server dies while a round is running, that round ends with it. Its state still says `running`, and the lock is free; the next `collect_round`, `session_status`, `close_session`, or `start_review_round` records it as failed with `reviewer_failed` and `details.reason: runner_lost`, and the next round starts normally. The round directory, its inputs, and its prompt stay on disk.

## A `conflict` on Launch

One round runs at a time per log destination. A `start_review_round` while another round is running, in this session or any other, answers `conflict`. The details name the running round (`running_session_id`, `running_round_number`), when it started (`started_at`), how long it has been running (`running_for`), and the monitor command for that session. The round is not lost: wait for it to finish, then `collect_round` on that session. Opening a new session does not get around the guard, and is not meant to.

A server or monitor checking for a dead round holds the lock for a moment. `start_review_round` retries a held lock briefly before refusing, so this rarely shows; a launch that still loses that race answers `conflict` and succeeds when repeated.

## Session Files

Each session directory holds `session.json` (the session's id, when it opened, and whether and when it closed) and one directory per round. Each round directory holds `_state.json` (running, completed, or failed, with timings and the stored error), `_run.json` (the reviewer, `max_findings`, calibration presence, and artifact manifest the round ran with), and the round's prompt, config snapshot, artifacts, outcome, and notes. See [Architecture and storage](architecture.md#session-directory-layout) for the full layout. There is no `status.json`.

## Session Synopsis

When `close_session` succeeds, the broker writes `<session>/_synopsis.md`: one durable, human-readable summary of the entire session, rendered from the session directory. It opens with a skimmable summary (round count, failed rounds, reviewers, latest verdict, decision tallies) and continues with a round-by-round detail block that restates each round's state, reviewer, calibration presence, the reviewer's findings, decisions recorded via `record_round_notes`, and any commentary. Failed rounds are listed with their stored error. The per-round files remain the record of each round's raw reviewer output and human notes.

## Error Codes

Five stable codes cover the operational surface:

- `user_error`: caller-fixable. A missing, unparseable, or reviewer-less `mercurius.yaml`; artifact validation; decision validation; a bad log destination.
- `not_found`: the addressed entity does not exist (unknown or malformed session id, a directory that is not a session in this layout, unknown round).
- `conflict`: the operation can't run in the current state (session closed, another round running in the project, round still running, notes on a failed round, session already closed).
- `reviewer_failed`: a round's reviewer subprocess failed, its output failed validation, it timed out (`details.reason: timeout`), or its runner was lost (`details.reason: runner_lost`). Retryable; the retry is the next round.
- `internal_error`: a broker bug, a file Mercurius could not write, or a session or round file that exists and cannot be parsed (the error names the file).

The `details` map on each error response carries context (session id, round number, state path, raw output path, etc.). The agent reads `code` for branching and `message` for human-readable guidance.

Refusals (a `conflict`, a `user_error`) go back to the caller and are not stored anywhere. A round's failure is stored with the round, in its `_state.json`, and `collect_round` returns it for as long as the round directory exists. The stored error is small: `details.cause` is capped at 8 KiB, and the reviewer's raw output is in `_raw.txt`, pointed at by `details.raw_path`.

## Cleanup

Mercurius does not delete session directories, failed rounds included. They are the audit trail. Delete old session directories manually when you no longer need the logs or snapshots; `runner.lock` in the log destination can be left in place or deleted while no round is running.

## Residuals

Hazards the current design leaves open on purpose:

- **A close and a start can cross.** `close_session` checks that no round is running and then writes `closed`, without holding the runner lock. A `start_review_round` on the same session from another process in that gap produces a round in a closed session, which the synopsis already written will not list. It takes two agents acting on one session in the same instant.
- **A reviewer's own children can outlive it.** When a reviewer exits on its own leaving a child behind, the wait delay closes the output pipes and moves on without killing anything. When the timeout fires, only the reviewer process itself is killed.
- **A server death leaves the reviewer running.** Rounds run inside the server process, and nothing ties a reviewer's life to the server's. If the server dies mid-round, the round is recorded as `runner_lost` on the next call, but the reviewer subprocess keeps running until it finishes on its own. It does not hold the runner lock. Kill it by hand if it matters.
- **A dead round leaves reviewer scratch behind.** The codex reviewer's temporary `CODEX_HOME` in the round directory (`.codex-home-*`) is removed on a normal return and not when the round is cut off. It holds symlinks, not copies.
- **A launch can be refused for a moment by a check.** A server marking a dead round, or a monitor looking at one, holds the lock briefly. The retry covers it; a launch that still loses says `conflict` and succeeds when repeated.
