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

Starts a review session.

Request:

```json
{
  "artifacts": [
    { "name": "design", "path": "/abs/path/to/design.md" },
    { "name": "work-order", "path": "/abs/path/to/work-order.md" }
  ],
  "reviewers": ["codex"],
  "budget": 4,
  "review_context": "optional session-specific constraints"
}
```

Response includes `session_id`, budget state, `max_findings`, selected reviewer, artifacts, `review_context_source`, and `review_context_present`.

Common errors: `invalid_artifacts`, `unknown_reviewer`, `panel_mode_unsupported`, `invalid_budget`, `invalid_log_destination`.

## `start_review_round`

Starts one background review round and returns immediately.

Request:

```json
{
  "session_id": "s_...",
  "artifacts": [
    { "name": "design", "path": "/abs/path/to/design.md" }
  ]
}
```

`artifacts` is optional. When present, it replaces the session's artifact set only after the round succeeds.

Response includes round number, state, reviewer, status path, events path, monitor command, and next action. The design agent should tell the user to monitor and re-engage when the round completes.

Common errors: `unknown_session`, `session_closed`, `budget_exhausted`, `round_in_progress`, `invalid_artifacts`.

## `round_status`

Returns status for a running or terminal round.

Request:

```json
{ "session_id": "s_...", "round_number": 1 }
```

If `round_number` is omitted, Mercurius returns the active round, latest round job, or latest completed round.

Common errors: `unknown_session`, `unknown_round`.

## `collect_round`

Returns a completed round result with triage and convergence guidance.

Request:

```json
{ "session_id": "s_...", "round_number": 1 }
```

Response includes:

- `manifest`: snapshot metadata.
- `reviewers`: raw reviewer outputs and usage notes.
- `triage.findings`: blocking concerns and questions.
- `triage.advisory_notes`: non-blocking polish.
- `triage.next_finding`: default first blocking finding.
- `convergence`: advisory diminishing-return signal.
- `next_action`: design-agent guidance.

If the round is still running, the tool returns `round_in_progress`. The agent should not immediately retry in a loop; it should ask the user to monitor and re-engage after completion.

Common errors: `unknown_session`, `unknown_round`, `round_in_progress`, `reviewer_failed`, `schema_violation`.

## `record_round_notes`

Records design-agent commentary and human decisions for a completed round.

Request:

```json
{
  "session_id": "s_...",
  "round_number": 1,
  "commentary": "markdown commentary",
  "decisions": [
    {
      "ref": "C-1",
      "disposition": "accepted",
      "note": "fixed in the work order"
    }
  ]
}
```

Dispositions are `accepted`, `rejected`, or `deferred`. Decision refs must match concern or question ids from the round.

The tool updates the round log, writes `<session_dir>/decisions.md`, and feeds decisions into future reviewer prompts.

Common errors: `unknown_session`, `unknown_round`, `empty_notes`, `unknown_ref`, `invalid_decision`.

## `close_session`

Closes a session.

Request:

```json
{ "session_id": "s_...", "verdict": "ready_to_build" }
```

Valid verdicts are `ready_to_build`, `paused`, and `abandoned`.

Common errors: `unknown_session`, `already_closed`, `round_in_progress`, `invalid_verdict`.

## `session_status`

Returns a read-only session view, including budget state, selected reviewer, artifacts, last error, active/latest round job, completed rounds, review context metadata, and convergence.

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
