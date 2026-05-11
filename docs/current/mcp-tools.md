# MCP Tools

Mercurius exposes these MCP tools:

- `open_session`
- `start_review_round`
- `round_status`
- `collect_round`
- `record_round_notes`
- `close_session`
- `session_status`
- `list_reviewers`
- `list_sessions`

Expected broker failures are returned as tool error results with structured content:

```json
{
  "error": {
    "code": "stable_code",
    "message": "human-readable message",
    "details": {},
    "retryable": false,
    "next_action": "agent-facing guidance"
  }
}
```

## `open_session`

Opens a review session - a lightweight container for one or more rounds.

Request:

```json
{
  "reviewers": ["codex"]
}
```

`reviewers` is optional when the config has exactly one reviewer. `review_context` and `review_focus` are read from `mercurius.yaml`; they are not tool inputs. Edit the YAML before opening a session if you want different calibration.

Response includes `session_id`, `max_findings`, the selected reviewer, and the `review_context_present` and `review_focus_present` booleans so the agent can confirm the YAML provides those fields.

Common errors: `unknown_reviewer`, `panel_mode_unsupported`, `invalid_log_destination`.

## `start_review_round`

Starts one background review round and returns immediately. Artifacts are required and scoped to this round only - nothing from a prior round in the same session carries over.

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

Response includes round number, state, reviewer, status path, events path, monitor command, and next action. The design agent should tell the user to monitor and re-engage when the round completes.

Common errors: `unknown_session`, `session_closed`, `round_in_progress`, `invalid_artifacts`.

## `round_status`

Returns status for a running or terminal round.

Request:

```json
{ "session_id": "s_...", "round_number": 1 }
```

If `round_number` is omitted, Mercurius returns the active round, latest round job, or latest completed round.

Common errors: `unknown_session`, `unknown_round`.

## `collect_round`

Returns a completed round result with triage guidance.

Request:

```json
{ "session_id": "s_...", "round_number": 1 }
```

Response includes:

- `manifest`: snapshot metadata.
- `reviewers`: raw reviewer outputs and usage notes.
- `triage.findings`: blocking concerns and questions, in reviewer-emitted order.
- `triage.advisory_notes`: non-blocking polish.
- `triage.next_finding`: default first blocking finding (highest severity, ties broken by id).
- `triage.guidance`: instructions for the design agent on how to walk findings.
- `next_action`: a one-line summary of what the agent should do next.

`triage.guidance` and `next_action` direct the agent to walk findings one at a time, explaining each finding and its proposed solution clearly and simply (using few words), discussing it with the user, implementing the fix once aligned, and then stopping before moving to the next finding.

If the round is still running, the tool returns `round_in_progress`. The agent should not immediately retry in a loop; it should ask the user to monitor and re-engage after completion.

Common errors: `unknown_session`, `unknown_round`, `round_in_progress`, `reviewer_failed`, `schema_violation`.

## `record_round_notes`

Records commentary and human decisions for a completed round. This call implicitly finalizes the round - there is no separate `close_round` step.

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

Dispositions are `fixed`, `rejected`, or `deferred`. Decision refs must match a concern, question, or advisory_note id from this round. The tool updates the round log only; decisions do not carry forward into future rounds.

Common errors: `unknown_session`, `unknown_round`, `empty_notes`, `unknown_ref`, `invalid_decision`.

## `close_session`

Closes a session. Sessions are light groupings of rounds; closure just marks the arc done.

Request:

```json
{ "session_id": "s_..." }
```

Common errors: `unknown_session`, `already_closed`, `round_in_progress`.

## `session_status`

Returns a read-only session view: state, opened/closed timestamps, configured `max_findings`, `review_context_present` and `review_focus_present` booleans, the selected reviewer, the latest active/last round job, completed rounds, and last error.

Request:

```json
{ "session_id": "s_..." }
```

Common errors: `unknown_session`.

## `list_reviewers`

Lists configured reviewers. Use this before `open_session` when a config contains multiple reviewers.

Request:

```json
{}
```

## `list_sessions`

Lists sessions known to the running server process.

Request:

```json
{}
```
