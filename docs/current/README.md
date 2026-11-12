# Current Mercurius Documentation

These docs describe the current Mercurius implementation.

Mercurius is a local MCP server that lets a design agent ask a reviewer, usually Codex, to review design and work-order artifacts. A session is a lightweight grouping of related single-shot rounds; each round snapshots its artifacts, runs a constrained review prompt, validates structured reviewer output, logs the round, and returns triage guidance to the design agent. Sessions and rounds live entirely on disk: the server holds nothing between tool calls, so any server process can serve any session.

## Start Here

- [User guide](user-guide.md) explains the real-world workflow.
- [Agent guide](agent-guide.md) is a portable, agent-facing playbook for driving a review well; hand it to the agent you use with Mercurius.
- [Configuration](configuration.md) explains `mercurius.yaml`.
- [MCP tools](mcp-tools.md) is the tool contract.
- [Reviewer output schema](reviewer-output.md) describes reviewer JSON.
- [Architecture and storage](architecture.md) explains broker behavior, prompts, snapshots, logs, and reviewer invocation.
- [Operations and troubleshooting](operations.md) explains monitoring, timeouts, errors, and logs.

## Current Capabilities

- Single-project server process configured by one `mercurius.yaml`, read on every tool call rather than at startup; the server starts without the file and a missing or broken file is a tool error.
- Sessions on disk: `session.json` per session and a self-contained directory per round, with machine records (`_state.json`, `_run.json`, `_review.json`, `_notes.json`) beside the human ones. No in-memory registry and no `status.json`.
- Two config files: the reviewer is set once per installation in the user-level config (`~/.config/mercurius/config.yaml`), and the project's `mercurius.yaml` holds calibration, guards, `max_findings`, and `log_destination`. A project picks its own reviewer only through `reviewer_override`; a plain `reviewer` block in the project file is an error.
- One reviewer per round, resolved per round with `max_findings` and the calibration, and reported with its source (`user config` or `project override`) wherever it is named.
- Rounds bounded by `reviewer.timeout` (default `20m`); one round at a time per log destination, enforced by a kernel lock on `runner.lock`, with a `conflict` on a second launch that names the running round.
- Failed rounds stay on disk under their number with their stored error and raw output (`_raw.txt`); a round whose runner died is recorded as `runner_lost` by the next reader.
- Codex, Claude, and pi reviewer implementations plus a dummy reviewer for tests and scaffolding.
- Single-shot rounds: artifacts and findings are scoped to one round. Sessions are light groupings; no shared state flows between rounds in the same session.
- Background rounds with `start_review_round` and `collect_round`, each run in its own detached runner process that survives the server and takes the reviewer down with it if it dies; the round records its pids and whether it outlived its server. Use `mercurius monitor --wait` to poll for completion; the monitor only reads.
- Per-round artifact snapshots and markdown logs in a self-contained round directory; commentary and decisions land in `_notes.json` and `_notes.md`.
- Six-tool MCP surface (`open_session`, `start_review_round`, `collect_round`, `record_round_notes`, `close_session`, `session_status`) and five stable error codes (`user_error`, `not_found`, `conflict`, `reviewer_failed`, `internal_error`).
- Structured reviewer output with blocking findings (`concerns`, `questions`) and separate `advisory_notes`.
- `review_context`, `review_focus`, and `settled_decisions` live in `mercurius.yaml` and are re-read at the start of every round, so edits between rounds take effect with no session reopen. `review_context` is calibration (stable framing); `settled_decisions` are guards (decisions the reviewer should stop re-raising).

## Current Non-Goals

- Mercurius does not edit artifacts.
- Mercurius does not orchestrate or persist the design agent's chat.
- Mercurius does not run headless CI workflows.
- Mercurius does not multiplex multiple projects in one server process.
- Mercurius does not yet run multi-reviewer panels or diff rounds.
