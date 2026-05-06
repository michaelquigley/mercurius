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

Mercurius runs as an MCP server that the design agent can call. Each call:

1. Runs the implementing agent against the current design artifacts under a constrained review prompt.
2. Returns structured output — readiness verdict, blocking concerns/questions, advisory notes, optional concrete diffs — rather than free-form prose.
3. Logs the round to a configurable destination so the audit trail accumulates without effort.

The design agent then uses the returned triage hint to present the full blocking-finding list, then discuss one finding at a time with the human before fixing, deferring, or rejecting it. Advisory notes are presented separately as non-blocking polish. The loop continues until the verdict is `ready_to_build` or the human calls it.

Mercurius is reviewer-agnostic by design. Codex is the first implementation; the interface is built so other implementing agents can be swapped in without touching the orchestration layer.

## Status

Mercurius has a working local MCP server, Codex and dummy reviewers, background review rounds, structured output validation, round logs, durable decisions, review context, convergence hints, and CLI monitoring.

- [`docs/current/`](docs/current/README.md) — documentation for the current implementation
- [`docs/current/user-guide.md`](docs/current/user-guide.md) — real-world usage guide
- [`docs/future/`](docs/future/) — future changes and deferred designs
- [`examples/mercurius.yaml`](examples/mercurius.yaml) — starter project config

## Install

From this checkout:

```sh
go install ./cmd/mercurius
```

Then run the MCP server on stdio:

```sh
mercurius --config /absolute/path/to/mercurius.yaml
```

Logs go to stderr because stdout is reserved for MCP transport messages.

## Configuration

Minimal config:

```yaml
default_budget: 4
max_findings: 6
review_context: |
  Add project/session constraints that should calibrate reviewer rigor.
review_focus: |
  Add project-specific things to look for that the base review philosophy
  does not already cover.
reviewers:
  - name: codex
    impl: codex
    model: gpt-5.5
```

Relative `log_destination` and `binary_path` values resolve relative to the config file, not the process working directory. Omit `binary_path` to use the reviewer's default executable lookup.

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
- `round_status`
- `collect_round`
- `record_round_notes`
- `close_session`
- `session_status`
- `list_reviewers`
- `list_sessions`

Manual smoke path: connect an MCP client, call `open_session` with absolute artifact paths for the design/work-order files you want reviewed, call `start_review_round`, monitor it with:

```sh
mercurius monitor --config /absolute/path/to/mercurius.yaml --session <session_id> --wait
```

When the round completes, call `collect_round`, present blocking `triage.findings` with `triage.advisory_notes` separate, handle one selected blocking finding with the user, record commentary/decisions with `record_round_notes`, then continue or close with `close_session`. `collect_round` and `session_status` include an advisory `convergence` signal to help decide when another round is no longer worth the cost.

## License

Apache 2.0. See [`LICENSE`](LICENSE).
