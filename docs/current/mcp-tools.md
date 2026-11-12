# MCP Tools

Mercurius exposes six MCP tools:

- `open_session`
- `start_review_round`
- `collect_round`
- `record_round_notes`
- `close_session`
- `session_status`

Every tool call reads the configuration and the session directory afresh; the server holds no session state between calls, so any server process can serve any session. A missing or unparseable `mercurius.yaml`, or one with a plain `reviewer` block, is a `user_error` from whichever tool is called, naming the path in `details.config_path`. An unparseable or invalid user-level config is a `user_error` naming it in `details.user_config_path`. Write or fix the file and call again, with no reconnect. See [Configuration](configuration.md) for the two files.

Expected broker failures are returned as tool error results with structured content:

```json
{
  "error": {
    "code": "user_error",
    "message": "human-readable message",
    "details": {}
  }
}
```

Error codes (five stable classes):

- `user_error`: caller can fix it (missing or unparseable `mercurius.yaml`, a reviewer block in it, an invalid user-level config, no reviewer configured, artifact validation, decision validation, bad log destination)
- `not_found`: session or round doesn't exist, or the session id is malformed
- `conflict`: operation can't run in the current state (closed session, another round running in the project, round still running, notes on a failed round)
- `reviewer_failed`: a round's reviewer failed, its output failed validation, it timed out, or its runner was lost (retryable; the retry is the next round)
- `internal_error`: broker bug, a write that failed, or a session or round file that cannot be parsed

Refusals are returned to the caller and are not stored anywhere. A round's own failure is stored with the round and returned by `collect_round`.

## `open_session`

Opens a review session: a lightweight container for one or more rounds, recorded in `<log_destination>/<session_id>/session.json`. This is the only call that creates the log destination. A session has no reviewer of its own; rounds do.

Request:

```json
{}
```

Response includes `session_id`, `opened_at`, and, as information about the configuration at this moment, `max_findings`, `reviewer` (the resolved reviewer block with its `source`, or `null` when none is configured), and the `review_context_present` and `review_focus_present` booleans. None of these is stored on the session: the reviewer, `max_findings`, and calibration are resolved again at the start of every round and recorded on that round. `open_session` succeeds with no reviewer configured; `start_review_round` is where the absence becomes an error.

## `start_review_round`

Starts one background review round and returns immediately. Artifacts are required and scoped to this round only - nothing from a prior round in the same session carries over.

