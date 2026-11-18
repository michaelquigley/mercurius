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

`mercurius.yaml` is the only source for these fields. To preview how the assembled prompt looks before running a real round against the configured reviewer, give the project a `dummy` reviewer through `reviewer_override` (and remove it afterward), open a session, and run a round. The dummy reviewer returns instantly, and the assembled prompt is written to `<session>/round-01/_prompt.md` for inspection.

## Long Reviews and MCP Timeouts

Real reviewer runs can outlive an MCP client's tool timeout. Use `start_review_round` and the returned monitor command rather than expecting a synchronous response. If `collect_round` returns the `conflict` code with details indicating the round is still running, the design agent should pause, tell the user to keep monitoring, and collect later.

Every round is bounded by `reviewer.timeout` (default `20m`; see [Configuration](configuration.md)). A round whose reviewer is still running at the deadline has the reviewer's whole process group killed, its own children included, and is recorded as failed with `details.reason: timeout`, unless the reviewer had already produced a valid review, in which case the round completes with it.

## Server Restarts

Sessions live on disk, so a server restart loses nothing that was recorded. A harness that restarts the server, or shuts it down while idle, during triage leaves the session intact: the next tool call, in a fresh process, reads the session directory and carries on. A session can be opened under one harness and triaged and closed under another.

A round runs in its own process, detached from the server, so a server that dies mid-round does not take the round with it. The runner finishes the review, writes the outcome, and marks the round `outlived_server`; `collect_round` from the next server returns it as usual. `session_status` and the synopsis show the mark per round.

If the runner itself dies (killed, or the machine goes down), the kernel kills its reviewer with it. The round's state still says `running`, and the lock is free; the next `collect_round`, `session_status`, `close_session`, or `start_review_round` records it as failed with `reviewer_failed` and `details.reason: runner_lost`, and the next round starts normally. The round directory, its inputs, its prompt, and `_runner.log` stay on disk.

## The Runner's Log

Each round's runner writes its own log to `<session>/round-NN/_runner.log`: when the round started running, the runner's and the reviewer's pids, and how the round ended, including whether it outlived its server. A runner that could not start the round at all, or could not write its final state, says why here. It is the first place to look when a round failed in a way `collect_round` does not explain.

## Killing a Round by Hand

The timeout bounds every round, so a hung review ends on its own. To end one sooner, read the pids the round recorded: `runner_pid` and `reviewer_pid` in its `_state.json`, also given as `runner_pid` in the details of a launch `conflict`. Kill the runner:

```sh
kill -KILL <runner_pid>
```

The kernel takes the reviewer down with it, and the next server call records the round as `runner_lost`. Check the pid before you use it: the pids are recorded for you to read, and a round found dead long after the fact may name a pid that now belongs to something else. Mercurius itself never signals a recorded pid.

## A `conflict` on Launch

One round runs at a time per log destination. A `start_review_round` while another round is running, in this session or any other, answers `conflict`. The details name the running round (`running_session_id`, `running_round_number`), when it started (`started_at`), how long it has been running (`running_for`), its runner's pid (`runner_pid`), whether it was started by the server now answering (`started_by_this_server`), and the monitor command for that session. A round started by another server usually means an earlier server died and its round carried on, or another harness is reviewing in this project. The round is not lost: wait for it to finish, then `collect_round` on that session. Opening a new session does not get around the guard, and is not meant to.

A server or monitor checking for a dead round holds the lock for a moment. `start_review_round` retries a held lock briefly before refusing, so this rarely shows; a launch that still loses that race answers `conflict` and succeeds when repeated.

## Session Files

Each session directory holds `session.json` (the session's id, when it opened, and whether and when it closed) and one directory per round. Each round directory holds `_state.json` (running, completed, or failed, with timings, the runner's and reviewer's pids, whether the round outlived its server, and the stored error), `_run.json` (the reviewer, `max_findings`, calibration presence, and artifact manifest the round ran with), `_runner.log`, and the round's prompt, config snapshot, artifacts, outcome, and notes. See [Architecture and storage](architecture.md#session-directory-layout) for the full layout. There is no `status.json`.

## Session Synopsis

When `close_session` succeeds, the broker writes `<session>/_synopsis.md`: one durable, human-readable summary of the entire session, rendered from the session directory. It opens with a skimmable summary (round count, failed rounds, reviewers, latest verdict, decision tallies) and continues with a round-by-round detail block that restates each round's state, reviewer, calibration presence, the reviewer's findings, decisions recorded via `record_round_notes`, and any commentary. Failed rounds are listed with their stored error, and rounds that outlived their server are marked. The per-round files remain the record of each round's raw reviewer output and human notes.

## Error Codes

Five stable codes cover the operational surface:

- `user_error`: caller-fixable. A missing or unparseable `mercurius.yaml`, or a `reviewer` block in it; an invalid user-level config; no reviewer configured in either file; artifact validation; decision validation; a bad log destination.
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
- **A reviewer's own children can outlive it.** The timeout kills the reviewer's whole process group. Two other paths do not: when the runner dies, the parent-death signal reaches the reviewer and not the processes it started; and when the reviewer exits on its own leaving a child behind, the wait delay closes the output pipes and moves on without killing anything.
- **A harness that kills by control group still kills the runner.** The runner's own session and process group escape a group kill and a hangup aimed at the server. They do not escape a cgroup being torn down, as a service manager does when it stops a unit.
- **A dead runner leaves reviewer scratch behind.** The codex reviewer's temporary `CODEX_HOME` in the round directory (`.codex-home-*`) is removed on a normal return and not when the runner is killed. It holds symlinks, not copies.
- **A machine file from a newer build is refused.** Session and round files are read strictly, so a file carrying a field this build does not know answers `internal_error` naming the file, the same as a malformed one. Sessions are short-lived and one build serves an arc, so files are not expected to cross versions.
- **The stored reviewer output is not the reviewer's bytes.** `_review.json` and `collect_round` hold the reviewer's object decoded and written back with sorted keys. Its content is exact, numbers and nulls included; its layout is not what the reviewer printed. `_round.md` shows the reviewer's own bytes.
- **A config key meant for another tool is refused.** An unknown key in either config file is an error, with no way to mark one as deliberate.
- **A launch can be refused for a moment by a check.** A server marking a dead round, or a monitor looking at one, holds the lock briefly. The retry covers it; a launch that still loses says `conflict` and succeeds when repeated.
