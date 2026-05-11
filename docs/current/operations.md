# Operations and Troubleshooting

## Monitor a Running Round

`start_review_round` returns a `monitor_command`. Run it in a terminal:

```sh
mercurius monitor --config /abs/path/to/mercurius.yaml --session s_... --wait
```

Without `--wait`, monitor prints current status and known events, then exits:

```sh
mercurius monitor --config /abs/path/to/mercurius.yaml --session s_...
```

List known sessions:

```sh
mercurius monitor --config /abs/path/to/mercurius.yaml --all
```

Monitor output is intentionally operator-facing rather than raw JSON. It reports session state, round count, whether `review_context` and `review_focus` are present in the config, and the active or last round job. A waited round ends with guidance like:

```text
round 1 completed
log: '/abs/path/to/reviews/s_.../round-01/_round.md'
next: ask the design agent to call collect_round for session 's_...' round 1
```

## Preview the Review Prompt

`mercurius preview` renders the prompt that a broker round would send to the reviewer, without creating a session, dispatching a reviewer, or writing any state under `log_destination`. Use it to iterate on `review_context` or `review_focus` (in `mercurius.yaml`) before paying the cost of a real round.

```sh
mercurius preview --config /abs/path/to/mercurius.yaml \
  --artifact design=/abs/path/to/design.md \
  --artifact work-order=/abs/path/to/work-order.md
```

Repeat `--artifact name=path` for each artifact. Names follow the same rules as artifacts under `start_review_round`: 1-64 characters, no leading underscore, no path separators. Paths can be absolute or relative to the process working directory; the parser splits on the first `=` only, so paths containing `=` are handled correctly.

`--output <file>` writes the assembled prompt to a file instead of stdout.

The output is the assembled prompt verbatim. Two preview invocations with the same inputs produce byte-equal output. The prompt is the same one a broker round would have produced, except the artifact `Snapshot path:` line uses the literal sentinel `(preview)` because preview does not snapshot artifacts.

## Long Reviews and MCP Timeouts

Real reviewer runs can outlive an MCP client's tool timeout. Use `start_review_round` and the returned monitor command rather than expecting a synchronous response. If `collect_round` returns `round_in_progress`, the design agent should pause, tell the user to keep monitoring, and collect later.

## Status Files

Each session directory contains:

- `status.json`: latest session state, active/latest round job, last error.
- `events.ndjson`: append-only event stream.
- `round-NN/_prompt.md`: assembled prompt for round NN. Useful for inspecting what the reviewer received. The round log frontmatter's `prompt_path` field points at this file (relative to the round directory).

Events include:

- `session_opened`
- `round_started`
- `artifacts_snapshotting`
- `artifacts_snapshotted`
- `reviewer_started`
- `reviewer_completed`
- `round_completed`
- `round_failed`
- `notes_recorded`
- `session_closed`

## Error Codes

Common broker error codes:

- `invalid_artifacts`: artifact list, name, path, readability, or snapshot failure.
- `unknown_reviewer`: selected reviewer is not configured.
- `panel_mode_unsupported`: zero or multiple reviewers resolved in the current single-reviewer implementation.
- `unknown_session`: session id is not known to the running server.
- `session_closed`: the session is closed.
- `round_in_progress`: another round is active or the requested round is still running.
- `reviewer_failed`: reviewer subprocess or implementation failed before producing valid raw output.
- `schema_violation`: raw reviewer output failed schema, readiness consistency, or finding-limit validation.
- `unknown_round`: requested round does not exist.
- `empty_notes`: `record_round_notes` had no commentary and no decisions.
- `unknown_ref`: a decision ref does not match a concern, question, or advisory_note in the round.
- `invalid_decision`: decision disposition is not `fixed`, `rejected`, or `deferred`.
- `already_closed`: `close_session` was called on a session that is already closed.
- `invalid_log_destination`: configured `log_destination` is not a writable directory.

## Cleanup

Mercurius does not delete successful session directories. They are the audit trail. Delete old session directories manually when you no longer need the logs or snapshots.

Failed rounds are cleaned up atomically by Mercurius: the round directory (including any partial snapshot, prompt log, or round log) is removed; the round counter does not advance.