The reviewer (from the user-level config, or the project's `reviewer_override`), `max_findings`, and calibration (`review_context`, `review_focus`, `settled_decisions`) are resolved at the start of this round and recorded in the round's `_run.json`, the reviewer with its `reviewer_source`, so edits between rounds take effect immediately with no session reopen. The round also snapshots the exact config bytes it read to `_config.yaml` in the round directory, beside the rendered `_prompt.md`.

Everything is checked before the round is handed to its runner, and a failure leaves no round behind:

- a missing, mid-edit, or unreadable YAML is a `user_error`; fix the file and start the round again;
- no reviewer configured anywhere is a `user_error` naming the user-level config path, where the `reviewer` block belongs;
- a closed session is a `conflict`; artifact problems are a `user_error`;
- another round running anywhere in the project, in this session or any other, is a `conflict`.

The launch `conflict` names the running round in its `details`: `running_session_id`, `running_round_number`, `started_at`, `running_for`, `runner_pid`, `started_by_this_server` (whether the server answering started it), the `monitor_command` for that session, and a `next_action` saying to wait and then call `collect_round` on that session. The round is not lost; collect it rather than relaunching, and do not open a new session to get around the refusal.

A round that fails keeps its number and its directory, so a retry after a failed round 2 is round 3.

Request:

```json
{
  "session_id": "s_...",
  "artifacts": [
    { "name": "design",     "path": "/abs/path/to/design.md" },
    { "name": "work-order", "path": "/abs/path/to/work-order.md" }
  ]
}
```

Response includes `round_number`, `state` (`running`), `reviewer` (the resolved reviewer block the round runs with: `name`, `impl`, `model`, `binary_path`, `extra_args`, `timeout`, and `source`, either `user config` or `project override`), `started_at`, `status_path` (the round's `_state.json`), `monitor_command`, and a `next_action` hint. The design agent should tell the user to monitor and re-engage when the round completes. When the round runs on a project override, `next_action` also tells the agent to tell the user so.

There is no separate `round_status` tool; to check progress, call `collect_round`. If the round is still running, it returns a `conflict` error with the status path embedded in `details`.

## `collect_round`

Returns a completed round result with triage guidance. The result is read from the round's `_run.json` and `_review.json`, so it is answered the same by any server process.

Request:

```json
{ "session_id": "s_...", "round_number": 1 }
```

`round_number` may be omitted to collect the session's latest round.

Response includes:

- `manifest`: snapshot metadata.
- `reviewers`: raw reviewer outputs and usage notes.
- `triage.findings`: blocking concerns and questions, in reviewer-emitted order.
- `triage.advisory_notes`: non-blocking polish.
- `triage.next_finding`: default first blocking finding (highest severity, ties broken by id).
- `triage.guidance`: instructions for the design agent on how to walk findings.
- `next_action`: a one-line summary of what the agent should do next.

`triage.guidance` and `next_action` direct the agent to walk findings one at a time, compressing each finding and its proposed solution to the plainest, fewest-words version, presenting it, and then stopping to wait for the user's decision before acting (confidence that the agent can predict the decision is not consent). The agent implements only after the user responds, then stops again before moving to the next finding.

If the round is still running, the tool returns `conflict`. The agent should ask the user to monitor and re-engage after completion.

If the round failed, the tool returns the stored failure as a tool error, for as long as the round directory exists: the round's code (`reviewer_failed` or `internal_error`) and message, with `round_number`, `state_path`, and, when the reviewer produced output, `raw_path` pointing at `_raw.txt` added to its `details`. `details.reason` is `timeout` for a round that hit its timeout and `runner_lost` for a round whose runner was gone.

## `record_round_notes`

Records commentary and human decisions for a completed round. Notes are written to `_notes.json` and its human twin `_notes.md` inside the round directory; the round log itself is immutable. A running round answers `conflict`, and so does a failed round, which has no findings to decide.

Request:

```json
{
  "session_id": "s_...",
  "round_number": 1,
  "commentary": "markdown commentary",
  "decisions": [
    { "ref": "C-1", "disposition": "fixed", "note": "fix landed in the work order" }
  ]
}
```

Dispositions are `fixed`, `rejected`, or `deferred`. This enum is declared on the tool's input schema, so a client sees the valid values directly and an invalid disposition is rejected at the schema boundary before the broker is reached. Include the full `decisions` array on the first call rather than recording commentary first and decisions later. Decision refs must match a concern, question, or advisory_note id from this round; the valid refs are recomputed from the round's raw reviewer output, and an unknown ref errors with them listed in the error `details`. The notes files are rewritten if called again on the same round; decisions do not carry forward into future rounds.

## `close_session`

Closes a session and writes a human-readable `_synopsis.md` at the session root that summarizes every round, failed rounds included, with each round's state, reviewer, calibration presence, findings, and any recorded decisions. The synopsis is rendered from the session directory, so any server process can close any session. A round still running answers `conflict`. The response includes the absolute path to the synopsis so callers can open it directly.

Request:

```json
{ "session_id": "s_..." }
```

Response fields: `session_id`, `closed_at`, `synopsis_path`.

## `session_status`

Returns a read-only session view: `session_id`, `state`, `opened_at`, `closed_at`, `round_count`, and `rounds`, which lists every round that was started, failed ones included. There is no session-level reviewer, finding limit, or error. Each round carries:

- `round_number`, `opened_at`, `state` (`running`, `completed`, or `failed`), and `completed_at`;
- `reviewer` (the block the round ran with, with its `source`) and `max_findings`;
- `state_path` (its `_state.json`) and, once completed, `log_path` (its `_round.md`);
- `has_notes` and `decision_count`;
- `outlived_server`, true when the round finished after the server that started it was gone;
- `error`, the stored failure of a failed round.

A latest round marked running whose runner is gone is recorded as failed with `details.reason: runner_lost` before it is reported.

Request:

```json
{ "session_id": "s_..." }
```
