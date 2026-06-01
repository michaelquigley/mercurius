# Mercurius

_Mediates the design-build duel._

Mercurius is a workflow broker that sits between a design agent and an implementing agent during the iterative review of design documents and work orders.

## The problem

A common pattern when building software with AI collaborators:

1. A design agent helps you think through and document a piece of work.
2. An implementing agent reviews the design before building, and surfaces concerns.
3. You shovel feedback between the two across rounds until the design is buildable.

Step 3 is the tax. Moving feedback from one agent to another is mechanical work, and the human in the loop ends up spending attention on transport when it should be reserved for decisions.

## What Mercurius does

Mercurius runs as an MCP server that the design agent can call. The design agent opens a session (a light grouping of related rounds), then for each review:

1. Runs the implementing agent against the supplied artifacts under a constrained review prompt.
2. Returns structured output — readiness verdict, blocking concerns/questions, advisory notes — rather than free-form prose.
3. Logs the round to a self-contained per-round directory so the audit trail accumulates without effort.

The design agent then walks the blocking findings one at a time, compressing each finding and its proposed solution to the plainest, fewest-words version, presenting it, and then waiting for the human's decision before acting — the fix lands only after the human responds. Advisory notes are presented separately as non-blocking polish. The loop continues until the verdict is `ready_to_build` or the human calls it.

Rounds are single-shot and self-contained: artifacts and findings are scoped to one round; nothing carries forward between rounds in the same session. The common workflow is `open session → review → fix → review → … → close`: multiple rounds in one session, editing the artifacts (and, if needed, the YAML's calibration or guards) between them. The config is re-read at the start of each round, so edits take effect on the next round with no reopen.

Mercurius is reviewer-agnostic by design. Codex is the first implementation; the interface is built so other implementing agents can be swapped in without touching the orchestration layer.

## Status

Mercurius has a working local MCP server, Codex and dummy reviewers, background single-shot review rounds, structured output validation, per-round logs, and CLI monitoring.

- [`docs/current/`](docs/current/README.md) — documentation for the current implementation
- [`docs/current/user-guide.md`](docs/current/user-guide.md) — real-world usage guide
- [`docs/current/agent-guide.md`](docs/current/agent-guide.md) — portable, agent-facing playbook for driving a review well
- [`docs/future/`](docs/future/) — future changes and deferred designs
- `mercurius bootstrap` — write a starter `mercurius.yaml` into the current directory

## Install

From this checkout:

```sh
go install ./cmd/mercurius
```

Initialize a new project with a starter config:

```sh
cd /path/to/your/project
mercurius bootstrap
```

This writes `mercurius.yaml` into the current directory using the embedded template. Pass `--force` to overwrite an existing file.

Then run the MCP server on stdio:

```sh
mercurius --config /absolute/path/to/mercurius.yaml
```

Logs go to stderr because stdout is reserved for MCP transport messages.

## Configuration

Minimal config:

```yaml
max_findings: 6
review_context: |
  Calibration only: the stable framing of what kind of review this is
  (deployment model, stakes, scope, simplicity-vs-defensiveness preference).
settled_decisions:
  - id: observability-out-of-scope
    do_not_flag: missing production-grade observability or multi-tenant concerns
review_focus: |
  Add project-specific things to look for that the base review philosophy
  does not already cover.
reviewer:
  name: codex
  impl: codex
  model: gpt-5.5
```

`review_context` (calibration), `settled_decisions` (guards — decisions already made that the reviewer should stop re-raising), and `review_focus` are read from this file; they are not MCP tool inputs. Mercurius re-reads the YAML at the start of every round, so edits between rounds take effect on the next round with no session reopen. Relative `log_destination` and `binary_path` values resolve relative to the config file, not the process working directory.

## MCP Usage

Example MCP client configuration:

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

Tools exposed:

- `open_session`
- `start_review_round`
- `collect_round`
- `record_round_notes`
- `close_session`
- `session_status`

Manual smoke path: connect an MCP client, call `open_session` (no artifacts at this stage), call `start_review_round` with absolute artifact paths for the design/work-order files you want reviewed, monitor it with:

```sh
mercurius monitor --config /absolute/path/to/mercurius.yaml --session <session_id> --wait
```

When the round completes, call `collect_round`, walk `triage.findings` one at a time with `triage.advisory_notes` separate, record commentary/decisions with `record_round_notes`, then either start another round in the same session, open a new session, or call `close_session`.

## License

Apache 2.0. See [`LICENSE`](LICENSE).
