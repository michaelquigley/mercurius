# MCP Tools

Mercurius exposes six MCP tools:

- `open_session`
- `start_review_round`
- `collect_round`
- `record_round_notes`
- `close_session`
- `session_status`

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

- `user_error` — caller can fix it (artifact validation, decision validation, bad log destination)
- `not_found` — session, round, or ref doesn't exist
- `conflict` — operation can't run in the current state (closed session, in-progress round)
- `reviewer_failed` — reviewer subprocess or output validation failure (retryable)
- `internal_error` — broker bug

## `open_session`

Opens a review session - a lightweight container for one or more rounds. The reviewer is taken from `mercurius.yaml`; there is no choice to make at session-open time.

Request:

```json
{}
```

Response includes `session_id`, `max_findings`, the configured reviewer, and the `review_context_present` and `review_focus_present` booleans so the agent can confirm the YAML provides those fields.

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

Response includes round number, state, status path, monitor command, and a next-action hint. The design agent should tell the user to monitor and re-engage when the round completes.

There is no separate `round_status` tool; to check progress, call `collect_round` — if the round is still running, it returns a `conflict` error with the status path embedded in `details`.

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

If the round is still running, the tool returns `conflict`. The agent should ask the user to monitor and re-engage after completion.

## `record_round_notes`

Records commentary and human decisions for a completed round. Notes are written to a sibling `_notes.md` file inside the round directory; the round log itself is immutable.

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

Dispositions are `fixed`, `rejected`, or `deferred`. Decision refs must match a concern, question, or advisory_note id from this round. The notes file is rewritten in place if called again on the same round; decisions do not carry forward into future rounds.

## `close_session`

Closes a session and writes a human-readable `_synopsis.md` at the session root that summarizes every round, its findings, and any recorded decisions. The response includes the absolute path to the synopsis so callers can open it directly.

Request:

```json
{ "session_id": "s_..." }
```

Response fields: `session_id`, `closed_at`, `synopsis_path`.

## `session_status`

Returns a read-only session view: state, opened/closed timestamps, configured `max_findings`, `review_context_present` and `review_focus_present` booleans, the configured reviewer, the latest active round (if any), completed rounds, and last error.

Request:

```json
{ "session_id": "s_..." }
```
