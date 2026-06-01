# Current Mercurius Documentation

These docs describe the current Mercurius implementation.

Mercurius is a local MCP server that lets a design agent ask a reviewer, usually Codex, to review design and work-order artifacts. A session is a lightweight grouping of related single-shot rounds; each round snapshots its artifacts, runs a constrained review prompt, validates structured reviewer output, logs the round, and returns triage guidance to the design agent.

## Start Here

- [User guide](user-guide.md) explains the real-world workflow.
- [Agent guide](agent-guide.md) is a portable, agent-facing playbook for driving a review well; hand it to the agent you use with Mercurius.
- [Configuration](configuration.md) explains `mercurius.yaml`.
- [MCP tools](mcp-tools.md) is the tool contract.
- [Reviewer output schema](reviewer-output.md) describes reviewer JSON.
- [Architecture and storage](architecture.md) explains broker behavior, prompts, snapshots, logs, and reviewer invocation.
- [Operations and troubleshooting](operations.md) explains monitoring, timeouts, errors, and logs.

## Current Capabilities

- Single-project server process loaded from one `mercurius.yaml`.
- One reviewer per server, configured singularly in the YAML.
- Codex reviewer implementation plus dummy reviewer for tests and scaffolding.
- Single-shot rounds: artifacts and findings are scoped to one round. Sessions are light groupings; no shared state flows between rounds in the same session.
- Background rounds with `start_review_round` and `collect_round`. Use `mercurius monitor --wait` to poll for completion.
- Per-round artifact snapshots and markdown logs in a self-contained round directory; commentary and decisions land in a sibling `_notes.md` file.
- Six-tool MCP surface (`open_session`, `start_review_round`, `collect_round`, `record_round_notes`, `close_session`, `session_status`) and five stable error codes (`user_error`, `not_found`, `conflict`, `reviewer_failed`, `internal_error`).
- Structured reviewer output with blocking findings (`concerns`, `questions`) and separate `advisory_notes`.
- `review_context`, `review_focus`, and `settled_decisions` live in `mercurius.yaml` and are re-read at the start of every round, so edits between rounds take effect with no session reopen. `review_context` is calibration (stable framing); `settled_decisions` are guards (decisions the reviewer should stop re-raising).

## Current Non-Goals

- Mercurius does not edit artifacts.
- Mercurius does not orchestrate or persist the design agent's chat.
- Mercurius does not run headless CI workflows.
- Mercurius does not multiplex multiple projects in one server process.
- Mercurius does not yet run multi-reviewer panels or diff rounds.
