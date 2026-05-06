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

You can override the config-level `review_context` and `review_focus` per session:

```json
{
  "review_context": "This is a one-shot migration with full backup and one supervised implementer.",
  "review_focus": "Pay particular attention to silent migration failures."
}
```

Whitespace-only overrides fall back to the config value. The session response and `session_status` report `review_context_source`, `review_context_present`, `review_focus_source`, and `review_focus_present` so the agent can confirm which value is in effect.

When iterating on `review_focus` (or other config-shaped content), use `mercurius preview` to render the round-1 prompt without running a real round; see `operations.md` for the command reference.

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
  "commentary": "C-1 was fixed in the design.",
  "decisions": [
    {
      "ref": "C-1",
      "disposition": "fixed",
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

Dispositions are `fixed`, `rejected`, or `deferred`:

- `fixed`: agreed and addressed in the artifacts in the same turn.
- `rejected`: disagreed with the finding.
- `deferred`: agreed but explicitly not addressing in this session.

Decision refs match a concern, question, or advisory_note id from the round. Advisory dispositions are recorded in `decisions.md` and flow into the prior-decisions block of future rounds the same way blocking dispositions do, but they do not contribute to the convergence counters - those track blocking-finding triage progress only.

Mercurius writes the notes into the round log and updates `<session_dir>/decisions.md`. Future rounds receive the decisions log and should avoid re-raising adjudicated items unless there is a concrete new reason.

## Decide Whether to Continue

Run another round when the artifacts changed materially or an open question was answered. Stop when:

- The latest round has `verdict: ready_to_build`.
- `triage.findings` is empty.
- `convergence.signal` is `consider_closing`.
- The remaining findings are advisory, rejected, or deferred under the stated context.

`ready_to_build` does not mean zero findings remain. It means the remaining findings - including any deferred or rejected ones - are below the noise floor for the implementer the artifacts are written for, under the stated `review_context`. The verdict reflects a judgment about implementation readiness, not a guarantee of artifact perfection. Advisory notes in particular do not block readiness.

Continued sessions and fresh sessions surface different review angles. A continued session has conversational momentum from prior rounds and tends to keep iterating on dimensions it has already explored. A fresh session against the same artifacts sees them cold and can find different surface area. After multiple continued rounds without convergence, opening a fresh session sometimes surfaces findings the continued arc did not reach.

Close the session with:

```json
{
  "session_id": "s_...",
  "verdict": "ready_to_build"
}
```

Use `paused` when the review is intentionally stopped before readiness. Use `abandoned` when the session is no longer relevant.
