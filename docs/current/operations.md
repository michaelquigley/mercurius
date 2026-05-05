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

## Long Reviews and MCP Timeouts

Real reviewer runs can outlive an MCP client's tool timeout. Use `start_review_round` rather than expecting a synchronous response. If `collect_round` returns `round_in_progress`, the design agent should pause, tell the user to keep monitoring, and collect later.

## Status Files

Each session directory contains:

- `status.json`: latest session state, active/latest round job, last error, and convergence signal.
- `events.ndjson`: append-only event stream.

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
- `invalid_budget`: session budget is not greater than zero.
- `unknown_reviewer`: selected reviewer is not configured.
- `panel_mode_unsupported`: zero or multiple reviewers resolved in the current single-reviewer implementation.
- `unknown_session`: session id is not known to the running server.
- `session_closed`: the session is closed.
- `budget_exhausted`: successful rounds already equal the budget.
- `round_in_progress`: another round is active or the requested round is still running.
- `reviewer_failed`: reviewer subprocess or implementation failed before producing valid raw output.
- `schema_violation`: raw reviewer output failed schema, readiness consistency, or finding-limit validation.
- `unknown_round`: requested round does not exist.
- `empty_notes`: `record_round_notes` had no commentary and no decisions.
- `unknown_ref`: a decision ref does not match a concern or question in the round.
- `invalid_decision`: decision disposition is not accepted, rejected, or deferred.
- `invalid_verdict`: close verdict is not ready_to_build, paused, or abandoned.

## Interpreting Convergence

`convergence.signal` is advisory:

- `none`: no useful convergence signal yet.
- `watch`: the session may be entering diminishing returns.
- `consider_closing`: the latest round is ready or has no blocking findings.

Mercurius never enforces convergence. The human decides whether another round is worth the cost.

## Cleanup

Mercurius does not delete successful session directories. They are the audit trail. Delete old session directories manually when you no longer need the logs or snapshots.

Failed rounds are cleaned up atomically by Mercurius: no round log, no snapshot directory, no budget consumption.
