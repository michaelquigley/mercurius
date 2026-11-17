# MCP Tools

Mercurius exposes six MCP tools:

- `open_session`
- `start_review_round`
- `collect_round`
- `record_round_notes`
- `close_session`
- `session_status`

The server holds no session state between calls. Each valid request reads the configuration afresh and consults the session directory as needed. A missing or unparseable `mercurius.yaml`, or one with a plain `reviewer` block, is a `user_error` naming the path in `details.config_path`. An unparseable or invalid user-level config is a `user_error` naming it in `details.user_config_path`. Write or fix the file and call again, with no reconnect. See [Configuration](configuration.md) for the two files.

Every tool except `open_session` requires a string `session_id`. A malformed id string reaches the broker and returns `not_found`. Omitted arguments and a whole arguments value of `null` are treated as an empty object. Inside the arguments object, unknown keys and explicit `null` values are rejected, including in nested objects. Where a tool takes `round_number`, it accepts positive integers written without a decimal point or exponent.

Expected broker failures and invalid arguments are returned as tool error results with structured content:

```json
{
  "error": {
    "code": "user_error",
    "message": "human-readable message",
    "details": {}
  }
}
```

If an argument fails schema validation, the result has `isError: true`, `error.code: "user_error"`, and `error.message: "arguments failed schema validation"`. The validator's reason is in `error.details.cause`. Invalid JSON, duplicate keys, and values that fail strict binding also return `user_error` with a cause.

Error codes (five stable classes):

- `user_error`: caller can fix it (missing or unparseable `mercurius.yaml`, a reviewer block in it, an invalid user-level config, no reviewer configured, artifact validation, decision validation, bad log destination)
- `not_found`: session or round doesn't exist, or the session id is malformed
- `conflict`: operation can't run in the current state (closed session, another round running in the project, round still running, notes on a failed round)
- `reviewer_failed`: a round's reviewer failed, its output failed validation, it timed out, or its runner was lost (retryable; the retry is the next round)
- `internal_error`: broker bug, a write that failed, or a session or round file that cannot be parsed

Refusals are returned to the caller and are not stored anywhere. A round's own failure is stored with the round and returned by `collect_round`.

Timestamps such as `opened_at`, `started_at`, `completed_at`, and `closed_at` in successful results use RFC 3339 with fractional seconds when present. Reviewer descriptions include an `extra_args` array, empty when no arguments are configured. Empty `model` and `binary_path` fields are omitted.

## `open_session`

Opens a review session: a lightweight container for one or more rounds, recorded in `<log_destination>/<session_id>/session.json`. This is the only call that creates the log destination. A session has no reviewer of its own; rounds do.

Request:

```json
{}
```

Response includes `session_id`, `opened_at`, `max_findings`, and the `review_context_present` and `review_focus_present` booleans. If a reviewer is configured, the response includes `reviewer` with its `source`. The reviewer, finding limit, and calibration describe the current configuration and are resolved again and recorded at each round's start. `open_session` succeeds with no reviewer configured. Starting a round requires one.

## `start_review_round`

Starts one background review round and returns immediately. The request requires a nonempty `artifacts` array. Each entry requires a string `name` and an absolute `path`. Artifacts are scoped to the round.

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

Omit `round_number` to collect the session's latest round. An explicit round number must be at least 1.

Response includes:

- `manifest`: snapshot metadata.
- `reviewers`: decoded reviewer JSON objects and usage notes.
- `triage.findings`: blocking concerns and questions, in reviewer-emitted order. An empty list is `[]`.
- `triage.advisory_notes`: non-blocking polish. An empty list is `[]`.
- `triage.next_finding`: default first blocking finding (highest severity, ties broken by id). The field is omitted when there are no blocking findings.
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

The request requires `round_number` of at least 1. `commentary` and `decisions` are optional fields, and the call must supply some commentary or at least one decision. Each decision requires `ref` and `disposition`, with an optional `note`. The schema accepts dispositions `fixed`, `rejected`, and `deferred`. Include the full `decisions` array on the first call. Decision refs must match a concern, question, or advisory note ID from this round. An unknown ref returns the valid refs in the error `details`. Another call on the same round rewrites the notes files. Decisions are scoped to the round.

## `close_session`

Closes a session and writes a human-readable `_synopsis.md` at the session root that summarizes every round, failed rounds included, with each round's state, reviewer, calibration presence, findings, and any recorded decisions. The synopsis is rendered from the session directory, so any server process can close any session. A round still running answers `conflict`. The response includes the absolute path to the synopsis so callers can open it directly.

Request:

```json
{ "session_id": "s_..." }
```

Response fields: `session_id`, `closed_at`, `synopsis_path`.

## `session_status`

Returns `session_id`, `state`, `opened_at`, `round_count`, and `rounds`, which lists every round that was started, including failed rounds. The response includes `closed_at` only when the session is closed. Reviewer, finding limit, and error information belong to the rounds. Each round carries:

- `round_number`, `opened_at`, `state` (`running`, `completed`, or `failed`), and `completed_at` once the round ends;
- `reviewer` (the block the round ran with, with its `source`) and `max_findings`;
- `state_path` (its `_state.json`) and, once completed, `log_path` (its `_round.md`);
- `has_notes` and `decision_count`;
- `outlived_server`, true when the round finished after the server that started it was gone;
- `error`, present only for a failed round.

A latest round marked running whose runner is gone is recorded as failed with `details.reason: runner_lost` before it is reported.

Request:

```json
{ "session_id": "s_..." }
```
