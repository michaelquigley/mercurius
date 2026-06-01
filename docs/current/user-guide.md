# User Guide

This guide describes the normal human workflow for using Mercurius with a design agent and a reviewer.

## Mental Model

You stay in the design agent's chat. The design agent calls Mercurius through MCP. Mercurius runs the configured reviewer against the supplied artifacts and returns structured feedback. You decide what matters, what to fix, what to defer, and when to stop.

A **session** is a lightweight grouping of one or more **rounds**. Each round is atomic: it snapshots its artifacts, dispatches one review, returns findings, accepts notes/decisions, and that's it. No state from a round flows into the next round in the same session. The common workflow is to open a session and run several rounds in it: review, fix what you and the agent agree on, run another round, and so on - editing the artifacts (and, if needed, the YAML's calibration or guards) between rounds. Close the session when the arc is done.

Mercurius is a reviewer broker, not an editor. It reads artifacts, snapshots them, asks for review, and records the audit trail. Artifact edits happen outside Mercurius.

## Prepare a Project

Create a `mercurius.yaml` in the project you want reviewed:

```yaml
max_findings: 8
review_context: |
  Deployment: personal project, single supervised implementer.
  Preference: simple over highly defensive when both are correct.
settled_decisions:
  - id: observability-out-of-scope
    do_not_flag: missing production-grade observability or multi-tenant concerns
review_focus: |
  Pay particular attention to invariants specific to this project that the
  universal what-to-flag criteria do not already cover.
reviewer:
  name: codex
  impl: codex
  model: gpt-5.5
```

`review_context`, `settled_decisions`, and `review_focus` are configuration-only - they are not MCP tool inputs. Mercurius re-reads `mercurius.yaml` at the start of every round, so editing them between rounds takes effect on the next round with no session reopen.

Use `review_context` for calibration only: the stable framing of what kind of review this is - deployment model, stakes, scope, simplicity-vs-defensiveness preference. It is true in round one and still true in round fourteen.

Use `settled_decisions` for guards: decisions already made that the reviewer should stop raising (for example, an out-of-scope concern it keeps re-flagging). Each entry is `{id, do_not_flag}`; the reviewer reads `do_not_flag`, while `id` is your handle for editing or removing the guard. The usual way a guard is born is that the reviewer raises something, you reject it as out of scope in that round's notes, and then - to stop it coming back - you promote that rejection into a guard. Earn guards by re-litigation; do not add them speculatively.

Use `review_focus` to direct the reviewer at project-specific surfaces that the base review philosophy does not already cover. The base prompt already carries the universal subtle-vs-obvious filter, so `review_focus` is typically just one paragraph naming invariants or risks unique to this project.

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

## Open a Session

Ask the design agent to open a Mercurius session. The session itself takes no inputs - the reviewer is fixed in `mercurius.yaml`, and artifacts are passed at round-start time:

```json
{}
```

The response reports the session id, the configured `max_findings`, the bound reviewer, and `review_context_present` / `review_focus_present` booleans. Those booleans are an at-open snapshot of what the config held when the session opened; because calibration is re-read per round, they are informational rather than a guarantee for every later round.

## Run a Round

Ask the design agent to start a round, passing the artifacts under review:

```json
{
  "session_id": "s_...",
  "artifacts": [
    { "name": "design", "path": "/abs/path/to/project/design.md" },
    { "name": "work-order", "path": "/abs/path/to/project/work-order.md" }
  ]
}
```

Artifact paths must be absolute and readable by the Mercurius server. Artifact names become snapshot filenames, so keep them short and safe. Names cannot begin with `_`; that prefix is reserved for broker-emitted meta files (`_round.md`, `_prompt.md`, `_config.yaml`, `_notes.md`) inside the round directory.

Real review rounds can take longer than the MCP client's timeout, so Mercurius runs the round in the background and returns immediately with a monitor command. Run it in a terminal:

```sh
mercurius monitor --config /absolute/path/to/mercurius.yaml --session s_... --wait
```

When the monitor reports completion, re-engage the design agent and ask it to collect the round.

## Walk the Findings

`collect_round` returns:

- `triage.findings`: blocking concerns and questions.
- `triage.advisory_notes`: non-blocking polish or downstream considerations.
- `triage.next_finding`: the default first blocking finding (highest severity, ties broken by id).

The design agent first presents all blocking findings as a brief overview, then presents advisory notes separately, then walks findings one at a time. For each finding, the agent compresses the finding and its proposed solution to the plainest, fewest-words version so you can make a fast call, presents it, and then stops and waits for your decision before acting - confidence that it can predict your answer is not consent. Only after you respond does it implement the fix. Then it stops and waits again before moving to the next finding.

This loop preserves a fresh turn for each finding and keeps the human judgment surface explicit: one finding per turn, both before acting and before advancing.

## Record Notes and Decisions

After finishing the walk-through (or partway through, when you want to take stock), the design agent calls `record_round_notes`. This implicitly finalizes the round - there is no separate close-round step.

```json
{
  "session_id": "s_...",
  "round_number": 1,
  "commentary": "fixes landed for C-1; A-1 deferred.",
  "decisions": [
    { "ref": "C-1", "disposition": "fixed",    "note": "work order now has acceptance test." },
    { "ref": "A-1", "disposition": "deferred", "note": "not worth changing for this scope." }
  ]
}
```

Dispositions are `fixed`, `rejected`, or `deferred`:

- `fixed`: agreed and addressed in the artifacts.
- `rejected`: disagreed with the finding.
- `deferred`: agreed but explicitly not addressing now.

Decision refs match a concern, question, or advisory_note id from this round. Decisions are written to a sibling `_notes.md` file inside the round directory; the immutable round log itself is not touched. Decisions do not carry forward into other rounds in the same session - each round starts cold.

## Decide Whether to Continue

After a round is finalized, you and the agent have three natural options:

- **Run another round in the same session**: the normal next pass - re-snapshot the updated artifacts and get a fresh review. Any edits you made to `mercurius.yaml` between rounds (calibration or guards) are picked up automatically, since the config is re-read at the start of each round.
- **Close the session and open a new one**: still available, but no longer necessary just to pick up YAML edits - closing and reopening used to be the only moment the config was re-read, and that is no longer the case.
- **Stop**: the latest round verdict is `ready_to_build`, or the remaining findings are advisory / deferred / rejected and below the noise floor for your stated `review_context`.

`ready_to_build` does not mean zero findings. It means the remaining findings are below the noise floor for the implementer the artifacts are written for, under the stated `review_context`. Advisory notes never block readiness.

Close the session with:

```json
{ "session_id": "s_..." }
```

There is no verdict on `close_session` - outcomes live on individual rounds. Closing a session marks the arc done and writes `<session>/_synopsis.md`, a tidy human-readable summary of every round (verdicts, findings, decisions, commentary). The path is returned as `synopsis_path` in the close response; open it when you want one durable artifact that describes what happened in this session without walking each round directory.
