# User Guide

This guide describes the normal human workflow for using Mercurius with a design agent and a reviewer.

## Mental Model

You stay in the design agent's chat. The design agent calls Mercurius through MCP. Mercurius runs the configured reviewer against the current artifacts and returns structured feedback. You decide what matters, what to fix, what to defer, and when to stop.

The key rule is that Mercurius is a reviewer broker, not an editor. It reads artifacts, snapshots them, asks for review, and records the audit trail. Artifact edits happen outside Mercurius.

## Prepare a Project

Create a `mercurius.yaml` in the project you want reviewed:

```yaml
default_budget: 4
max_findings: 8
review_context: |
  Deployment: personal project, single supervised implementer.
  Preference: simple over highly defensive when both are correct.
  Out of scope: production-grade observability and multi-tenant concerns.
review_focus: |
  Pay particular attention to invariants specific to this project that the
  universal what-to-flag criteria do not already cover.
reviewers:
  - name: codex
    impl: codex
    model: gpt-5.5
```

Use `review_context` to calibrate reviewer rigor. This is where you say whether the work is a one-shot migration, production infrastructure, a personal tool, a security-sensitive change, or something else. Good context reduces over-polishing and helps the reviewer suppress findings that do not apply.

Use `review_focus` to direct the reviewer at project-specific surfaces that the base review philosophy does not already cover. The base prompt already carries the universal subtle-vs-obvious filter, so `review_focus` is typically just one paragraph naming the invariants or risks unique to this project. The two fields divide labor cleanly: `review_context` calibrates rigor; `review_focus` directs attention.

## Run the Server

Install or run Mercurius from this repository:

```sh
go install ./cmd/mercurius
```

Configure your MCP client to launch it:

```json
{
  "mcpServers": {
    "mercurius": {
      "command": "mercurius",
      "args": ["--config", "/absolute/path/to/mercurius.yaml"]
    }
  }
}
```

Mercurius speaks MCP over stdio. Logs go to stderr so stdout stays reserved for MCP messages.

## Start a Review Session

Ask the design agent to open a Mercurius session for the artifacts:

```json
{
  "artifacts": [
    { "name": "design", "path": "/abs/path/to/project/design.md" },
    { "name": "work-order", "path": "/abs/path/to/project/work-order.md" }
  ]
}
```

Artifact paths must be absolute and readable by the Mercurius server. Artifact names become snapshot filenames, so keep them short and safe. Artifact names cannot begin with `_`; that prefix is reserved for broker-emitted meta files inside the snapshot directory (`_prompt.md` today, future meta files may follow).

You can override the config-level review context per session:

```json
{
  "review_context": "This is a one-shot migration with full backup and one supervised implementer."
}
```

## Run a Round

The design agent should call `start_review_round`. Real review rounds can take longer than the MCP client's timeout, so Mercurius starts the work in the background and returns immediately with a monitor command.

Run the monitor command yourself in a terminal:

```sh
mercurius monitor --config /absolute/path/to/mercurius.yaml --session s_... --wait
```

When the monitor reports completion, re-engage the design agent and ask it to collect the round.

## Triage Findings

`collect_round` returns:

- `triage.findings`: blocking concerns and questions only.
- `triage.advisory_notes`: non-blocking polish or downstream considerations.
- `triage.next_finding`: the default first blocking finding to address.
- `convergence`: an advisory signal about whether another round may be worthwhile.

The design agent should present the full blocking finding list first, present advisory notes separately, and then handle one blocking finding in the current turn. For each finding, choose one of:

- Discuss it before deciding.
- Fix the artifact.
- Defer it with rationale.
- Reject it with rationale.

This one-finding-per-turn pattern preserves a fresh tool-call budget and keeps the human judgment surface explicit.

## Record Notes and Decisions

After a finding is handled, the design agent can call `record_round_notes`. Commentary is free-form markdown. Decisions are structured records tied to reviewer refs:

```json
{
  "session_id": "s_...",
  "round_number": 1,
  "commentary": "C-1 was accepted and fixed in the design.",
  "decisions": [
    {
      "ref": "C-1",
      "disposition": "accepted",
      "note": "The work order now includes a concrete acceptance test."
    },
    {
      "ref": "A-1",
      "disposition": "rejected",
      "note": "Advisory only and not worth changing for this one-shot migration."
    }
  ]
}
```

Decision refs must match blocking concerns or questions from the round. Advisory notes are visible to the human but are not valid decision refs in the current implementation.

Mercurius writes the notes into the round log and updates `<session_dir>/decisions.md`. Future rounds receive the decisions log and should avoid re-raising adjudicated items unless there is a concrete new reason.

## Decide Whether to Continue

Run another round when the artifacts changed materially or an open question was answered. Stop when:

- The latest round has `verdict: ready_to_build`.
- `triage.findings` is empty.
- `convergence.signal` is `consider_closing`.
- The remaining findings are advisory, rejected, or deferred under the stated context.

Close the session with:

```json
{
  "session_id": "s_...",
  "verdict": "ready_to_build"
}
```

Use `paused` when the review is intentionally stopped before readiness. Use `abandoned` when the session is no longer relevant.
