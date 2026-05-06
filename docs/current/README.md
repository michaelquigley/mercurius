# Current Mercurius Documentation

These docs describe the current Mercurius implementation.

Mercurius is a local MCP server that lets a design agent ask a reviewer, usually Codex, to review design and work-order artifacts. It snapshots the artifacts, runs a constrained review prompt, validates structured reviewer output, logs each completed round, and returns triage guidance to the design agent.

## Start Here

- [User guide](user-guide.md) explains the real-world workflow.
- [Configuration](configuration.md) explains `mercurius.yaml`.
- [MCP tools](mcp-tools.md) is the tool contract.
- [Reviewer output schema](reviewer-output.md) describes reviewer JSON.
- [Architecture and storage](architecture.md) explains broker behavior, prompts, snapshots, logs, and reviewer invocation.
- [Operations and troubleshooting](operations.md) explains monitoring, prompt previewing, timeouts, errors, and logs.

## Current Capabilities

- Single-project server process loaded from one `mercurius.yaml`.
- Single reviewer per session, selected at `open_session`.
- Codex reviewer implementation plus dummy reviewer for tests and scaffolding.
- Background review rounds with `start_review_round`, `round_status`, and `collect_round`.
- Per-round artifact snapshots and markdown logs.
- Structured reviewer output with blocking findings and separate advisory notes.
- Session-level `review_context`, decisions carry-forward, durable `decisions.md`, and convergence hints.
- CLI monitoring through `mercurius monitor` and unsighted prompt preview through `mercurius preview`.

## Current Non-Goals

- Mercurius does not edit artifacts.
- Mercurius does not orchestrate or persist the design agent's chat.
- Mercurius does not run headless CI workflows.
- Mercurius does not multiplex multiple projects in one server process.
- Mercurius does not yet run multi-reviewer panels or diff rounds.
